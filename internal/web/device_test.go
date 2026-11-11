package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/approval"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// approvalLink makes a guest room link with device approval a.
func (h *harness) approvalLink(a model.Approval) model.Link {
	h.t.Helper()
	l, err := h.links.Create(model.Link{
		Name: "Anna's visit", ValidFrom: t0.Add(-time.Hour), ValidUntil: t0.Add(24 * time.Hour),
		TemplateIDs: []string{"guest_room"}, Approval: a,
	}, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return l
}

// jar keeps the cookies one browser got, by name.
type jar map[string]*http.Cookie

func (j jar) take(rec *httptest.ResponseRecorder) {
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(j, c.Name)
			continue
		}
		j[c.Name] = c
	}
}

func (j jar) list() []*http.Cookie {
	out := make([]*http.Cookie, 0, len(j))
	for _, c := range j {
		out = append(out, c)
	}
	return out
}

// dev sends a guest request for l with the jar's cookies and keeps what
// comes back.
func (h *harness) dev(j jar, l model.Link, method, route string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := h.g(req{method: method, path: "/g/" + l.Slug + "/" + route, body: body, cookies: j.list()})
	j.take(rec)
	return rec
}

// approveNext approves the link's oldest pending request from the admin
// API.
func (h *harness) approveNext(l model.Link, approve bool) string {
	h.t.Helper()
	v := decode[struct {
		Requests []pendingView `json:"requests"`
	}](h.t.(*testing.T), h.a(req{method: "GET", path: "/api/devices/pending?link=" + l.ID}))
	if len(v.Requests) == 0 {
		h.t.Fatal("no pending request")
	}
	if v.Requests[0].LinkName != l.Name {
		h.t.Errorf("pending link name = %q", v.Requests[0].LinkName)
	}
	id := v.Requests[0].ID
	rec := h.a(req{method: "POST", path: "/api/devices/pending/" + id, body: map[string]any{"approve": approve}})
	if rec.Code != 200 {
		h.t.Fatalf("decide: %d %s", rec.Code, rec.Body)
	}
	return id
}

func TestDeviceManualFlow(t *testing.T) {
	h := newHarness(t)
	l := h.approvalLink(model.Approval{Manual: true})
	j := jar{}
	if rec := h.dev(j, l, "GET", "state", nil); rec.Code != 403 || decode[map[string]string](t, rec)["status"] != "approval_required" {
		t.Fatalf("state before request: %d %s", rec.Code, rec.Body)
	}
	rec := h.dev(j, l, "POST", "device/request", map[string]any{})
	if rec.Code != 200 || decode[map[string]string](t, rec)["status"] != "pending" {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	rc := j[approval.RequestCookieName(l.ID)]
	if rc == nil || rc.Path != access.GuestCookiePath || !rc.HttpOnly || rc.SameSite != http.SameSiteStrictMode {
		t.Fatalf("request cookie = %+v", rc)
	}
	if ev := h.fake.FiredEvents(); len(ev) != 1 || ev[0].EventType != approval.EventDeviceRequest {
		t.Fatalf("events = %+v", ev)
	}
	// The link view shows the pending request and an empty device list.
	v := decode[map[string]any](t, h.a(req{method: "GET", path: "/api/links/" + l.ID}))
	if p, _ := v["pending"].([]any); len(p) != 1 {
		t.Errorf("link view pending = %v", v["pending"])
	}
	if d, ok := v["devices"].([]any); !ok || len(d) != 0 {
		t.Errorf("link view devices = %v", v["devices"])
	}
	if a, _ := v["approval"].(map[string]any); a["manual"] != true {
		t.Errorf("link view approval = %v", v["approval"])
	}

	h.approveNext(l, true)
	// Polling picks up the device cookie.
	if rec := h.dev(j, l, "GET", "device/status", nil); decode[map[string]string](t, rec)["status"] != "approved" {
		t.Fatalf("status: %s", rec.Body)
	}
	if j[approval.DeviceCookieName(l.ID)] == nil || j[approval.RequestCookieName(l.ID)] != nil {
		t.Fatalf("cookies after approval: %v", j)
	}
	if rec := h.dev(j, l, "POST", "act", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}); rec.Code != 200 {
		t.Fatalf("act: %d %s", rec.Code, rec.Body)
	}
	v = decode[map[string]any](t, h.a(req{method: "GET", path: "/api/links/" + l.ID}))
	if d, _ := v["devices"].([]any); len(d) != 1 {
		t.Errorf("devices after approval = %v", v["devices"])
	}
	// Unknown requests and devices are 404.
	if rec := h.a(req{method: "POST", path: "/api/devices/pending/nope", body: map[string]any{"approve": true}}); rec.Code != 404 {
		t.Errorf("decide unknown: %d", rec.Code)
	}
	if rec := h.a(req{method: "POST", path: "/api/devices/pending/nope", body: map[string]any{}}); rec.Code != 400 {
		t.Errorf("decide without approve: %d", rec.Code)
	}
	if rec := h.a(req{method: "DELETE", path: "/api/links/" + l.ID + "/devices/0000000000000000", body: map[string]any{}}); rec.Code != 404 {
		t.Errorf("remove unknown device: %d", rec.Code)
	}
}

func TestDeviceDenied(t *testing.T) {
	h := newHarness(t)
	l := h.approvalLink(model.Approval{Manual: true})
	j := jar{}
	h.dev(j, l, "POST", "device/request", map[string]any{})
	h.approveNext(l, false)
	if rec := h.dev(j, l, "GET", "device/status", nil); decode[map[string]string](t, rec)["status"] != "denied" {
		t.Fatalf("status: %s", rec.Body)
	}
	if rec := h.dev(j, l, "GET", "state", nil); rec.Code != 403 {
		t.Errorf("state after deny: %d", rec.Code)
	}
}

// Device routes need the password session but not the device gate, and
// guest POSTs keep their Origin and JSON checks.
func TestDeviceRoutesChecks(t *testing.T) {
	h := newHarness(t)
	l := h.approvalLink(model.Approval{Manual: true})
	hash, _ := access.HashPassword("pw")
	if _, err := h.links.AddPassword(l.ID, "Anna", hash); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"device/status", "device/pending"} {
		if rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/" + route}); rec.Code != 401 {
			t.Errorf("%s without session: %d", route, rec.Code)
		}
	}
	for _, route := range []string{"device/request", "device/decide"} {
		if rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/" + route, body: map[string]any{}}); rec.Code != 401 {
			t.Errorf("%s without session: %d", route, rec.Code)
		}
	}
	j := jar{}
	if rec := h.dev(j, l, "POST", "password", map[string]any{"password": "pw"}); rec.Code != 200 {
		t.Fatalf("login: %d", rec.Code)
	}
	if rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/device/request", body: map[string]any{}, origin: "https://evil.example", cookies: j.list()}); rec.Code != 403 {
		t.Errorf("cross-origin request: %d", rec.Code)
	}
	if rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/device/request", body: "x", ctype: "text/plain", cookies: j.list()}); rec.Code != 415 {
		t.Errorf("non-JSON request: %d", rec.Code)
	}
	if rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/device/request", cookies: j.list()}); rec.Code != 405 {
		t.Errorf("GET request: %d", rec.Code)
	}
	if rec := h.dev(j, l, "POST", "device/request", map[string]any{}); rec.Code != 200 {
		t.Fatalf("request with session: %d %s", rec.Code, rec.Body)
	}
	if ev := h.fake.FiredEvents(); len(ev) != 1 || ev[0].Data["label"] != "Anna" {
		t.Errorf("event label: %+v", ev)
	}
	// A waiting page polls status often. It uses the authed limiter, so
	// it gets more than the pre-auth burst.
	for i := range 30 {
		if rec := h.dev(j, l, "GET", "device/status", nil); rec.Code != 200 {
			t.Fatalf("poll %d: %d", i, rec.Code)
		}
	}
	// A huge decide body is refused.
	big := map[string]any{"request_id": string(make([]byte, 64<<10)), "approve": true}
	if rec := h.dev(j, l, "POST", "device/decide", big); rec.Code < 400 {
		t.Errorf("huge decide body: %d", rec.Code)
	}
}

// Rotating a slug keeps the device and the password session, since the
// cookies are per link id under /g/. A cookie value of link A in a
// cookie named for link B still fails: the token is bound to A's id.
func TestDeviceAndSessionSurviveSlugRotation(t *testing.T) {
	h := newHarness(t)
	l := h.approvalLink(model.Approval{SelfService: true}) // claim mode
	hash, _ := access.HashPassword("pw")
	h.links.AddPassword(l.ID, "Anna", hash)
	j := jar{}
	h.dev(j, l, "POST", "password", map[string]any{"password": "pw"})
	if rec := h.dev(j, l, "POST", "device/request", map[string]any{}); decode[map[string]string](t, rec)["status"] != "approved" {
		t.Fatalf("claim: %s", rec.Body)
	}
	rec := h.a(req{method: "POST", path: "/api/links/" + l.ID + "/rotate", body: map[string]any{}})
	if rec.Code != 200 {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body)
	}
	rotated, _ := h.links.Get(l.ID)
	if rotated.Slug == l.Slug {
		t.Fatal("slug did not change")
	}
	if rec := h.dev(j, l, "GET", "state", nil); rec.Code != 404 {
		t.Errorf("old slug: %d", rec.Code)
	}
	if rec := h.dev(j, rotated, "GET", "state", nil); rec.Code != 200 {
		t.Fatalf("new slug with old cookies: %d %s", rec.Code, rec.Body)
	}

	// Link B, also claimed by another browser, with the same password.
	b := h.approvalLink(model.Approval{SelfService: true})
	h.links.AddPassword(b.ID, "Anna", hash)
	jb := jar{}
	h.dev(jb, b, "POST", "password", map[string]any{"password": "pw"})
	h.dev(jb, b, "POST", "device/request", map[string]any{})
	if rec := h.dev(jb, b, "GET", "state", nil); rec.Code != 200 {
		t.Fatalf("link B: %d", rec.Code)
	}
	// A's session and device values under B's names fail on B.
	cross := []*http.Cookie{
		{Name: access.LinkCookie(sessionCookie, b.ID), Value: j[access.LinkCookie(sessionCookie, l.ID)].Value},
		{Name: approval.DeviceCookieName(b.ID), Value: j[approval.DeviceCookieName(l.ID)].Value},
	}
	if rec := h.g(req{method: "GET", path: "/g/" + b.Slug + "/state", cookies: cross}); rec.Code != 401 {
		t.Errorf("A's session on B: %d", rec.Code)
	}
	cross[0] = jb[access.LinkCookie(sessionCookie, b.ID)]
	if rec := h.g(req{method: "GET", path: "/g/" + b.Slug + "/state", cookies: cross}); rec.Code != 403 {
		t.Errorf("A's device on B: %d", rec.Code)
	}
}

// In claim mode, "reset devices" lets the next device claim the link,
// and the old device's cookie fails at once.
func TestDeviceResetClaimsAgain(t *testing.T) {
	h := newHarness(t)
	l := h.approvalLink(model.Approval{SelfService: true})
	first, second := jar{}, jar{}
	h.dev(first, l, "POST", "device/request", map[string]any{})
	if rec := h.dev(second, l, "POST", "device/request", map[string]any{}); decode[map[string]string](t, rec)["status"] != "pending" {
		t.Fatalf("second before reset: %s", rec.Body)
	}
	rec := h.a(req{method: "POST", path: "/api/links/" + l.ID + "/devices/reset", body: map[string]any{}})
	if v := decode[map[string]any](t, rec); rec.Code != 200 || len(v["devices"].([]any)) != 0 || len(v["pending"].([]any)) != 0 {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	if rec := h.dev(first, l, "GET", "state", nil); rec.Code != 403 {
		t.Errorf("old device after reset: %d", rec.Code)
	}
	third := jar{}
	if rec := h.dev(third, l, "POST", "device/request", map[string]any{}); decode[map[string]string](t, rec)["status"] != "approved" {
		t.Fatalf("claim after reset: %s", rec.Body)
	}
	if rec := h.dev(third, l, "GET", "state", nil); rec.Code != 200 {
		t.Errorf("new claimer: %d", rec.Code)
	}
}

// Revoking a device ends its open stream.
func TestDeviceRevokeEndsStream(t *testing.T) {
	h := newHarness(t)
	l := h.approvalLink(model.Approval{SelfService: true})
	j := jar{}
	h.dev(j, l, "POST", "device/request", map[string]any{})
	got, _ := h.links.Get(l.ID)
	if len(got.Devices) != 1 {
		t.Fatalf("devices = %+v", got.Devices)
	}
	ch, resp := openStream(t, h, l, j.list()...)
	if resp.StatusCode != 200 {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	for ev, _ := next(t, ch); ev.name != "ready"; ev, _ = next(t, ch) {
	}
	if rec := h.a(req{method: "DELETE", path: "/api/links/" + l.ID + "/devices/" + got.Devices[0].ID, body: map[string]any{}}); rec.Code != 200 {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	for {
		ev, ok := next(t, ch)
		if !ok {
			t.Fatal("closed without end")
		}
		if ev.name == "end" {
			break
		}
	}
	if rec := h.dev(j, l, "GET", "state", nil); rec.Code != 403 {
		t.Errorf("state after revoke: %d", rec.Code)
	}
}

// Turning approval on ends a stream that opened without a device.
func TestDeviceApprovalOnEndsStream(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	ch, _ := openStream(t, h, l)
	for ev, _ := next(t, ch); ev.name != "ready"; ev, _ = next(t, ch) {
	}
	l.Approval = model.Approval{Manual: true}
	if _, err := h.links.Update(l.ID, l); err != nil {
		t.Fatal(err)
	}
	for {
		ev, ok := next(t, ch)
		if !ok {
			t.Fatal("closed without end")
		}
		if ev.name == "end" {
			return
		}
	}
}
