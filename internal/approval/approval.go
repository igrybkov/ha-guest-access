// Package approval admits new devices to links that need approval
// (model.Link.Approval).
//
// A new device asks for access and waits with a short code on screen. The
// request lives in memory only, for Options.PendingTTL. HA gets the event
// guest_access_device_request, and a decision comes back as
// guest_access_device_decision, from the admin API, or from an approved
// device of the same link (self-service). An approved device is stored on
// the link and gets a signed cookie. Every request checks that the device
// is still on the link, so a revoke or reset works at once.
//
// The guest handlers run after web checked the slug, the link window and
// the password session. web also checks Origin and the JSON content type
// of guest POSTs.
package approval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/audit"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// HA event types. The blueprint in blueprints/ uses the same names.
const (
	EventDeviceRequest  = "guest_access_device_request"
	EventDeviceDecision = "guest_access_device_decision"
)

// Cookie name bases. The real name adds the link id (see
// DeviceCookieName), and the path is access.GuestCookiePath, so both
// survive a slug rotation.
const (
	// DeviceCookie holds the signed device token of an approved device.
	DeviceCookie = "ga_device"
	// RequestCookie ties a browser to its pending request. It holds a
	// random secret, never the request id, so knowing a request id (as
	// HA, the admin or an approver does) is not enough to pick up the
	// device cookie.
	RequestCookie = "ga_device_request"
)

// DeviceCookieName is the device cookie's name for link linkID.
func DeviceCookieName(linkID string) string { return access.LinkCookie(DeviceCookie, linkID) }

// RequestCookieName is the request cookie's name for link linkID.
func RequestCookieName(linkID string) string { return access.LinkCookie(RequestCookie, linkID) }

// Defaults for Options. The app options (the "approval" group) override
// them; these only fill in zero fields.
const (
	DefaultPendingTTL    = 10 * time.Minute
	DefaultMaxPending    = 3
	DefaultMaxDevices    = 10
	DefaultEventsPerHour = 5
	DefaultTouchThrottle = 5 * time.Minute
	DefaultMaxBodyBytes  = 16 << 10
	DefaultEventTimeout  = 5 * time.Second
)

// CodeAlphabet is the alphabet of request codes. It has no 0, O, 1, I or
// L, so a code read aloud or off a small screen is not misread.
const CodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// CodeLen is the length of a request code.
const CodeLen = 4

var (
	// ErrUnknownRequest means no pending request has that id: it never
	// existed, expired, was decided already, or belongs to another link.
	ErrUnknownRequest = errors.New("no such pending request")
	// ErrTooManyPending means the link has the most pending requests.
	ErrTooManyPending = errors.New("too many pending requests")
	// ErrTooManyEvents means the link fired its request events for this
	// hour.
	ErrTooManyEvents = errors.New("too many device requests this hour")
)

// LinkStore is the part of *links.Store the manager uses.
type LinkStore interface {
	Get(linkID string) (model.Link, error)
	AddDevice(linkID string, d model.LinkDevice, maxDevices int) (model.LinkDevice, error)
	ClaimDevice(linkID string, d model.LinkDevice, maxDevices int) (model.LinkDevice, error)
	RemoveDevice(linkID, deviceID string) error
	ResetDevices(linkID string) error
	TouchDevice(linkID, deviceID string, now time.Time, every time.Duration) error
}

var _ LinkStore = (*links.Store)(nil)

// Options configure a Manager. Links, FireEvent and Signer are required.
// The limits come from the app options; a zero limit takes its Default*.
type Options struct {
	Links LinkStore
	// FireEvent fires an HA event, like ha.Client.FireEvent.
	FireEvent func(ctx context.Context, eventType string, data map[string]any) error
	Signer    *access.Signer
	// Now is the clock. Default time.Now.
	Now    func() time.Time
	Logger *slog.Logger
	// PendingTTL is how long a request waits, and how long a decided one
	// waits for its browser to pick it up.
	PendingTTL time.Duration
	// MaxPending caps undecided requests per link.
	MaxPending int
	// MaxDevices caps approved devices per link. Lowering it keeps the
	// devices a link has; new ones are refused until it is under the cap.
	MaxDevices int
	// EventsPerHour caps request events per link per hour.
	EventsPerHour int
	// TouchThrottle is the most often a device's LastSeenAt is saved.
	TouchThrottle time.Duration
	// MaxBodyBytes caps the JSON body of a self-service decision. Pass
	// the app's request body limit.
	MaxBodyBytes int64
	// EventTimeout caps one FireEvent call.
	EventTimeout time.Duration
	// Audit records device.* events. nil records nothing.
	Audit *audit.Logger
	// IPv6Prefix keys the refused-request audit throttle on an IPv6
	// client's prefix (see access.KeyAddr). Zero means
	// access.DefaultIPv6Prefix.
	IPv6Prefix int
}

// Request is a pending device request, as the admin API shows it.
type Request struct {
	ID        string    `json:"request_id"`
	LinkID    string    `json:"link_id"`
	Code      string    `json:"code"`
	Label     string    `json:"label,omitempty"`
	Device    string    `json:"device"`
	IP        string    `json:"ip,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GuestRequest is a pending request as an approver device sees it. It has
// no IP.
type GuestRequest struct {
	ID        string    `json:"request_id"`
	Code      string    `json:"code"`
	Label     string    `json:"label,omitempty"`
	Device    string    `json:"device"`
	CreatedAt time.Time `json:"created_at"`
}

type state int

const (
	statePending state = iota
	stateApproved
	stateDenied
)

// request is one request in memory.
type request struct {
	Request
	secret   string // value of RequestCookie
	linkName string // for audit records
	state    state
	deviceID string // set when approved
}

// Manager runs device approval. Safe for concurrent use.
//
// Lock order: Manager.mu, then the link store's locks. The store never
// calls the manager. Decisions and claims hold mu across the store write,
// so a decision and a reset of the same link can't interleave, and one
// request never yields two devices.
type Manager struct {
	opts Options
	log  *slog.Logger

	mu       sync.Mutex
	byID     map[string]*request
	bySecret map[string]*request
	events   map[string][]time.Time // link id -> request events in the last hour, oldest first

	// refused thins out refused device request records. A device that
	// keeps asking past the caps gets one record per period per link,
	// reason and client, which counts the ones left out.
	refused *audit.Throttle
}

// refusedAuditPeriod is the throttle period of refused device requests.
const refusedAuditPeriod = time.Minute

// New returns a Manager. It panics when a required option is missing.
func New(o Options) *Manager {
	if o.Links == nil || o.FireEvent == nil || o.Signer == nil {
		panic("approval: Links, FireEvent and Signer are required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PendingTTL <= 0 {
		o.PendingTTL = DefaultPendingTTL
	}
	if o.MaxPending <= 0 {
		o.MaxPending = DefaultMaxPending
	}
	if o.MaxDevices <= 0 {
		o.MaxDevices = DefaultMaxDevices
	}
	if o.EventsPerHour <= 0 {
		o.EventsPerHour = DefaultEventsPerHour
	}
	if o.TouchThrottle <= 0 {
		o.TouchThrottle = DefaultTouchThrottle
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if o.EventTimeout <= 0 {
		o.EventTimeout = DefaultEventTimeout
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		opts:     o,
		log:      log.With("component", "approval"),
		byID:     map[string]*request{},
		bySecret: map[string]*request{},
		events:   map[string][]time.Time{},
		refused:  audit.NewThrottle(refusedAuditPeriod),
	}
}

func (m *Manager) now() time.Time { return m.opts.Now().UTC().Round(0) }

// Admit decides whether this request's device may use the link. On ok=false it has
// already written a JSON response: 403 {"status":"approval_required"} when no
// request exists yet, or 403 {"status":"pending","code":"K7Q2"} while waiting.
//
// With approval off it returns ok and an empty deviceID. A valid device
// cookie for a device still on the link returns ok; Admit then notes the
// device as seen and renews the cookie if the link's end moved. A request
// that was approved but not picked up yet is handed over here too.
func (m *Manager) Admit(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string) (deviceID string, ok bool) {
	if !link.Approval.On() {
		return "", true
	}
	now := m.now()
	if id, ok := m.deviceFromCookie(w, r, link, c, now); ok {
		return id, true
	}
	m.mu.Lock()
	m.sweep(now)
	req := m.requestFromCookie(r, link.ID)
	if req != nil && req.state == stateApproved {
		id, ok := m.handoff(w, link, c, req)
		m.mu.Unlock()
		if ok {
			return id, true
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"status": "approval_required"})
		return "", false
	}
	var code string
	if req != nil && req.state == statePending {
		code = req.Code
	}
	m.mu.Unlock()
	if code != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"status": "pending", "code": code})
		return "", false
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"status": "approval_required"})
	return "", false
}

// StillValid reports whether deviceID may still use link, without writing
// anything. Open event streams call it with the device that Admit
// returned. With approval off it is true. With approval on, the device
// must still be on the link, so a revoke or reset ends the stream, and so
// does turning approval on while a stream with no device is open.
func (m *Manager) StillValid(link model.Link, deviceID string) bool {
	if !link.Approval.On() {
		return true
	}
	if deviceID == "" {
		return false
	}
	_, ok := link.Device(deviceID)
	return ok
}

// RequestAccess handles the guest's POST asking for access. It answers:
//
//   - 200 {"status":"approved"}: approval is off, the device is approved
//     already, or it just claimed the link (claim mode, no device yet).
//   - 200 {"status":"pending","code":"K7Q2"}: a request waits. A browser
//     that already has a pending request gets the same one back, and no
//     new event fires.
//   - 429 {"status":"try_later"} with Retry-After: the link has the most
//     pending requests, or fired its events for this hour.
func (m *Manager) RequestAccess(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string) {
	if !link.Approval.On() {
		writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
		return
	}
	now := m.now()
	if _, ok := m.deviceFromCookie(w, r, link, c, now); ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
		return
	}
	device := DescribeDevice(r.UserAgent())
	label = Clean(label)

	m.mu.Lock()
	m.sweep(now)
	if req := m.requestFromCookie(r, link.ID); req != nil {
		switch req.state {
		case statePending:
			code := req.Code
			m.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"status": "pending", "code": code})
			return
		case stateApproved:
			if _, ok := m.handoff(w, link, c, req); ok {
				m.mu.Unlock()
				writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
				return
			}
			// The device was revoked before pickup; ask again.
		case stateDenied:
			m.drop(req) // a denied guest may ask again; the event cap still holds
		}
	}

	if link.Approval.Claim() && len(link.Devices) == 0 {
		d, err := m.opts.Links.ClaimDevice(link.ID, model.LinkDevice{
			Label: label, Device: device, ApprovedAt: now, ApprovedBy: model.ApprovedByClaim,
		}, m.opts.MaxDevices)
		switch {
		case err == nil:
			m.mu.Unlock()
			m.log.Info("device claimed link", "link", link.ID, "device", d.ID, "label", label, "ua", device)
			m.record(audit.Record{Event: "device.claimed", Result: audit.ResultAllowed, By: model.ApprovedByClaim,
				LinkID: link.ID, LinkName: link.Name, DeviceID: d.ID, DeviceLabel: label, PasswordLabel: label, Device: device}, c)
			m.setDeviceCookie(w, link, c, d.ID)
			writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
			return
		case !errors.Is(err, links.ErrClaimed):
			m.mu.Unlock()
			m.log.Error("claim link", "link", link.ID, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error"})
			return
		}
		// Another device won the claim; this one asks like any other.
	}

	req, retry, err := m.newRequest(link, label, device, c, now)
	m.mu.Unlock()
	if err != nil {
		m.log.Info("device request refused", "link", link.ID, "label", label, "reason", err)
		reason := "too_many_pending"
		if errors.Is(err, ErrTooManyEvents) {
			reason = "too_many_events"
		}
		if ok, skipped := m.refused.Allow(link.ID+"|"+reason+"|"+access.LimitKey(c.IP, m.opts.IPv6Prefix), now); ok {
			m.record(audit.Record{Event: "device.request", Result: audit.ResultDenied, Reason: reason, Count: skipped,
				LinkID: link.ID, LinkName: link.Name, PasswordLabel: label, Device: device}, c)
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"status": "try_later"})
		return
	}
	m.log.Info("device request", "link", link.ID, "request", req.ID, "code", req.Code, "label", label, "ua", device, "ip", req.IP)
	m.record(audit.Record{Event: "device.request", Result: audit.ResultOK, LinkID: link.ID, LinkName: link.Name,
		PasswordLabel: label, Device: device, RequestID: req.ID, Code: req.Code}, c)
	http.SetCookie(w, &http.Cookie{
		Name: RequestCookieName(link.ID), Value: req.secret, Path: access.GuestCookiePath,
		MaxAge: int(m.opts.PendingTTL / time.Second), Expires: req.ExpiresAt,
		HttpOnly: true, Secure: c.HTTPS, SameSite: http.SameSiteStrictMode,
	})
	m.fire(link, req.Request)
	writeJSON(w, http.StatusOK, map[string]string{"status": "pending", "code": req.Code})
}

// newRequest adds a pending request, or says why not and in how many
// seconds to try again. It requires m.mu.
func (m *Manager) newRequest(link model.Link, label, device string, c access.Client, now time.Time) (*request, int, error) {
	linkID := link.ID
	var pending []*request
	for _, req := range m.byID {
		if req.LinkID == linkID && req.state == statePending {
			pending = append(pending, req)
		}
	}
	if len(pending) >= m.opts.MaxPending {
		first := pending[0].ExpiresAt
		for _, req := range pending {
			if req.ExpiresAt.Before(first) {
				first = req.ExpiresAt
			}
		}
		return nil, retryAfter(first.Sub(now)), ErrTooManyPending
	}
	if ev := m.events[linkID]; len(ev) >= m.opts.EventsPerHour {
		return nil, retryAfter(ev[0].Add(time.Hour).Sub(now)), ErrTooManyEvents
	}
	req := &request{
		Request: Request{
			ID: randomHex(), LinkID: linkID, Code: m.newCode(linkID), Label: label, Device: device,
			CreatedAt: now, ExpiresAt: now.Add(m.opts.PendingTTL),
		},
		secret:   randomHex(),
		linkName: link.Name,
	}
	if c.IP.IsValid() {
		req.IP = c.IP.String()
	}
	m.byID[req.ID], m.bySecret[req.secret] = req, req
	m.events[linkID] = append(m.events[linkID], now)
	return req, 0, nil
}

// fire sends the request event to HA. A failure is logged: the request
// still waits, and the admin UI can decide it.
func (m *Manager) fire(link model.Link, req Request) {
	ctx, cancel := context.WithTimeout(context.Background(), m.opts.EventTimeout)
	defer cancel()
	err := m.opts.FireEvent(ctx, EventDeviceRequest, map[string]any{
		"link_id":    link.ID,
		"link_name":  link.Name,
		"request_id": req.ID,
		"code":       req.Code,
		"label":      req.Label,
		"device":     req.Device,
		"ip":         req.IP,
	})
	if err != nil {
		m.log.Warn("fire device request event", "link", link.ID, "request", req.ID, "err", err)
	}
}

// Status handles the guest's poll. It always answers 200 with one of:
// {"status":"approved"} (and sets the device cookie), {"status":"pending",
// "code":"K7Q2"}, {"status":"denied"} or {"status":"approval_required"}
// when this browser has no request (or it expired).
func (m *Manager) Status(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string) {
	if !link.Approval.On() {
		writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
		return
	}
	now := m.now()
	if _, ok := m.deviceFromCookie(w, r, link, c, now); ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
		return
	}
	m.mu.Lock()
	m.sweep(now)
	req := m.requestFromCookie(r, link.ID)
	resp := map[string]string{"status": "approval_required"}
	switch {
	case req == nil:
	case req.state == statePending:
		resp = map[string]string{"status": "pending", "code": req.Code}
	case req.state == stateDenied:
		m.drop(req)
		clearCookie(w, RequestCookieName(link.ID), c)
		resp = map[string]string{"status": "denied"}
	case req.state == stateApproved:
		if _, ok := m.handoff(w, link, c, req); ok {
			resp = map[string]string{"status": "approved"}
		}
	}
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

// PendingForDevice lists the link's pending requests for an approved
// device of the same link, when the link allows self-service. It answers
// 200 {"requests":[...]} or 403 {"status":"approval_required"}.
func (m *Manager) PendingForDevice(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string) {
	if _, ok := m.approver(w, r, link, c); !ok {
		return
	}
	m.mu.Lock()
	m.sweep(m.now())
	out := []GuestRequest{}
	for _, req := range m.pendingOf(link.ID) {
		out = append(out, GuestRequest{ID: req.ID, Code: req.Code, Label: req.Label, Device: req.Device, CreatedAt: req.CreatedAt})
	}
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

// DecideByDevice handles a self-service decision: a POST of
// {"request_id":"...","approve":true} from an approved device of the same
// link. The request must belong to that link. It answers 200
// {"status":"approved"|"denied"}, 400 on a bad body, 403 when the caller
// may not decide, 404 {"status":"not_found"} for an unknown request, and
// 409 {"status":"too_many_devices"} when the link is full.
func (m *Manager) DecideByDevice(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string) {
	approverID, ok := m.approver(w, r, link, c)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Approve   *bool  `json:"approve"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, m.opts.MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.RequestID == "" || body.Approve == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"status": "bad_request"})
		return
	}
	err := m.decide(body.RequestID, *body.Approve, model.ApprovedByDevicePrefix+approverID, link.ID)
	switch {
	case errors.Is(err, ErrUnknownRequest):
		writeJSON(w, http.StatusNotFound, map[string]string{"status": "not_found"})
	case errors.Is(err, links.ErrTooManyDevices):
		writeJSON(w, http.StatusConflict, map[string]string{"status": "too_many_devices"})
	case err != nil:
		m.log.Error("device decision", "link", link.ID, "request", body.RequestID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error"})
	case *body.Approve:
		writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "denied"})
	}
}

// approver returns the caller's device id when it may decide for link:
// the link allows self-service and the caller holds a valid device cookie
// for a device still on this link. Otherwise it writes 403.
func (m *Manager) approver(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client) (string, bool) {
	if link.Approval.SelfService {
		if id, ok := m.deviceFromCookie(w, r, link, c, m.now()); ok {
			return id, true
		}
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"status": "approval_required"})
	return "", false
}

// Pending lists the pending requests of link linkID, oldest first, for
// the admin API. An empty linkID lists every link's.
func (m *Manager) Pending(linkID string) []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweep(m.now())
	out := []Request{}
	for _, req := range m.pendingOf(linkID) {
		out = append(out, req.Request)
	}
	return out
}

// Decide approves or denies a pending request from the admin API. It
// returns ErrUnknownRequest, links.ErrTooManyDevices, or a store error.
func (m *Manager) Decide(requestID string, approve bool) error {
	return m.decide(requestID, approve, model.ApprovedByAdmin, "")
}

// RemoveDevice revokes one device from the admin API. Its cookie fails on
// the next request. Nothing goes to HA.
func (m *Manager) RemoveDevice(linkID, deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := audit.Record{Event: "device.revoked", Result: audit.ResultOK, LinkID: linkID, DeviceID: deviceID}
	if l, err := m.opts.Links.Get(linkID); err == nil {
		rec.LinkName = l.Name
		if d, ok := l.Device(deviceID); ok {
			rec.DeviceLabel, rec.Device = d.Label, d.Device
		}
	}
	if err := m.opts.Links.RemoveDevice(linkID, deviceID); err != nil {
		return err
	}
	for _, req := range m.byID {
		if req.LinkID == linkID && req.deviceID == deviceID {
			m.drop(req)
		}
	}
	m.log.Info("device revoked", "link", linkID, "device", deviceID)
	m.record(rec, access.Client{})
	return nil
}

// ResetDevices revokes every device of a link and drops its requests,
// from the admin API. In claim mode the next device claims the link again.
// Nothing goes to HA.
func (m *Manager) ResetDevices(linkID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := audit.Record{Event: "device.reset", Result: audit.ResultOK, LinkID: linkID}
	if l, err := m.opts.Links.Get(linkID); err == nil {
		rec.LinkName = l.Name
	}
	if err := m.opts.Links.ResetDevices(linkID); err != nil {
		return err
	}
	for _, req := range m.byID {
		if req.LinkID == linkID {
			m.drop(req)
		}
	}
	m.log.Info("devices reset", "link", linkID)
	m.record(rec, access.Client{})
	return nil
}

// HandleDecisionEvent applies a guest_access_device_decision event from
// HA: {"request_id":"...","approve":true}. approve must be a JSON bool; a
// payload without one is malformed and ignored, never read as a denial.
// Unknown and expired requests are ignored. Every case logs a line.
func (m *Manager) HandleDecisionEvent(data json.RawMessage) {
	var ev struct {
		RequestID string `json:"request_id"`
		Approve   *bool  `json:"approve"`
	}
	if err := json.Unmarshal(data, &ev); err != nil || ev.RequestID == "" || ev.Approve == nil {
		m.log.Warn("malformed device decision event", "err", err, "bytes", len(data))
		return
	}
	err := m.decide(ev.RequestID, *ev.Approve, model.ApprovedByHA, "")
	switch {
	case errors.Is(err, ErrUnknownRequest):
		m.log.Info("device decision for an unknown or expired request", "request", logID(ev.RequestID))
	case err != nil:
		m.log.Error("device decision from HA", "request", logID(ev.RequestID), "err", err)
	}
}

// Run subscribes to decision events with subscribe, which has the shape
// of ha.Client.SubscribeEvents; pass that method. The subscription lasts
// until ctx is done.
func (m *Manager) Run(ctx context.Context, subscribe func(context.Context, string, func(ha.Event)) error) error {
	return subscribe(ctx, EventDeviceDecision, func(ev ha.Event) { m.HandleDecisionEvent(ev.Data) })
}

// decide applies a decision by by. With onlyLink set, the request must
// belong to that link (self-service). An approval that fails in the store
// leaves the request pending, so it can be tried again.
func (m *Manager) decide(requestID string, approve bool, by, onlyLink string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.sweep(now)
	req := m.byID[requestID]
	if req == nil || req.state != statePending || (onlyLink != "" && req.LinkID != onlyLink) {
		return ErrUnknownRequest
	}
	if !approve {
		req.state, req.ExpiresAt = stateDenied, now.Add(m.opts.PendingTTL)
		m.log.Info("device denied", "link", req.LinkID, "request", req.ID, "by", by)
		m.recordDecision("device.denied", audit.ResultDenied, req, by, "")
		return nil
	}
	d, err := m.opts.Links.AddDevice(req.LinkID, model.LinkDevice{
		Label: req.Label, Device: req.Device, ApprovedAt: now, ApprovedBy: by,
	}, m.opts.MaxDevices)
	if err != nil {
		return err
	}
	req.state, req.deviceID, req.ExpiresAt = stateApproved, d.ID, now.Add(m.opts.PendingTTL)
	m.log.Info("device approved", "link", req.LinkID, "request", req.ID, "device", d.ID, "by", by)
	m.recordDecision("device.approved", audit.ResultAllowed, req, by, d.ID)
	return nil
}

// record sends r to the audit log with the client c, when there is one.
func (m *Manager) record(r audit.Record, c access.Client) {
	r.Time = m.now()
	if c.IP.IsValid() {
		r.ClientIP = c.IP.String()
	}
	r.ViaProxy, r.Unresolved = c.ViaProxy, c.Unresolved
	m.opts.Audit.Log(r)
}

// recordDecision records a decision on req. The client fields are the
// requester's, from when it asked.
func (m *Manager) recordDecision(event, result string, req *request, by, deviceID string) {
	m.opts.Audit.Log(audit.Record{
		Time: m.now(), Event: event, Result: result, By: by,
		LinkID: req.LinkID, LinkName: req.linkName, PasswordLabel: req.Label, DeviceLabel: req.Label, DeviceID: deviceID,
		Device: req.Device, ClientIP: req.IP, RequestID: req.ID, Code: req.Code,
	})
}

// handoff gives the browser of an approved request its device cookie and
// forgets the request. It fails when the device is no longer on the link.
// It requires m.mu.
func (m *Manager) handoff(w http.ResponseWriter, link model.Link, c access.Client, req *request) (string, bool) {
	m.drop(req)
	clearCookie(w, RequestCookieName(link.ID), c)
	if _, ok := link.Device(req.deviceID); !ok {
		return "", false
	}
	m.setDeviceCookie(w, link, c, req.deviceID)
	return req.deviceID, true
}

// deviceFromCookie returns the device id of a valid device cookie for a
// device still on link. It notes the device as seen, and renews the
// cookie when the link's end moved since it was signed.
func (m *Manager) deviceFromCookie(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, now time.Time) (string, bool) {
	for _, ck := range r.CookiesNamed(DeviceCookieName(link.ID)) {
		id, exp, ok := m.opts.Signer.VerifyDevice(link.ID, ck.Value, now)
		if !ok {
			continue
		}
		if _, ok := link.Device(id); !ok {
			continue // revoked
		}
		if err := m.opts.Links.TouchDevice(link.ID, id, now, m.opts.TouchThrottle); err != nil {
			m.log.Debug("touch device", "link", link.ID, "device", id, "err", err)
		}
		if exp.Unix() != link.ValidUntil.Unix() {
			m.setDeviceCookie(w, link, c, id)
		}
		return id, true
	}
	return "", false
}

// requestFromCookie returns this browser's request for linkID, if any. It
// requires m.mu.
func (m *Manager) requestFromCookie(r *http.Request, linkID string) *request {
	for _, ck := range r.CookiesNamed(RequestCookieName(linkID)) {
		if req := m.bySecret[ck.Value]; req != nil && req.LinkID == linkID {
			return req
		}
	}
	return nil
}

// pendingOf returns the undecided requests of linkID (all links when
// empty), oldest first. It requires m.mu.
func (m *Manager) pendingOf(linkID string) []*request {
	var out []*request
	for _, req := range m.byID {
		if req.state == statePending && (linkID == "" || req.LinkID == linkID) {
			out = append(out, req)
		}
	}
	slices.SortFunc(out, func(a, b *request) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// newCode returns a code no other request of linkID has. With 31^4 codes
// and at most a few requests per link, the loop ends at once. It requires
// m.mu.
func (m *Manager) newCode(linkID string) string {
	for {
		code := randomCode()
		taken := false
		for _, req := range m.byID {
			if req.LinkID == linkID && req.Code == code {
				taken = true
				break
			}
		}
		if !taken {
			return code
		}
	}
}

// sweep drops expired requests and events older than an hour. Memory then
// stays bounded: each link holds at most EventsPerHour requests. It
// requires m.mu.
func (m *Manager) sweep(now time.Time) {
	for _, req := range m.byID {
		if !now.Before(req.ExpiresAt) {
			m.drop(req)
		}
	}
	for id, ev := range m.events {
		i := 0
		for i < len(ev) && !now.Before(ev[i].Add(time.Hour)) {
			i++
		}
		if i == len(ev) {
			delete(m.events, id)
		} else if i > 0 {
			m.events[id] = append([]time.Time(nil), ev[i:]...)
		}
	}
}

// drop forgets a request. It requires m.mu.
func (m *Manager) drop(req *request) {
	delete(m.byID, req.ID)
	delete(m.bySecret, req.secret)
}

func (m *Manager) setDeviceCookie(w http.ResponseWriter, link model.Link, c access.Client, deviceID string) {
	http.SetCookie(w, &http.Cookie{
		Name: DeviceCookieName(link.ID), Value: m.opts.Signer.SignDevice(link.ID, deviceID, link.ValidUntil),
		Path: access.GuestCookiePath, Expires: link.ValidUntil,
		HttpOnly: true, Secure: c.HTTPS, SameSite: http.SameSiteStrictMode,
	})
}

func clearCookie(w http.ResponseWriter, name string, c access.Client) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Path: access.GuestCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: c.HTTPS, SameSite: http.SameSiteStrictMode,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// retryAfter turns a wait into whole seconds for Retry-After, at least 1.
func retryAfter(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}

// randomHex returns 128 random bits as 32 hex characters. Hex keeps the
// blueprint's action ids easy to split.
func randomHex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return hex.EncodeToString(b)
}

// randomCode returns CodeLen characters of CodeAlphabet. It rejects bytes
// past the last whole multiple of the alphabet, so no character is more
// likely than another.
func randomCode() string {
	const n = len(CodeAlphabet)
	const limit = 256 - 256%n
	out := make([]byte, 0, CodeLen)
	var b [16]byte
	for len(out) < CodeLen {
		_, _ = rand.Read(b[:])
		for _, x := range b {
			if int(x) < limit && len(out) < CodeLen {
				out = append(out, CodeAlphabet[int(x)%n])
			}
		}
	}
	return string(out)
}

// logID caps an id from outside before it goes in a log line.
func logID(s string) string {
	if len(s) > 64 {
		return fmt.Sprintf("%.64q...", s)
	}
	return strconv.Quote(s)
}
