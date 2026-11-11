// Package web is the HTTP side of the app. It serves two ports with two
// separate muxes:
//
//   - Admin (AdminHandler): the JSON API and the admin UI. Only the
//     Supervisor ingress proxy may reach it.
//   - Guest (GuestHandler): the guest page and the guest API under
//     /g/{slug}. It has no admin routes at all.
//
// Every check that decides what a guest may do lives in authz and access.
// This package wires them in a fixed order and turns their answers into
// HTTP responses that never echo internal details.
package web

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/approval"
	"github.com/igrybkov/ha-guest-access/internal/audit"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/ma"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// HA is the part of the HA client the server uses. ha.Client satisfies it.
type HA interface {
	CallService(ctx context.Context, domain, service string, data map[string]any, target *ha.Target, returnResponse bool) (json.RawMessage, error)
	FireEvent(ctx context.Context, eventType string, data map[string]any) error
}

// Inventory is the registry snapshot and state source. *inventory.Cache
// satisfies it.
type Inventory interface {
	Inventory(ctx context.Context) (model.Inventory, error)
	States(ctx context.Context, ids map[string]bool) (map[string]model.State, error)
	Watch(ctx context.Context, ids map[string]bool, fn func(model.State)) error
}

// DeviceGate decides whether this request's device may use the link.
type DeviceGate interface {
	// Admit runs after the password session and before any grant check.
	// On ok=false it has already written the response.
	Admit(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string) (deviceID string, ok bool)
	// StillValid is Admit for an open event stream: it writes nothing.
	// deviceID is what Admit returned when the stream opened.
	StillValid(link model.Link, deviceID string) bool
}

// Devices is device approval as the server uses it. *approval.Manager
// satisfies it.
type Devices interface {
	DeviceGate
	// Guest routes under /g/{slug}/device/. They run after the password
	// session but not the gate.
	RequestAccess(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string)
	Status(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string)
	PendingForDevice(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string)
	DecideByDevice(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string)
	// Admin calls.
	Pending(linkID string) []approval.Request
	Decide(requestID string, approve bool) error
	RemoveDevice(linkID, deviceID string) error
	ResetDevices(linkID string) error
}

var _ Devices = (*approval.Manager)(nil)

// Deps are the collaborators of the server.
type Deps struct {
	// Options are the app options: settings and every tunable value. They
	// never change while the app runs; Supervisor restarts it on a save.
	Options   config.Options
	Templates *config.TemplatesStore
	Links     *links.Store
	Inventory Inventory
	HA        HA
	Signer    *access.Signer

	// Devices runs device approval. nil = an approval.Manager built from
	// these deps; it then gets no decision events from HA.
	Devices Devices

	// NewMA builds the Music Assistant client. nil = ma.NewWithTimeout.
	// It may return a nil Client: MA is off.
	NewMA func(url, token string, timeout time.Duration) (ma.Client, error)

	// Audit records security events. nil records nothing.
	Audit *audit.Logger

	// DevAdminAnyPeer lets any peer reach the admin port (make run-dev).
	DevAdminAnyPeer bool

	// TLSStatus reports the guest port's HTTPS state for the settings
	// page. nil = HTTPS is off.
	TLSStatus func() TLSStatus

	// Now is the clock. nil = time.Now.
	Now func() time.Time

	Version string
	Logger  *slog.Logger
}

// Timeouts for calls into HA, and fallbacks for zero options (tests).
// The real values come from the app options.
const (
	defaultHACall       = 10 * time.Second
	defaultHAEvent      = 5 * time.Second
	defaultStreamCheck  = 5 * time.Second
	defaultHeartbeat    = 25 * time.Second
	defaultStreamRetry  = 30 * time.Second
	defaultGuestBody    = 16 << 10
	defaultAdminBody    = 1 << 20
	defaultNearTTL      = 15 * time.Minute
	defaultMaxAccuracyM = 1000
)

// Server serves both ports. Safe for concurrent use.
type Server struct {
	deps    Deps
	log     *slog.Logger
	devices Devices
	newMA   func(url, token string, timeout time.Duration) (ma.Client, error)

	lim     *limitSet
	trusted []netip.Prefix // trusted proxies
	nets    []netip.Prefix // local networks

	streams streamCounter
	audit   *audit.Logger
	// auditThrottle holds back records a client can cause in bulk.
	auditThrottle *audit.Throttle
	// denialCap caps bulk denial records per minute across all clients.
	denialCap *audit.Cap
	// sliderThrottle spaces the allowed records of slider drags.
	sliderThrottle *audit.Throttle
	diag           *diagRing
	assets         *assetSet
	// badLinks holds the problems last logged for each link that fails
	// policy.ServableLink, so each is logged once.
	badMu    sync.Mutex
	badLinks map[string]string
	// notFoundPage is the rendered not-found page. notFoundRedirect is
	// where browsers go instead, or "" to show the page. Both come from
	// the options only, so every unknown path gets the same answer.
	notFoundPage     []byte
	notFoundRedirect string
	// maRefused is true while Music Assistant refuses the token: set by
	// a party call that gets ma.ErrUnauthorized, cleared by one that
	// works. The admin API shows it as a warning.
	maRefused atomic.Bool
}

// New returns a Server.
func New(d Deps) *Server {
	s := &Server{
		deps:     d,
		log:      d.Logger,
		devices:  d.Devices,
		newMA:    d.NewMA,
		streams:  newStreamCounter(),
		badLinks: map[string]string{},
		audit:    d.Audit,

		auditThrottle:  audit.NewThrottle(auditThrottlePeriod),
		sliderThrottle: audit.NewThrottle(sliderAuditPeriod),
		denialCap:      audit.NewCap(d.Options.AuditMaxDenialsPerMin, time.Minute),
		diag:           newDiagRing(max(1, d.Options.DiagRequests)),
		assets:         loadAssets(),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.newMA == nil {
		s.newMA = ma.NewWithTimeout
	}
	if s.deps.Now == nil {
		s.deps.Now = time.Now
	}
	o := &s.deps.Options
	if o.MATimeout <= 0 {
		o.MATimeout = ma.Timeout
	}
	if o.GuestBodyBytes <= 0 {
		o.GuestBodyBytes = defaultGuestBody
	}
	if o.AdminBodyBytes <= 0 {
		o.AdminBodyBytes = defaultAdminBody
	}
	if o.NearTTL <= 0 {
		o.NearTTL = defaultNearTTL
	}
	if o.MaxAccuracyM <= 0 {
		o.MaxAccuracyM = defaultMaxAccuracyM
	}
	o.Settings.RateLimits = o.Settings.RateLimits.WithDefaults()
	// Options are checked at load; a bad entry here only drops it, which
	// fails closed (no trusted proxy, nothing local).
	s.trusted = parsePrefixes(o.Settings.TrustedProxies, s.log, "trusted proxy")
	s.nets = parsePrefixes(o.Settings.LocalAccess.Networks, s.log, "local network")
	s.lim = newLimitSet(*o)
	s.notFoundPage = s.assets.renderNotFound(
		cmp.Or(o.NotFound.Title, config.DefaultNotFoundTitle),
		cmp.Or(o.NotFound.Message, config.DefaultNotFoundMessage))
	if r := o.NotFound.Redirect; r != "" && config.NotFoundRedirectOK(r) {
		s.notFoundRedirect = r
	}
	for _, f := range []struct {
		v   *time.Duration
		def time.Duration
	}{
		{&o.HACallTimeout, defaultHACall},
		{&o.HAEventTimeout, defaultHAEvent},
		{&o.StreamCheck, defaultStreamCheck},
		{&o.StreamHeartbeat, defaultHeartbeat},
		{&o.StreamRetry, defaultStreamRetry},
	} {
		if *f.v <= 0 {
			*f.v = f.def
		}
	}
	if s.devices == nil {
		a := s.deps.Options.Approval
		s.devices = approval.New(approval.Options{
			Links: d.Links, FireEvent: d.HA.FireEvent, Signer: d.Signer, Now: s.deps.Now, Logger: s.log,
			PendingTTL: a.PendingTTL, MaxPending: a.MaxPending, MaxDevices: a.MaxDevices,
			EventsPerHour: a.EventsPerHour, TouchThrottle: a.TouchThrottle,
			MaxBodyBytes: s.deps.Options.GuestBodyBytes, EventTimeout: s.deps.Options.HAEventTimeout,
			Audit: d.Audit, IPv6Prefix: s.deps.Options.Settings.RateLimits.IPv6Prefix,
		})
	}
	return s
}

func (s *Server) now() time.Time { return s.deps.Now() }

// settings returns the settings from the app options.
func (s *Server) settings() model.Settings { return s.deps.Options.Settings }

func parsePrefixes(list []string, log *slog.Logger, what string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, v := range list {
		p, err := model.ParsePrefix(v)
		if err != nil {
			log.Warn("ignoring a bad "+what, "value", v)
			continue
		}
		out = append(out, p)
	}
	return out
}

// recoverPanics turns a panic in a handler into a 500 so one bad request
// does not take the process down.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("web: panic", "route", routeLabel(r.URL.Path), "panic", v)
				writeError(w, http.StatusInternalServerError, msgInternal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
