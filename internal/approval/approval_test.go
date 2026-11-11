package approval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

var manual = model.Approval{Manual: true}
var claim = model.Approval{SelfService: true}
var both = model.Approval{Manual: true, SelfService: true}

func TestNewNeedsDeps(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New with no deps did not panic")
		}
	}()
	New(Options{})
}

func TestOptionDefaults(t *testing.T) {
	e := newEnv(t, Options{})
	o := e.m.opts
	if o.PendingTTL != DefaultPendingTTL || o.MaxPending != DefaultMaxPending || o.MaxDevices != DefaultMaxDevices ||
		o.EventsPerHour != DefaultEventsPerHour || o.TouchThrottle != DefaultTouchThrottle || o.MaxBodyBytes != DefaultMaxBodyBytes {
		t.Errorf("defaults: %+v", o)
	}
}

// With approval off, every handler lets the guest in and Admit writes
// nothing.
func TestApprovalOff(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(model.Approval{})
	id, ok, res := e.admit(l.ID, newBrowser(iPhoneUA))
	if !ok || id != "" || res.body != nil || len(res.cookies) != 0 {
		t.Fatalf("Admit = %q, %v, %+v", id, ok, res)
	}
	want(t, "request", e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}"), 200, "approved")
	want(t, "status", e.call(e.m.Status, l.ID, newBrowser(iPhoneUA), ""), 200, "approved")
	if len(e.fired()) != 0 || len(e.m.Pending("")) != 0 {
		t.Error("approval off made a request")
	}
}

// The whole manual flow, from the first visit to an admitted device.
func TestManualFlow(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(manual)
	b := newBrowser(iPhoneUA)
	b.https = true

	_, ok, res := e.admit(l.ID, b)
	if ok {
		t.Fatal("unknown device admitted")
	}
	want(t, "first admit", res, 403, "approval_required")

	res = e.call(e.m.RequestAccess, l.ID, b, "{}")
	want(t, "request", res, 200, "pending")
	code := res.codeStr
	if len(code) != CodeLen || strings.Trim(code, CodeAlphabet) != "" {
		t.Fatalf("code %q", code)
	}
	rc := b.cookie(RequestCookieName(l.ID))
	if rc == nil || !rc.HttpOnly || !rc.Secure || rc.SameSite != http.SameSiteStrictMode || rc.Path != access.GuestCookiePath || rc.MaxAge != 600 {
		t.Fatalf("request cookie %+v", rc)
	}

	ev := e.fired()
	if len(ev) != 1 || ev[0].Type != EventDeviceRequest {
		t.Fatalf("events %+v", ev)
	}
	reqID := e.requestID(l.ID)
	wantData := map[string]any{"link_id": l.ID, "link_name": "Anna", "request_id": reqID, "code": code,
		"label": "Anna", "device": "iPhone, Safari", "ip": "203.0.113.7"}
	if fmt.Sprint(ev[0].Data) != fmt.Sprint(wantData) {
		t.Errorf("event data %v, want %v", ev[0].Data, wantData)
	}
	if rc.Value == reqID {
		t.Error("the request cookie holds the request id")
	}

	_, ok, res = e.admit(l.ID, b)
	if ok || res.code != 403 || res.status != "pending" || res.codeStr != code {
		t.Fatalf("waiting admit: %v %+v", ok, res)
	}
	res = e.call(e.m.Status, l.ID, b, "")
	if res.code != 200 || res.status != "pending" || res.codeStr != code {
		t.Fatalf("waiting status: %+v", res)
	}

	if p := e.m.Pending(l.ID); len(p) != 1 || p[0].Code != code || p[0].IP != "203.0.113.7" || p[0].Device != "iPhone, Safari" {
		t.Fatalf("admin pending %+v", p)
	}
	if err := e.m.Decide(reqID, true); err != nil {
		t.Fatal(err)
	}
	if len(e.m.Pending(l.ID)) != 0 {
		t.Error("decided request still pending")
	}

	want(t, "status after approval", e.call(e.m.Status, l.ID, b, ""), 200, "approved")
	dc := b.cookie(DeviceCookieName(l.ID))
	if dc == nil || !dc.HttpOnly || !dc.Secure || dc.SameSite != http.SameSiteStrictMode || dc.Path != access.GuestCookiePath ||
		!dc.Expires.Equal(l.ValidUntil) {
		t.Fatalf("device cookie %+v", dc)
	}
	if b.cookie(RequestCookieName(l.ID)) != nil {
		t.Error("request cookie kept after pickup")
	}
	id, ok, _ := e.admit(l.ID, b)
	if !ok || id == "" {
		t.Fatal("approved device not admitted")
	}
	d, found := e.link(l.ID).Device(id)
	if !found || d.ApprovedBy != model.ApprovedByAdmin || d.Label != "Anna" || d.Device != "iPhone, Safari" || !d.ApprovedAt.Equal(t0) {
		t.Errorf("stored device %+v", d)
	}
	// Admitting needs no extra events.
	if len(e.fired()) != 1 {
		t.Errorf("%d events", len(e.fired()))
	}
}

// Without HTTPS the cookies are not Secure, or the browser would drop
// them on a plain-HTTP LAN visit.
func TestCookiesNotSecureOverHTTP(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(claim)
	b := newBrowser(iPhoneUA)
	want(t, "claim", e.call(e.m.RequestAccess, l.ID, b, "{}"), 200, "approved")
	if dc := b.cookie(DeviceCookieName(l.ID)); dc == nil || dc.Secure || !dc.HttpOnly {
		t.Fatalf("device cookie %+v", dc)
	}
}

// An approved request is handed over by Admit too, so the guest needs no
// extra poll.
func TestAdmitPicksUpApproval(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(manual)
	b := newBrowser(iPhoneUA)
	e.call(e.m.RequestAccess, l.ID, b, "{}")
	if err := e.m.Decide(e.requestID(l.ID), true); err != nil {
		t.Fatal(err)
	}
	id, ok, _ := e.admit(l.ID, b)
	if !ok || id == "" || b.cookie(DeviceCookieName(l.ID)) == nil {
		t.Fatalf("Admit after approval = %q, %v", id, ok)
	}
}

// A double tap returns the same request and fires no second event.
func TestRequestIsIdempotentPerBrowser(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(manual)
	b := newBrowser(iPhoneUA)
	first := e.call(e.m.RequestAccess, l.ID, b, "{}")
	for range 5 {
		res := e.call(e.m.RequestAccess, l.ID, b, "{}")
		if res.status != "pending" || res.codeStr != first.codeStr {
			t.Fatalf("repeat = %+v, first code %q", res, first.codeStr)
		}
	}
	if len(e.fired()) != 1 || len(e.m.Pending(l.ID)) != 1 {
		t.Errorf("%d events, %d pending", len(e.fired()), len(e.m.Pending(l.ID)))
	}
}

func TestPendingLimitAndTTL(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(manual)
	codes := map[string]bool{}
	var browsers []*browser
	for i := range DefaultMaxPending {
		b := newBrowser(iPhoneUA)
		res := e.call(e.m.RequestAccess, l.ID, b, "{}")
		want(t, fmt.Sprint("request ", i), res, 200, "pending")
		codes[res.codeStr] = true
		browsers = append(browsers, b)
		e.clock.Add(time.Minute)
	}
	if len(codes) != DefaultMaxPending {
		t.Errorf("codes not unique: %v", codes)
	}
	res := e.call(e.m.RequestAccess, l.ID, newBrowser(androidUA), "{}")
	want(t, "over the pending cap", res, 429, "try_later")
	// The oldest request ends 10 min after t0; the clock is at t0+3m.
	if got := res.header.Get("Retry-After"); got != "420" {
		t.Errorf("Retry-After = %q, want 420", got)
	}
	// Another link has its own cap.
	other := e.newLink(manual)
	want(t, "other link", e.call(e.m.RequestAccess, other.ID, newBrowser(androidUA), "{}"), 200, "pending")

	e.clock.Add(DefaultPendingTTL - 3*time.Minute) // t0+10m: the first request ends
	want(t, "expired request", e.call(e.m.Status, l.ID, browsers[0], ""), 200, "approval_required")
	_, _, res = e.admit(l.ID, browsers[0])
	want(t, "admit after expiry", res, 403, "approval_required")
	want(t, "status of a live one", e.call(e.m.Status, l.ID, browsers[1], ""), 200, "pending")
	want(t, "room again", e.call(e.m.RequestAccess, l.ID, newBrowser(androidUA), "{}"), 200, "pending")
	if len(e.m.Pending(l.ID)) != DefaultMaxPending {
		t.Errorf("%d pending", len(e.m.Pending(l.ID)))
	}
}

// Approving an expired request does nothing.
func TestDecideExpired(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(manual)
	e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}")
	id := e.requestID(l.ID)
	e.clock.Add(DefaultPendingTTL)
	if err := e.m.Decide(id, true); !errors.Is(err, ErrUnknownRequest) {
		t.Errorf("Decide expired = %v", err)
	}
	if len(e.link(l.ID).Devices) != 0 {
		t.Error("expired request approved")
	}
}

func TestEventRateCap(t *testing.T) {
	e := newEnv(t, Options{MaxPending: 100})
	l := e.newLink(manual)
	for i := range DefaultEventsPerHour {
		want(t, fmt.Sprint("request ", i), e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}"), 200, "pending")
		e.clock.Add(time.Minute)
	}
	res := e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}")
	want(t, "over the event cap", res, 429, "try_later")
	if got := res.header.Get("Retry-After"); got != "3300" {
		t.Errorf("Retry-After = %q, want 3300", got)
	}
	if n := len(e.fired()); n != DefaultEventsPerHour {
		t.Errorf("%d events fired", n)
	}
	e.clock.Add(55 * time.Minute) // the first event leaves the hour
	want(t, "after an hour", e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}"), 200, "pending")
}

// A failed FireEvent still leaves the request for the admin, and still
// counts toward the cap.
func TestFireFailureKeepsRequest(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(manual)
	e.fail.Store(true)
	want(t, "request", e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}"), 200, "pending")
	if len(e.m.Pending(l.ID)) != 1 || !strings.Contains(e.logs.String(), "HA is down") {
		t.Error("request lost or failure not logged")
	}
}

func TestCodesUniquePerLink(t *testing.T) {
	e := newEnv(t, Options{MaxPending: 2000, EventsPerHour: 2000})
	l := e.newLink(manual)
	seen := map[string]bool{}
	for range 1500 {
		res := e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}")
		if seen[res.codeStr] {
			t.Fatalf("code %q given twice", res.codeStr)
		}
		seen[res.codeStr] = true
	}
}

func TestRandomCodeAlphabet(t *testing.T) {
	counts := map[rune]int{}
	for range 20000 {
		c := randomCode()
		if len(c) != CodeLen {
			t.Fatalf("code %q", c)
		}
		for _, r := range c {
			counts[r]++
		}
	}
	if strings.ContainsAny(CodeAlphabet, "0O1IL") {
		t.Fatal("alphabet has an ambiguous character")
	}
	if len(counts) != len(CodeAlphabet) {
		t.Errorf("%d of %d characters seen", len(counts), len(CodeAlphabet))
	}
	// 80000 draws over 31 characters: about 2581 each. Bias from a
	// plain modulo would push a few well past this band.
	for r, n := range counts {
		if n < 2200 || n > 2950 {
			t.Errorf("%q drawn %d times", r, n)
		}
	}
}

func TestClaimMode(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(claim)
	first := newBrowser(iPhoneUA)
	_, _, res := e.admit(l.ID, first)
	want(t, "first admit", res, 403, "approval_required")
	want(t, "claim", e.call(e.m.RequestAccess, l.ID, first, "{}"), 200, "approved")
	id, ok, _ := e.admit(l.ID, first)
	if !ok {
		t.Fatal("claimer not admitted")
	}
	if d, _ := e.link(l.ID).Device(id); d.ApprovedBy != model.ApprovedByClaim {
		t.Errorf("claimer %+v", d)
	}
	if len(e.fired()) != 0 {
		t.Error("a claim fired an event")
	}
	second := newBrowser(androidUA)
	want(t, "second device", e.call(e.m.RequestAccess, l.ID, second, "{}"), 200, "pending")
	if len(e.fired()) != 1 || len(e.link(l.ID).Devices) != 1 {
		t.Error("second device was not made to wait")
	}
}

// Manual plus self-service: the first device waits like any other.
func TestBothModesNoClaim(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(both)
	want(t, "first device", e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}"), 200, "pending")
}

// Of many first devices at once, exactly one claims the link.
func TestClaimConcurrent(t *testing.T) {
	e := newEnv(t, Options{MaxPending: 100, EventsPerHour: 100})
	l := e.newLink(claim)
	var mu sync.Mutex
	statuses := map[string]int{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}")
			mu.Lock()
			statuses[res.status]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if statuses["approved"] != 1 || statuses["pending"] != 19 || len(e.link(l.ID).Devices) != 1 {
		t.Fatalf("statuses %v, devices %d", statuses, len(e.link(l.ID).Devices))
	}
}

// claimed returns a link in mode a with one device, held by the returned
// browser.
func (e *env) claimed(a model.Approval) (model.Link, *browser, string) {
	e.t.Helper()
	l := e.newLink(a)
	b := newBrowser(iPhoneUA)
	if a.Claim() {
		e.call(e.m.RequestAccess, l.ID, b, "{}")
	} else {
		e.call(e.m.RequestAccess, l.ID, b, "{}")
		if err := e.m.Decide(e.requestID(l.ID), true); err != nil {
			e.t.Fatal(err)
		}
		e.call(e.m.Status, l.ID, b, "")
	}
	id, ok, _ := e.admit(l.ID, b)
	if !ok {
		e.t.Fatal("first device not admitted")
	}
	return l, b, id
}

func decideBody(id string, approve bool) string {
	return fmt.Sprintf(`{"request_id":%q,"approve":%v}`, id, approve)
}

func TestSelfService(t *testing.T) {
	for _, a := range []model.Approval{claim, both} {
		t.Run(fmt.Sprintf("%+v", a), func(t *testing.T) {
			e := newEnv(t, Options{})
			l, approver, approverID := e.claimed(a)
			guest := newBrowser(androidUA)
			want(t, "guest asks", e.call(e.m.RequestAccess, l.ID, guest, "{}"), 200, "pending")

			// Without a device, no list and no decision.
			want(t, "list without device", e.call(e.m.PendingForDevice, l.ID, newBrowser(iPhoneUA), ""), 403, "approval_required")
			want(t, "list by the waiting guest", e.call(e.m.PendingForDevice, l.ID, guest, ""), 403, "approval_required")
			reqID := e.requestID(l.ID)
			want(t, "guest approves itself", e.call(e.m.DecideByDevice, l.ID, guest, decideBody(reqID, true)), 403, "approval_required")

			res := e.call(e.m.PendingForDevice, l.ID, approver, "")
			list, _ := res.body["requests"].([]any)
			if res.code != 200 || len(list) != 1 {
				t.Fatalf("list: %+v", res)
			}
			item := list[0].(map[string]any)
			if item["request_id"] != reqID || item["device"] != "Android, Chrome" || item["ip"] != nil {
				t.Errorf("listed %v", item)
			}

			want(t, "bad body", e.call(e.m.DecideByDevice, l.ID, approver, `{"request_id":"x","approve":"yes"}`), 400, "bad_request")
			want(t, "no approve", e.call(e.m.DecideByDevice, l.ID, approver, fmt.Sprintf(`{"request_id":%q}`, reqID)), 400, "bad_request")
			want(t, "unknown field", e.call(e.m.DecideByDevice, l.ID, approver, `{"request_id":"x","approve":true,"by":"admin"}`), 400, "bad_request")
			want(t, "body over the cap", e.call(e.m.DecideByDevice, l.ID, approver, decideBody(reqID+strings.Repeat(" ", 2000), true)), 400, "bad_request")
			want(t, "unknown request", e.call(e.m.DecideByDevice, l.ID, approver, decideBody("ffff", true)), 404, "not_found")
			want(t, "approve", e.call(e.m.DecideByDevice, l.ID, approver, decideBody(reqID, true)), 200, "approved")
			want(t, "approve twice", e.call(e.m.DecideByDevice, l.ID, approver, decideBody(reqID, true)), 404, "not_found")

			want(t, "guest picks up", e.call(e.m.Status, l.ID, guest, ""), 200, "approved")
			id, ok, _ := e.admit(l.ID, guest)
			if !ok {
				t.Fatal("guest not admitted")
			}
			if d, _ := e.link(l.ID).Device(id); d.ApprovedBy != "device:"+approverID {
				t.Errorf("approved by %q", d.ApprovedBy)
			}
		})
	}
}

// On a manual-only link, a device can't approve others.
func TestSelfServiceOffForManual(t *testing.T) {
	e := newEnv(t, Options{})
	l, approver, _ := e.claimed(manual)
	e.call(e.m.RequestAccess, l.ID, newBrowser(androidUA), "{}")
	want(t, "list", e.call(e.m.PendingForDevice, l.ID, approver, ""), 403, "approval_required")
	want(t, "decide", e.call(e.m.DecideByDevice, l.ID, approver, decideBody(e.requestID(l.ID), true)), 403, "approval_required")
}

// A device of link A can't approve for link B, with its cookie on B or
// with B's request id on A.
func TestSelfServiceCrossLink(t *testing.T) {
	e := newEnv(t, Options{})
	a, approverA, _ := e.claimed(claim)
	b, _, _ := e.claimed(claim)
	e.call(e.m.RequestAccess, b.ID, newBrowser(androidUA), "{}")
	reqB := e.requestID(b.ID)

	want(t, "A's cookie on B", e.call(e.m.DecideByDevice, b.ID, approverA, decideBody(reqB, true)), 403, "approval_required")
	want(t, "A's cookie lists B", e.call(e.m.PendingForDevice, b.ID, approverA, ""), 403, "approval_required")
	want(t, "B's request on A", e.call(e.m.DecideByDevice, a.ID, approverA, decideBody(reqB, true)), 404, "not_found")
	res := e.call(e.m.PendingForDevice, a.ID, approverA, "")
	if list, _ := res.body["requests"].([]any); len(list) != 0 {
		t.Errorf("A sees B's requests: %v", list)
	}
	if len(e.link(b.ID).Devices) != 1 || len(e.m.Pending(b.ID)) != 1 {
		t.Error("cross-link decision took effect")
	}
}

func TestDenyThenAskAgain(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(manual)
	b := newBrowser(iPhoneUA)
	e.call(e.m.RequestAccess, l.ID, b, "{}")
	if err := e.m.Decide(e.requestID(l.ID), false); err != nil {
		t.Fatal(err)
	}
	_, _, res := e.admit(l.ID, b)
	want(t, "admit after deny", res, 403, "approval_required")
	want(t, "status after deny", e.call(e.m.Status, l.ID, b, ""), 200, "denied")
	if b.cookie(RequestCookieName(l.ID)) != nil {
		t.Error("request cookie kept after deny")
	}
	want(t, "status again", e.call(e.m.Status, l.ID, b, ""), 200, "approval_required")
	want(t, "ask again", e.call(e.m.RequestAccess, l.ID, b, "{}"), 200, "pending")
	if len(e.link(l.ID).Devices) != 0 {
		t.Error("denied device stored")
	}
}

// A deny seen only by RequestAccess (no Status poll) lets the guest ask
// again at once.
func TestRequestAfterDenyWithoutPoll(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(manual)
	b := newBrowser(iPhoneUA)
	e.call(e.m.RequestAccess, l.ID, b, "{}")
	_ = e.m.Decide(e.requestID(l.ID), false)
	want(t, "ask again", e.call(e.m.RequestAccess, l.ID, b, "{}"), 200, "pending")
	if len(e.fired()) != 2 {
		t.Errorf("%d events", len(e.fired()))
	}
}

func TestRevokeTakesEffectAtOnce(t *testing.T) {
	e := newEnv(t, Options{})
	l, b, id := e.claimed(both)
	other := newBrowser(androidUA)
	e.call(e.m.RequestAccess, l.ID, other, "{}")
	_ = e.m.Decide(e.requestID(l.ID), true)
	e.call(e.m.Status, l.ID, other, "")
	if _, ok, _ := e.admit(l.ID, other); !ok {
		t.Fatal("second device not admitted")
	}

	if err := e.m.RemoveDevice(l.ID, id); err != nil {
		t.Fatal(err)
	}
	_, ok, res := e.admit(l.ID, b)
	if ok {
		t.Fatal("revoked device admitted")
	}
	want(t, "revoked admit", res, 403, "approval_required")
	want(t, "revoked lists", e.call(e.m.PendingForDevice, l.ID, b, ""), 403, "approval_required")
	if _, ok, _ := e.admit(l.ID, other); !ok {
		t.Error("revoke hit another device")
	}
	if err := e.m.RemoveDevice(l.ID, id); !errors.Is(err, links.ErrNotFound) {
		t.Errorf("revoke twice: %v", err)
	}
	if len(e.fired()) != 2 {
		t.Error("revoke fired an event")
	}
}

func TestResetDevices(t *testing.T) {
	e := newEnv(t, Options{})
	l, b, _ := e.claimed(claim)
	waiting := newBrowser(androidUA)
	e.call(e.m.RequestAccess, l.ID, waiting, "{}")
	events := len(e.fired())

	if err := e.m.ResetDevices(l.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.admit(l.ID, b); ok {
		t.Fatal("device admitted after reset")
	}
	if len(e.m.Pending(l.ID)) != 0 {
		t.Error("pending requests kept after reset")
	}
	want(t, "waiting guest after reset", e.call(e.m.Status, l.ID, waiting, ""), 200, "approval_required")
	// Claim mode: the next device claims the link again.
	want(t, "claim again", e.call(e.m.RequestAccess, l.ID, waiting, "{}"), 200, "approved")
	if _, ok, _ := e.admit(l.ID, waiting); !ok {
		t.Error("new claimer not admitted")
	}
	if len(e.fired()) != events {
		t.Error("reset fired an event")
	}
	if err := e.m.ResetDevices("nope"); !errors.Is(err, links.ErrNotFound) {
		t.Errorf("reset unknown link: %v", err)
	}
}

// An approval that is revoked before the browser picks it up gives no
// cookie.
func TestRevokedBeforePickup(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(manual)
	b := newBrowser(iPhoneUA)
	e.call(e.m.RequestAccess, l.ID, b, "{}")
	_ = e.m.Decide(e.requestID(l.ID), true)
	if err := e.links.ResetDevices(l.ID); err != nil { // straight in the store, as a stale pickup would see it
		t.Fatal(err)
	}
	want(t, "status", e.call(e.m.Status, l.ID, b, ""), 200, "approval_required")
	if b.cookie(DeviceCookieName(l.ID)) != nil {
		t.Error("cookie for a revoked device")
	}
}

func TestTooManyDevices(t *testing.T) {
	e := newEnv(t, Options{MaxDevices: 1})
	l, _, _ := e.claimed(both)
	e.call(e.m.RequestAccess, l.ID, newBrowser(androidUA), "{}")
	id := e.requestID(l.ID)
	if err := e.m.Decide(id, true); !errors.Is(err, links.ErrTooManyDevices) {
		t.Fatalf("Decide over the cap = %v", err)
	}
	if len(e.m.Pending(l.ID)) != 1 {
		t.Error("request dropped; it should wait for a free slot")
	}
}

// Forged, expired and wrong-purpose cookies never pass Admit.
func TestForgedCookies(t *testing.T) {
	e := newEnv(t, Options{})
	l, _, id := e.claimed(both)
	other := e.newLink(both)
	exp := l.ValidUntil
	for name, tok := range map[string]string{
		"garbage":           "not-a-token",
		"empty":             "",
		"other link":        e.signer.SignDevice(other.ID, id, exp),
		"expired":           e.signer.SignDevice(l.ID, id, t0.Add(-time.Second)),
		"session as device": e.signer.SignSession(l.ID, id, exp),
		"location":          e.signer.Sign(l.ID, exp),
		"unknown device":    e.signer.SignDevice(l.ID, "0000000000000000", exp),
		"other key":         access.NewSigner(bytes.Repeat([]byte{8}, access.MinKeyLen)).SignDevice(l.ID, id, exp),
	} {
		b := newBrowser(iPhoneUA)
		b.set(DeviceCookieName(l.ID), tok)
		if _, ok, res := e.admit(l.ID, b); ok || res.code != 403 {
			t.Errorf("%s: admitted (%+v)", name, res)
		}
	}
	// The real token works, so the cases above fail for their reason.
	b := newBrowser(iPhoneUA)
	b.set(DeviceCookieName(l.ID), e.signer.SignDevice(l.ID, id, exp))
	if _, ok, _ := e.admit(l.ID, b); !ok {
		t.Fatal("a good token fails")
	}
}

// A device token can't stand in for a session or a location token.
func TestDeviceTokenIsNotSession(t *testing.T) {
	e := newEnv(t, Options{})
	l, b, id := e.claimed(claim)
	tok := b.cookie(DeviceCookieName(l.ID)).Value
	if _, ok := e.signer.VerifySession(l.ID, tok, t0); ok {
		t.Error("device token passes as a session")
	}
	if e.signer.Verify(l.ID, tok, t0) {
		t.Error("device token passes as a location")
	}
	if got, _, ok := e.signer.VerifyDevice(l.ID, tok, t0); !ok || got != id {
		t.Error("device token does not verify as one")
	}
}

// When the admin extends the link, Admit renews the cookie to the new end,
// so the device does not lose access at the old one.
func TestCookieRenewedWhenLinkExtended(t *testing.T) {
	e := newEnv(t, Options{})
	l, b, _ := e.claimed(claim)
	_, _, res := e.admit(l.ID, b)
	if len(res.cookies) != 0 {
		t.Fatal("cookie renewed with no change")
	}
	edit := e.link(l.ID)
	edit.ValidUntil = l.ValidUntil.Add(72 * time.Hour)
	if _, err := e.links.Update(l.ID, edit); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.admit(l.ID, b); !ok {
		t.Fatal("not admitted after extension")
	}
	dc := b.cookie(DeviceCookieName(l.ID))
	if !dc.Expires.Equal(edit.ValidUntil) {
		t.Fatalf("cookie expires %v, want %v", dc.Expires, edit.ValidUntil)
	}
	if _, exp, ok := e.signer.VerifyDevice(l.ID, dc.Value, t0); !ok || !exp.Equal(edit.ValidUntil) {
		t.Errorf("renewed token exp %v, %v", exp, ok)
	}
}

// Admit notes the device as seen, throttled by the option.
func TestAdmitTouchesDevice(t *testing.T) {
	e := newEnv(t, Options{TouchThrottle: time.Hour})
	l, b, id := e.claimed(claim)
	e.clock.Add(time.Minute)
	e.admit(l.ID, b)
	first, _ := e.link(l.ID).Device(id)
	e.clock.Add(time.Minute)
	e.admit(l.ID, b)
	second, _ := e.link(l.ID).Device(id)
	if first.LastSeenAt.IsZero() || !second.LastSeenAt.Equal(first.LastSeenAt) {
		t.Errorf("last seen %v then %v", first.LastSeenAt, second.LastSeenAt)
	}
}

// Many browsers at once: the caps hold and nothing races.
func TestConcurrentRequests(t *testing.T) {
	e := newEnv(t, Options{})
	l := e.newLink(manual)
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := newBrowser(iPhoneUA)
			e.call(e.m.RequestAccess, l.ID, b, "{}")
			e.call(e.m.Status, l.ID, b, "")
			e.admit(l.ID, b)
			for _, p := range e.m.Pending(l.ID) {
				if i%2 == 0 {
					_ = e.m.Decide(p.ID, i%4 == 0)
				} else {
					raw, _ := json.Marshal(map[string]any{"request_id": p.ID, "approve": true})
					e.m.HandleDecisionEvent(raw)
				}
			}
			e.call(e.m.Status, l.ID, b, "")
		}()
	}
	wg.Wait()
	if n := len(e.fired()); n > DefaultEventsPerHour {
		t.Errorf("%d events fired", n)
	}
	if n := len(e.m.Pending(l.ID)); n > DefaultMaxPending {
		t.Errorf("%d pending", n)
	}
	if n := len(e.link(l.ID).Devices); n > DefaultEventsPerHour {
		t.Errorf("%d devices from at most %d requests", n, DefaultEventsPerHour)
	}
}

// Admin and HA deciding the same request at once add one device.
func TestConcurrentDecisionsOneDevice(t *testing.T) {
	for range 20 {
		e := newEnv(t, Options{})
		l := e.newLink(manual)
		e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}")
		id := e.requestID(l.ID)
		raw, _ := json.Marshal(map[string]any{"request_id": id, "approve": true})
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if i%2 == 0 {
					_ = e.m.Decide(id, true)
				} else {
					e.m.HandleDecisionEvent(raw)
				}
			}()
		}
		wg.Wait()
		if n := len(e.link(l.ID).Devices); n != 1 {
			t.Fatalf("%d devices from one request", n)
		}
	}
}

func TestHADecisionEvents(t *testing.T) {
	e := newEnv(t, Options{})
	fake := &hatest.Fake{}
	ctx := t.Context()
	if err := e.m.Run(ctx, fake.SubscribeEvents); err != nil {
		t.Fatal(err)
	}
	if fake.Subscribers(EventDeviceDecision) != 1 {
		t.Fatal("not subscribed")
	}
	l := e.newLink(manual)
	approveB, denyB, keepB := newBrowser(iPhoneUA), newBrowser(androidUA), newBrowser(iPhoneUA)
	ids := map[*browser]string{}
	for _, b := range []*browser{approveB, denyB, keepB} {
		before := map[string]bool{}
		for _, p := range e.m.Pending(l.ID) {
			before[p.ID] = true
		}
		e.call(e.m.RequestAccess, l.ID, b, "{}")
		for _, p := range e.m.Pending(l.ID) {
			if !before[p.ID] {
				ids[b] = p.ID
			}
		}
	}

	fake.EmitData(EventDeviceDecision, map[string]any{"request_id": ids[approveB], "approve": true})
	fake.EmitData(EventDeviceDecision, map[string]any{"request_id": ids[denyB], "approve": false})
	want(t, "approved by HA", e.call(e.m.Status, l.ID, approveB, ""), 200, "approved")
	want(t, "denied by HA", e.call(e.m.Status, l.ID, denyB, ""), 200, "denied")
	id, _, _ := e.admit(l.ID, approveB)
	if d, _ := e.link(l.ID).Device(id); d.ApprovedBy != model.ApprovedByHA {
		t.Errorf("approved by %q", d.ApprovedBy)
	}

	// Malformed payloads change nothing, and never count as a deny.
	for _, raw := range []string{
		`not json`, `null`, `[]`, `{}`, `{"approve":true}`,
		fmt.Sprintf(`{"request_id":%q}`, ids[keepB]),
		fmt.Sprintf(`{"request_id":%q,"approve":"false"}`, ids[keepB]),
		fmt.Sprintf(`{"request_id":%q,"approve":0}`, ids[keepB]),
		fmt.Sprintf(`{"request_id":%q,"approve":null}`, ids[keepB]),
	} {
		fake.Emit(ha.Event{EventType: EventDeviceDecision, Data: json.RawMessage(raw)})
	}
	want(t, "after malformed events", e.call(e.m.Status, l.ID, keepB, ""), 200, "pending")
	if !strings.Contains(e.logs.String(), "malformed device decision event") {
		t.Error("malformed event not logged")
	}

	// Unknown and expired ids are ignored with a log line.
	fake.EmitData(EventDeviceDecision, map[string]any{"request_id": "0123456789abcdef0123456789abcdef", "approve": true})
	e.clock.Add(DefaultPendingTTL)
	fake.EmitData(EventDeviceDecision, map[string]any{"request_id": ids[keepB], "approve": true})
	if n := strings.Count(e.logs.String(), "unknown or expired request"); n != 2 {
		t.Errorf("%d unknown/expired log lines, want 2", n)
	}
	if len(e.link(l.ID).Devices) != 1 {
		t.Errorf("devices %+v", e.link(l.ID).Devices)
	}
}

// A huge request id from HA is cut before it reaches the log.
func TestDecisionEventLogIsCapped(t *testing.T) {
	e := newEnv(t, Options{})
	raw, _ := json.Marshal(map[string]any{"request_id": strings.Repeat("A", 10000), "approve": true})
	e.m.HandleDecisionEvent(raw)
	if strings.Count(e.logs.String(), "A") > 200 {
		t.Error("request id logged in full")
	}
}

func FuzzHandleDecisionEvent(f *testing.F) {
	for _, s := range []string{`{"request_id":"x","approve":true}`, `{}`, `null`, `{"approve":"1"}`, "\x00", `{"request_id":1}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		e := newEnv(t, Options{})
		l := e.newLink(manual)
		e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}")
		e.m.HandleDecisionEvent(json.RawMessage(raw))
		// No random payload can name the 128-bit request id.
		if len(e.link(l.ID).Devices) != 0 {
			t.Fatalf("payload %q approved a device", raw)
		}
	})
}

// StillValid follows the stored link: a revoke, a reset, or approval
// turned on ends what it said before.
func TestStillValid(t *testing.T) {
	e := newEnv(t, Options{})
	l, _, id := e.claimed(claim)
	off := l
	off.Approval = model.Approval{}
	for _, c := range []struct {
		name   string
		link   model.Link
		device string
		want   bool
	}{
		{"approval off, no device", off, "", true},
		{"approved device", e.link(l.ID), id, true},
		{"no device while on", e.link(l.ID), "", false},
		{"unknown device", e.link(l.ID), "0000000000000000", false},
	} {
		if got := e.m.StillValid(c.link, c.device); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if err := e.m.RemoveDevice(l.ID, id); err != nil {
		t.Fatal(err)
	}
	if e.m.StillValid(e.link(l.ID), id) {
		t.Error("revoked device still valid")
	}
}
