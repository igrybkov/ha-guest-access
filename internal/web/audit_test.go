package web

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/audit"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// lockedBuffer is a bytes.Buffer safe for parallel writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// auditHarness is a harness whose audit log goes to a file and a buffer.
type auditHarness struct {
	*harness
	path string
	log  *lockedBuffer
	al   *audit.Logger
}

func newAuditHarness(t *testing.T, opts ...harnessOpt) *auditHarness {
	t.Helper()
	ah := &auditHarness{path: filepath.Join(t.TempDir(), audit.FileName), log: &lockedBuffer{}}
	al, err := audit.New(audit.Options{Path: ah.path, Enabled: true, Logger: slog.New(slog.NewTextHandler(ah.log, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { al.Close() })
	ah.al = al
	opts = append(opts, func(o *config.Options, d *Deps) { d.Audit = al })
	ah.harness = newHarness(t, opts...)
	return ah
}

// records reads the audit file, oldest first.
func (ah *auditHarness) records() []audit.Record {
	ah.t.Helper()
	raw, _ := os.ReadFile(ah.path)
	var out []audit.Record
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r audit.Record
		if err := json.Unmarshal(line, &r); err != nil {
			ah.t.Fatalf("bad line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// Markers that must never reach the audit file or the app log.
const (
	markerSlug     = "zzslugmarker7731"
	markerPassword = "pw-marker-5551"
	markerLat      = 41.87811234
	markerLon      = -87.62981234
)

// Every event kind goes through real handlers. Neither the file nor the
// log holds a slug, a password, a cookie value or a coordinate.
func TestAuditEveryEventNeverLeaks(t *testing.T) {
	ah := newAuditHarness(t)
	var cookies []string
	keep := func(rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		for _, c := range rec.Result().Cookies() {
			if c.Value != "" {
				cookies = append(cookies, c.Value)
			}
		}
		return rec
	}
	l, err := ah.links.Create(model.Link{Name: "Anna's visit", ValidFrom: t0.Add(-time.Hour), ValidUntil: t0.Add(24 * time.Hour),
		TemplateIDs: []string{"guest_room", "entry"}}, markerSlug)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := access.HashPassword(markerPassword)
	ah.links.AddPassword(l.ID, "Anna", hash)
	login := keep(ah.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": markerPassword}}))
	session := cookie(login, sessionCookie)
	ah.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": "wrong"}})
	keep(ah.act(l, "light.guest_room", "light.turn_on", map[string]any{"brightness_pct": 40}, session))
	ah.act(l, "light.bedroom", "light.turn_on", nil, session)
	ah.g(req{method: "GET", path: "/g/" + l.Slug + "/state", cookies: []*http.Cookie{{Name: session.Name, Value: "AQIAAAAAAAAAAAAAAAAA"}}})
	keep(ah.g(req{method: "POST", path: "/g/" + l.Slug + "/where", cookies: []*http.Cookie{session},
		body: map[string]any{"lat": markerLat, "lon": markerLon, "accuracy": 5}}))
	ah.g(req{method: "POST", path: "/g/" + l.Slug + "/where", cookies: []*http.Cookie{session},
		body: map[string]any{"lat": markerLat, "lon": markerLon, "accuracy": 1e6}})
	// Lockout: five wrong passwords from one IP.
	for range 5 {
		ah.g(req{method: "POST", path: "/g/" + l.Slug + "/password", peer: "203.0.113.20:1", body: map[string]any{"password": "nope"}})
	}
	ah.g(req{method: "GET", path: "/g/" + markerSlug + "x/state"})
	for range 20 {
		ah.g(req{method: "GET", path: "/g/" + l.Slug, peer: "203.0.113.30:1"})
	}

	// Device approval: claim, request, approve, deny, revoke, reset.
	ah.advance(time.Minute)
	claim := ah.approvalLink(model.Approval{SelfService: true})
	j1, j2, j3 := jar{}, jar{}, jar{}
	keep(ah.dev(j1, claim, "POST", "device/request", map[string]any{}))
	keep(ah.dev(j2, claim, "POST", "device/request", map[string]any{}))
	manual := ah.approvalLink(model.Approval{Manual: true})
	keep(ah.dev(j3, manual, "POST", "device/request", map[string]any{}))
	ah.approveNext(manual, true)
	keep(ah.dev(j3, manual, "GET", "device/status", nil))
	keep(ah.dev(jar{}, manual, "POST", "device/request", map[string]any{}))
	ah.approveNext(manual, false)
	got, _ := ah.links.Get(manual.ID)
	ah.a(req{method: "DELETE", path: "/api/links/" + manual.ID + "/devices/" + got.Devices[0].ID, body: map[string]any{}})
	ah.a(req{method: "POST", path: "/api/links/" + claim.ID + "/devices/reset", body: map[string]any{}})
	// Admin writes, and a refused peer.
	ah.a(req{method: "POST", path: "/api/links/" + l.ID + "/rotate", body: map[string]any{"slug": markerSlug + "b"},
		headers: map[string]string{"X-Remote-User-Name": "illia"}})
	ah.a(req{method: "POST", path: "/api/links/" + l.ID + "/passwords", body: map[string]any{"label": "Bob", "password": markerPassword + "b"}})
	ah.a(req{method: "GET", path: "/api/links", peer: "203.0.113.99:1"})

	recs := ah.records()
	seen := map[string]bool{}
	for _, r := range recs {
		seen[r.Event] = true
	}
	for _, want := range []string{
		"guest.password.ok", "guest.password.fail", "guest.lockout", "guest.act.allowed", "guest.act.denied",
		"guest.session.invalid", "guest.near.ok", "guest.near.denied", "guest.slug.miss", "guest.rate_limited",
		"device.claimed", "device.request", "device.approved", "device.denied", "device.revoked", "device.reset",
		"admin.device.decide", "admin.device.revoke", "admin.device.reset", "admin.link.rotate", "admin.password.add",
		"admin.refused",
	} {
		if !seen[want] {
			t.Errorf("no %s record", want)
		}
	}
	raw, _ := os.ReadFile(ah.path)
	for name, out := range map[string]string{"file": string(raw), "log": ah.log.String()} {
		for _, bad := range []string{markerSlug, markerPassword, "41.878112", "87.629812"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s holds %q", name, bad)
			}
		}
		for _, c := range cookies {
			if strings.Contains(out, c) {
				t.Errorf("%s holds a cookie value", name)
			}
		}
	}
	if len(cookies) < 4 {
		t.Errorf("only %d cookies seen", len(cookies))
	}
}

// One denied act gives one record with the internal reason; the guest
// sees only a generic message.
func TestAuditActDenialReasons(t *testing.T) {
	ah := newAuditHarness(t)
	l := ah.link("guest_room", "entry", "office")
	for _, c := range []struct {
		entity, action string
		data           map[string]any
		reason         string
	}{
		{"light.bedroom", "light.turn_on", nil, reasonNotGranted},
		{"switch.office", "switch.turn_on", nil, reasonNetwork},
		{"lock.front_door", "lock.unlock", nil, reasonNear},
		{"light.guest_room", "light.turn_on", map[string]any{"brightness_pct": 500}, "bad_data:brightness_pct"},
	} {
		before := len(ah.records())
		rec := ah.act(l, c.entity, c.action, c.data)
		if strings.Contains(rec.Body.String(), c.reason) {
			t.Errorf("%s: reason in the response: %s", c.reason, rec.Body)
		}
		recs := ah.records()[before:]
		if len(recs) != 1 || recs[0].Event != "guest.act.denied" || recs[0].Reason != c.reason ||
			recs[0].EntityID != c.entity || recs[0].LinkID != l.ID {
			t.Errorf("%s: records %+v", c.reason, recs)
		}
	}
}

// Admin writes name the HA user from the ingress headers. Reads are not
// recorded.
func TestAuditAdminUser(t *testing.T) {
	ah := newAuditHarness(t)
	l := ah.link("guest_room")
	user := map[string]string{"X-Remote-User-Id": "u1", "X-Remote-User-Name": "illia", "X-Remote-User-Display-Name": "Illia"}
	ah.a(req{method: "POST", path: "/api/links/" + l.ID + "/revoke", body: map[string]any{}, headers: user})
	ah.a(req{method: "GET", path: "/api/links", headers: user})
	ah.a(req{method: "DELETE", path: "/api/links/nope", body: map[string]any{}, headers: user})
	recs := ah.records()
	if len(recs) != 2 {
		t.Fatalf("records = %+v", recs)
	}
	r := recs[0]
	if r.Event != "admin.link.revoke" || r.Result != audit.ResultOK || r.LinkID != l.ID || r.LinkName != l.Name ||
		r.User == nil || r.User.ID != "u1" || r.User.Name != "illia" || r.User.DisplayName != "Illia" {
		t.Errorf("revoke record = %+v", r)
	}
	if r := recs[1]; r.Event != "admin.link.delete" || r.Result != audit.ResultFailed || r.Reason != "not_found" || r.Status != 404 {
		t.Errorf("failed delete record = %+v", r)
	}
	// GET /api/audit reads them back, newest first.
	v := decode[struct {
		Records []audit.Record `json:"records"`
	}](t, ah.a(req{method: "GET", path: "/api/audit?event=admin.link&limit=1"}))
	if len(v.Records) != 1 || v.Records[0].Event != "admin.link.delete" {
		t.Errorf("audit API = %+v", v.Records)
	}
	if rec := ah.a(req{method: "GET", path: "/api/audit?limit=x"}); rec.Code != 400 {
		t.Errorf("bad limit: %d", rec.Code)
	}
}

// A flood of slug misses from one IP gives one record per period, which
// then counts the ones left out.
func TestAuditThrottlesMisses(t *testing.T) {
	ah := newAuditHarness(t)
	for range 5 {
		ah.g(req{method: "GET", path: "/g/no-such-slug-here/state"})
	}
	ah.advance(2 * time.Minute)
	ah.g(req{method: "GET", path: "/g/no-such-slug-here/state"})
	var misses []audit.Record
	for _, r := range ah.records() {
		if r.Event == "guest.slug.miss" {
			misses = append(misses, r)
		}
	}
	if len(misses) != 2 || misses[1].Count != 4 {
		t.Errorf("misses = %+v", misses)
	}
}

// A slider drag gives one allowed record per period per entity, not one
// per value. The next record counts the values left out.
func TestAuditThrottlesSliderDrags(t *testing.T) {
	ah := newAuditHarness(t)
	l := ah.link("guest_room")
	for i := range 20 {
		if c := ah.slider(l, "light.guest_room", 1+i); c != 200 {
			t.Fatalf("value %d: %d", i, c)
		}
		ah.advance(100 * time.Millisecond)
	}
	ah.advance(10 * time.Second)
	ah.slider(l, "light.guest_room", 50)
	ah.act(l, "light.guest_room", "light.turn_off", nil)
	var allowed []audit.Record
	for _, r := range ah.records() {
		if r.Event == "guest.act.allowed" {
			allowed = append(allowed, r)
		}
	}
	if len(allowed) != 3 || allowed[1].Count != 19 || allowed[2].Action != "light.turn_off" {
		t.Errorf("allowed records = %+v", allowed)
	}
}

// With the dev bypass on, a peer other than the ingress proxy can send
// X-Remote-User headers. They are not read: the user is recorded as dev.
// The ingress proxy's headers still count.
func TestAuditAdminDevPeerUser(t *testing.T) {
	ah := newAuditHarness(t, func(_ *config.Options, d *Deps) { d.DevAdminAnyPeer = true })
	l := ah.link("guest_room")
	user := map[string]string{"X-Remote-User-Id": "u1", "X-Remote-User-Name": "illia", "X-Remote-User-Display-Name": "Illia"}
	if c := ah.a(req{method: "POST", path: "/api/links/" + l.ID + "/revoke", body: map[string]any{}, headers: user, peer: "192.0.2.5:4000"}).Code; c != 200 {
		t.Fatalf("dev peer: %d", c)
	}
	ah.a(req{method: "POST", path: "/api/links/" + l.ID + "/revoke", body: map[string]any{}, headers: user})
	recs := ah.records()
	if len(recs) != 2 {
		t.Fatalf("records = %+v", recs)
	}
	if u := recs[0].User; u == nil || *u != (audit.User{Name: "dev"}) || recs[0].Peer != "192.0.2.5" {
		t.Errorf("dev peer record: user %+v, peer %q", u, recs[0].Peer)
	}
	if u := recs[1].User; u == nil || u.ID != "u1" || u.Name != "illia" || u.DisplayName != "Illia" {
		t.Errorf("ingress record: user %+v", u)
	}
}

// A flood of one guest denial kind from one client can't rotate a recent
// admin record out of the audit files. Limits are off, so nothing else
// stops the flood.
func TestAuditDenialFloodKeepsAdminRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), audit.FileName)
	al, err := audit.New(audit.Options{Path: path, Enabled: true, MaxBytes: 16 << 10, Logger: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { al.Close() })
	h := newHarness(t, func(o *config.Options, d *Deps) {
		o.LimitsEnabled = false
		d.Audit = al
	})
	l := h.link("guest_room")
	other := h.link("guest_room")
	if c := h.a(req{method: "POST", path: "/api/links/" + other.ID + "/revoke", body: map[string]any{}}).Code; c != 200 {
		t.Fatalf("revoke: %d", c)
	}
	for i := range 500 {
		if c := h.act(l, "light.bedroom", "light.turn_on", nil).Code; c != 403 {
			t.Fatalf("flood %d: %d", i, c)
		}
		h.advance(10 * time.Millisecond)
	}
	var all []byte
	for _, p := range []string{path, path + ".1"} {
		raw, _ := os.ReadFile(p)
		all = append(all, raw...)
	}
	if !bytes.Contains(all, []byte(`"event":"admin.link.revoke"`)) {
		t.Error("the admin record was rotated out")
	}
	if n := bytes.Count(all, []byte(`"event":"guest.act.denied"`)); n > 5 {
		t.Errorf("%d denial records for one client in 5 s", n)
	}
}
