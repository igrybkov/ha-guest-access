package web

// Security review tests. A test named TestSecFails... or listed as a
// finding fails until the fix lands. The others lock in behavior that was
// reviewed and found safe.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/approval"
	"github.com/igrybkov/ha-guest-access/internal/audit"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
	"github.com/igrybkov/ha-guest-access/internal/inventory"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// ---- findings ----

// handWritten builds a harness whose link store reads a links.yaml
// written by hand, with no validation on the way in.
func handWritten(t *testing.T, ls ...model.Link) *harness {
	t.Helper()
	dir := t.TempDir()
	raw, err := yaml.Marshal(model.Links{Version: migrate.DataCurrent, Links: ls})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, migrate.LinksFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := links.New(migrate.OpenData(dir, quietLog), links.Options{Logger: quietLog})
	return newHarness(t, func(_ *config.Options, d *Deps) { d.Links = store })
}

func handLink(slug string, extra model.Rule) model.Link {
	return model.Link{
		ID: "hand0001", Name: "Hand", Slug: slug, CreatedAt: t0,
		ValidFrom: t0.Add(-time.Hour), ValidUntil: t0.Add(24 * time.Hour),
		Extra: []model.Rule{extra},
	}
}

// A links.yaml entry that breaks model.Link.Validate loads and is served
// as is. The guest side does not validate links, so a typo in a hand edit
// widens access: network "LOCAL" is not "local", near_home_m below zero
// reads as off, and a wildcard-only select (refused on save) grants a
// whole domain. AGENTS.md says the guest side fails closed on these.
func TestSecHandWrittenLinkFailsClosed(t *testing.T) {
	const slug = "handwrittenslug0001"
	cases := []struct {
		name   string
		rule   model.Rule
		entity string
		action string
	}{
		{"network typo", model.Rule{
			Select: model.Selector{Entities: []string{"switch.office"}}, Actions: []string{"switch.turn_on"},
			Access: &model.Access{Network: "LOCAL"},
		}, "switch.office", "switch.turn_on"},
		{"negative radius", model.Rule{
			Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock"},
			Access: &model.Access{NearHomeM: -5},
		}, "lock.front_door", "lock.unlock"},
		{"wildcard only select", model.Rule{
			Select: model.Selector{Entities: []string{"*"}}, Actions: []string{"light.turn_on"},
		}, "light.bedroom", "light.turn_on"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := handWritten(t, handLink(slug, tc.rule))
			rec := h.g(req{method: "POST", path: "/g/" + slug + "/act",
				body: map[string]any{"entity_id": tc.entity, "action": tc.action}})
			if rec.Code == http.StatusOK {
				t.Fatalf("an invalid hand-written link granted %s on %s: %d %s", tc.action, tc.entity, rec.Code, rec.Body)
			}
			if n := len(h.fake.ServiceCalls()); n != 0 {
				t.Fatalf("%d HA calls", n)
			}
		})
	}
	t.Run("short slug", func(t *testing.T) {
		h := handWritten(t, handLink("abc", model.Rule{
			Select: model.Selector{Entities: []string{"light.guest_room"}}, Actions: []string{"light.turn_on"},
		}))
		if rec := h.g(req{method: "GET", path: "/g/abc/state"}); rec.Code != http.StatusNotFound {
			t.Fatalf("a 3 character slug works: %d", rec.Code)
		}
	})
}

// Safe: a hand-written link with a "code" key in its variables, or with a
// zero valid_until, does not validate, so guests get not found for it.
func TestSecHandWrittenLinkSafeCases(t *testing.T) {
	const slug = "handwrittenslug0002"
	h := handWritten(t, handLink(slug, model.Rule{
		Select: model.Selector{Entities: []string{"script.let_me_in"}}, Actions: []string{"script.run"},
		Variables: map[string]any{"Code": "1234"},
	}))
	if rec := h.g(req{method: "POST", path: "/g/" + slug + "/act",
		body: map[string]any{"entity_id": "script.let_me_in", "action": "script.run"}}); rec.Code != http.StatusNotFound {
		t.Errorf("script with a code variable: %d", rec.Code)
	}
	l := handLink(slug, model.Rule{Select: model.Selector{Entities: []string{"light.guest_room"}}, Actions: []string{"light.turn_on"}})
	l.ValidUntil = time.Time{}
	h = handWritten(t, l)
	if rec := h.g(req{method: "POST", path: "/g/" + slug + "/act",
		body: map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}}); rec.Code != http.StatusNotFound {
		t.Errorf("zero valid_until: %d", rec.Code)
	}
}

// With no home location HA reports 0, 0. The docs say the guest side fails
// closed on a near-home rule then. It does not: the guest sends 0, 0 and is
// at "home" (everybody knows these coordinates).
func TestSecNoHomeLocationFailsClosed(t *testing.T) {
	h := newHarness(t)
	h.fake.Core = ha.Config{}
	l := h.link("entry") // lock.front_door needs near_home_m 300
	rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/where",
		body: map[string]any{"lat": 0, "lon": 0, "accuracy": 5}})
	var cs []*http.Cookie
	if c := cookie(rec, nearCookie); c != nil {
		cs = append(cs, c)
	}
	act := h.act(l, "lock.front_door", "lock.unlock", nil, cs...)
	if act.Code == http.StatusOK {
		t.Fatalf("lock opened with no home set, from 0,0 (where: %d)", rec.Code)
	}
}

// Rate limits key on the full IPv6 address. One /64 holds 2^64 addresses,
// so a client with one prefix gets a fresh pre-auth bucket per request.
func TestSecIPv6PrefixSharesLimiter(t *testing.T) {
	h := newHarness(t)
	limited := false
	for i := range 60 {
		peer := fmt.Sprintf("[2001:db8:1:2::%x]:5000", i+1)
		if h.g(req{method: "GET", path: "/nothing", peer: peer}).Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("60 requests from one /64, each from its own address, were never limited")
	}
}

// Every guest request, even a refused one, adds a diagnostics entry with
// the forwarding headers as sent and, for a path under /api/, the whole
// path (routeLabel returns it as is). Each can be close to 1 MiB, and the
// ring keeps `diagnostics.requests` entries (up to 1000). The admin page
// renders them as text, so this is memory, not script.
func TestSecDiagnosticsBoundsHeaderValues(t *testing.T) {
	h := newHarness(t)
	h.g(req{method: "GET", path: "/nothing", headers: map[string]string{
		"X-Forwarded-For": strings.Repeat("a", 60_000), "X-Forwarded-Host": strings.Repeat("b", 60_000),
	}})
	h.g(req{method: "GET", path: "/api/" + strings.Repeat("x", 60_000)})
	for _, e := range h.srv.diag.list() {
		if len(e.Route) > 1024 {
			t.Fatalf("diagnostics keep a route of %d bytes", len(e.Route))
		}
		for k, v := range e.Headers {
			if len(v) > 1024 {
				t.Fatalf("diagnostics keep %d bytes of %s", len(v), k)
			}
		}
	}
}

// A timeout or a lost connection does not say the call did not run. The
// use of a one-time pair is given back anyway, so the guest can run an
// unlock twice. This one needs a decision: DOCS say a failed call gives the
// use back. The fix must split ambiguous errors (timeout, connection lost
// after the send) from a definite error result from HA. The stand-in error
// here wraps context.DeadlineExceeded; the real client's error shape on a
// timeout was not checked.
func TestSecOneTimeNotRefundedOnAmbiguousFailure(t *testing.T) {
	h := newHarness(t)
	l, err := h.links.Create(model.Link{
		Name: "Once", ValidFrom: t0.Add(-time.Hour), ValidUntil: t0.Add(24 * time.Hour),
		TemplateIDs: []string{"guest_room"}, OneTime: true,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	n := 0
	h.fake.Respond = func(hatest.ServiceCall) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return nil, fmt.Errorf("ha: call: %w", context.DeadlineExceeded)
		}
		return nil, nil
	}
	if c := h.act(l, "light.guest_room", "light.turn_off", nil).Code; c != http.StatusBadGateway {
		t.Fatalf("first call: %d", c)
	}
	if c := h.act(l, "light.guest_room", "light.turn_off", nil).Code; c == http.StatusOK {
		t.Fatalf("the one-time pair ran again after a call that may have run (%d HA calls)", len(h.fake.ServiceCalls()))
	}
}

// ---- reviewed, found safe ----

// The guest port has no admin route, whatever the path trick, method or
// peer (even the ingress address).
func TestSecGuestPortHasNoAdminRoutes(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	paths := []string{
		"/api/links", "//api/links", "/g/../api/links", "/g/" + l.Slug + "/../../api/links", "/api%2Flinks",
		"/%2e%2e/api/links", "/api/settings", "//api/settings", "/./api/settings", "/api/../api/settings",
		"/healthz", "/g/" + l.Slug + "/..%2f..%2fapi/links", "/g/" + l.Slug + "/api/links", "/g//api/links",
	}
	for _, peer := range []string{"203.0.113.10:5000", "172.30.32.2:4000"} {
		for _, m := range []string{"GET", "POST", "HEAD", "OPTIONS", "DELETE", "PUT"} {
			for _, p := range paths {
				body := any(nil)
				if m == "POST" || m == "PUT" || m == "DELETE" {
					body = map[string]any{}
				}
				rec := h.g(req{method: m, path: p, body: body, peer: peer})
				out := rec.Body.String()
				if rec.Code == 200 || strings.Contains(out, `"links"`) || strings.Contains(out, `"local_access"`) || strings.Contains(out, l.Slug+`"`) {
					t.Errorf("%s %s from %s: %d %s", m, p, peer, rec.Code, out)
				}
				if loc := rec.Header().Get("Location"); loc != "" && !strings.HasPrefix(loc, "/") {
					t.Errorf("%s %s redirects off site: %q", m, p, loc)
				}
			}
		}
	}
}

// Only the ingress peer reaches the admin port. Headers prove nothing.
func TestSecAdminPeerCheck(t *testing.T) {
	h := newHarness(t)
	spoof := map[string]string{
		"X-Forwarded-For": "172.30.32.2", "X-Real-Ip": "172.30.32.2", "X-Ingress-Path": "/api/hassio_ingress/x",
		"X-Remote-User-Id": "abc", "Forwarded": "for=172.30.32.2",
	}
	for _, peer := range []string{"127.0.0.1:1", "172.30.32.1:1", "172.30.32.3:1", "192.0.2.5:1", "[::1]:1", "[fe80::1]:1", "[2001:db8::1]:1", "bogus", ""} {
		rec := h.do(h.admin, req{method: "GET", path: "/api/settings", peer: peer, headers: spoof})
		if peer == "" {
			continue // the harness fills a default peer
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("peer %q: %d", peer, rec.Code)
		}
	}
	if rec := h.a(req{method: "GET", path: "/api/settings"}); rec.Code != 200 {
		t.Errorf("ingress peer: %d", rec.Code)
	}
}

// A cross-site form can send text/plain, urlencoded or multipart bodies.
// None of them gets through the JSON rule, so none can write.
func TestSecAdminRejectsNonJSONWrites(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/jsonx", "text/json", ""} {
		for _, p := range []string{"/api/links/" + l.ID + "/deactivate", "/api/links/" + l.ID, "/api/templates"} {
			m := "POST"
			if p == "/api/links/"+l.ID {
				m = "DELETE"
			}
			rec := h.a(req{method: m, path: p, body: "{}", ctype: ct, headers: map[string]string{"Content-Type": ct}})
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s %s as %q: %d", m, p, ct, rec.Code)
			}
		}
	}
	if got, _ := h.links.Get(l.ID); got.Inactive {
		t.Fatal("a non-JSON write turned the link off")
	}
}

// Cookie attributes. Secure follows the real scheme: an untrusted peer
// cannot make it lie, and a trusted proxy sets it.
func TestSecCookieFlags(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.TrustedProxies = []string{"192.0.2.50"} })
	l := h.link("guest_room")
	hash, _ := access.HashPassword("pw")
	if _, err := h.links.AddPassword(l.ID, "Anna", hash); err != nil {
		t.Fatal(err)
	}
	login := func(peer string, hdr map[string]string) *http.Cookie {
		rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": "pw"}, peer: peer, headers: hdr})
		c := cookie(rec, sessionCookie)
		if c == nil {
			t.Fatalf("no session cookie: %d %s", rec.Code, rec.Body)
		}
		return c
	}
	c := login("203.0.113.10:1", map[string]string{"X-Forwarded-Proto": "https"})
	if c.Secure {
		t.Error("an untrusted peer set X-Forwarded-Proto and got a Secure cookie")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/g/" || c.Name != access.LinkCookie(sessionCookie, l.ID) {
		t.Errorf("cookie = %+v", c)
	}
	if c := login("192.0.2.50:1", map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"}); !c.Secure {
		t.Error("trusted proxy over https: cookie is not Secure")
	}
	if c := login("192.0.2.50:1", map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "http"}); c.Secure {
		t.Error("trusted proxy over http: cookie is Secure")
	}
}

// The trailing slash redirect repeats the request's own path and never
// leaves the site.
func TestSecTrailingSlashRedirect(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/g/abc/", "/g//evil.example/", "/g/%2F%2Fevil.example/", "/g/%5Cevil.example/", "/g/a%0d%0aSet-Cookie:x=1/", "//evil.example/", "/g/https:%2F%2Fevil.example/"} {
		rec := h.g(req{method: "GET", path: p})
		loc := rec.Header().Get("Location")
		if loc != "" && (!strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") || strings.ContainsAny(loc, "\r\n\\")) {
			t.Errorf("GET %s: Location %q", p, loc)
		}
		if rec.Header().Get("Set-Cookie") != "" {
			t.Errorf("GET %s set a cookie", p)
		}
	}
}

// Every guest response carries the strict CSP, and the page shell has no
// inline script or handler.
func TestSecGuestCSPEverywhere(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	for _, r := range []req{
		{method: "GET", path: "/g/" + l.Slug}, {method: "GET", path: "/g/" + l.Slug + "/state"}, {method: "GET", path: "/g/nope/"},
		{method: "GET", path: "/nothing"}, {method: "POST", path: "/g/" + l.Slug + "/act", body: "x", ctype: "text/plain"},
		{method: "GET", path: "/assets/x/guest.js"},
	} {
		rec := h.g(r)
		if got := rec.Header().Get("Content-Security-Policy"); got != guestCSP || !strings.HasPrefix(got, "default-src 'self'") {
			t.Errorf("%s %s: CSP %q", r.method, r.path, got)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s %s: no nosniff", r.method, r.path)
		}
	}
	page := h.g(req{method: "GET", path: "/g/" + l.Slug}).Body.String()
	for i := 0; ; {
		j := strings.Index(page[i:], "<script")
		if j < 0 {
			break
		}
		end := strings.Index(page[i+j:], ">")
		if tag := page[i+j : i+j+end]; !strings.Contains(tag, " src=") {
			t.Errorf("inline script: %s", tag)
		}
		i += j + end
	}
	for _, bad := range []string{" onclick=", " onload=", " onerror=", "javascript:", "<style"} {
		if strings.Contains(page, bad) {
			t.Errorf("page shell has %q", bad)
		}
	}
}

// A session token is bound to its link, its purpose and its password. It
// dies with the password, the link and the window.
func TestSecSessionReplay(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	l2 := h.link("guest_room")
	hash, _ := access.HashPassword("pw")
	pw1, err := h.links.AddPassword(l.ID, "Anna", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.links.AddPassword(l2.ID, "Anna", hash); err != nil {
		t.Fatal(err)
	}
	rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": "pw"}})
	ses := cookie(rec, sessionCookie)
	if ses == nil {
		t.Fatalf("login: %d", rec.Code)
	}
	call := func(l model.Link, cs ...*http.Cookie) int {
		return h.act(l, "light.guest_room", "light.turn_on", nil, cs...).Code
	}
	if c := call(l, ses); c != 200 {
		t.Fatalf("act with the session: %d", c)
	}
	// Another link, under its own cookie name.
	moved := &http.Cookie{Name: access.LinkCookie(sessionCookie, l2.ID), Value: ses.Value}
	if c := call(l2, moved); c != 401 {
		t.Errorf("session of link 1 on link 2: %d", c)
	}
	// Other purposes under the session name.
	exp := t0.Add(time.Hour)
	sg := h.srv.deps.Signer
	for name, tok := range map[string]string{
		"location": sg.Sign(l.ID, exp), "near": sg.SignNear(l.ID, 1, exp), "device": sg.SignDevice(l.ID, pw1.ID, exp),
	} {
		if c := call(l, &http.Cookie{Name: ses.Name, Value: tok}); c != 401 {
			t.Errorf("%s token as a session: %d", name, c)
		}
	}
	// Removing the password ends its sessions, even when a new password
	// has the same text and the link keeps another password.
	hash2, _ := access.HashPassword("pw")
	if _, err := h.links.AddPassword(l.ID, "Bob", hash2); err != nil {
		t.Fatal(err)
	}
	if rec := h.a(req{method: "DELETE", path: "/api/links/" + l.ID + "/passwords/" + pw1.ID, body: map[string]any{}}); rec.Code != 200 {
		t.Fatalf("remove password: %d", rec.Code)
	}
	if c := call(l, ses); c != 401 {
		t.Errorf("session after its password was removed: %d", c)
	}
	// A rotated slug keeps sessions (by design) but the old slug is dead.
	rec = h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": "pw"}})
	ses = cookie(rec, sessionCookie)
	if rec := h.a(req{method: "POST", path: "/api/links/" + l.ID + "/deactivate", body: map[string]any{}}); rec.Code != 200 {
		t.Fatalf("deactivate: %d", rec.Code)
	}
	if c := call(l, ses); c != 410 {
		t.Errorf("session on an inactive link: %d", c)
	}
	h.advance(48 * time.Hour)
	if c := call(l2, &http.Cookie{Name: access.LinkCookie(sessionCookie, l2.ID), Value: h.srv.deps.Signer.SignSession(l2.ID, "x", t0.Add(time.Hour))}); c == 200 {
		t.Errorf("expired link: %d", c)
	}
}

// Self-service approval stays inside the link. An approved device of link
// A cannot decide a request of link B, by route, by cookie name, or from
// the requester's own browser.
func TestSecSelfServiceApprovalStaysInLink(t *testing.T) {
	h := newHarness(t)
	a := h.approvalLink(model.Approval{SelfService: true})
	b := h.approvalLink(model.Approval{SelfService: true})
	ja, jb, jc := jar{}, jar{}, jar{}
	for _, c := range []struct {
		j jar
		l model.Link
	}{{ja, a}, {jb, b}} {
		if rec := h.dev(c.j, c.l, "POST", "device/request", map[string]any{}); decode[map[string]string](t, rec)["status"] != "approved" {
			t.Fatalf("claim: %s", rec.Body)
		}
	}
	if rec := h.dev(jc, b, "POST", "device/request", map[string]any{}); decode[map[string]string](t, rec)["status"] != "pending" {
		t.Fatalf("request: %s", rec.Body)
	}
	pend := decode[struct {
		Requests []approval.GuestRequest `json:"requests"`
	}](t, h.dev(jb, b, "GET", "device/pending", nil))
	if len(pend.Requests) != 1 {
		t.Fatalf("pending = %+v", pend)
	}
	id := pend.Requests[0].ID
	approve := map[string]any{"request_id": id, "approve": true}

	if rec := h.dev(ja, a, "POST", "device/decide", approve); rec.Code != http.StatusNotFound {
		t.Errorf("A decides B's request: %d %s", rec.Code, rec.Body)
	}
	swapped := &http.Cookie{Name: approval.DeviceCookieName(b.ID), Value: ja[approval.DeviceCookieName(a.ID)].Value}
	if rec := h.g(req{method: "POST", path: "/g/" + b.Slug + "/device/decide", body: approve, cookies: []*http.Cookie{swapped}}); rec.Code != http.StatusForbidden {
		t.Errorf("A's cookie on B: %d %s", rec.Code, rec.Body)
	}
	if rec := h.dev(jc, b, "POST", "device/decide", approve); rec.Code != http.StatusForbidden {
		t.Errorf("requester approves itself: %d %s", rec.Code, rec.Body)
	}
	if rec := h.dev(jc, b, "GET", "device/pending", nil); rec.Code != http.StatusForbidden {
		t.Errorf("requester lists pending: %d", rec.Code)
	}
	if got := h.srv.devices.Pending(b.ID); len(got) != 1 {
		t.Errorf("request is no longer pending: %+v", got)
	}
}

// What a guest can put in the device request event: nothing of the
// User-Agent beyond a fixed word list, no forwarding header text.
func TestSecDeviceRequestEventHasNoGuestText(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.TrustedProxies = []string{"192.0.2.50"} })
	l := h.approvalLink(model.Approval{Manual: true})
	rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/device/request", body: map[string]any{}, peer: "192.0.2.50:1",
		headers: map[string]string{
			"User-Agent":      `Mozilla/5.0 (iPhone) <img src=x onerror=alert(1)> {{7*7}} EVILUA Safari/604`,
			"X-Forwarded-For": "EVILXFF, 203.0.113.9",
		}})
	if rec.Code != 200 {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	ev := h.fake.FiredEvents()
	if len(ev) != 1 {
		t.Fatalf("events = %+v", ev)
	}
	raw, _ := json.Marshal(ev[0].Data)
	for _, bad := range []string{"EVIL", "<", "onerror", "{{"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("event holds %q: %s", bad, raw)
		}
	}
	if ev[0].Data["device"] != "iPhone, Safari" {
		t.Errorf("device = %v", ev[0].Data["device"])
	}
}

// A grant follows HA at once: a removed entity, a moved device, a hidden
// entity and an edited template stop working on the next request.
func TestSecGrantsFollowHA(t *testing.T) {
	registry := func(h *harness, typ string) { h.fake.EmitData(typ, map[string]any{}) }
	try := func(h *harness, l model.Link) int {
		return h.act(l, "light.guest_room", "light.turn_on", nil).Code
	}
	t.Run("entity removed", func(t *testing.T) {
		h := newHarness(t)
		l := h.link("guest_room")
		if c := try(h, l); c != 200 {
			t.Fatal(c)
		}
		h.fake.EntityList = h.fake.EntityList[1:]
		h.fake.StateList = h.fake.StateList[1:]
		registry(h, "entity_registry_updated")
		if c := try(h, l); c != 403 {
			t.Errorf("removed entity: %d", c)
		}
	})
	t.Run("device moved", func(t *testing.T) {
		h := newHarness(t)
		l := h.link("guest_room")
		if c := try(h, l); c != 200 {
			t.Fatal(c)
		}
		h.fake.DeviceList[0].AreaID = "bedroom"
		registry(h, "device_registry_updated")
		if c := try(h, l); c != 403 {
			t.Errorf("moved device: %d", c)
		}
	})
	t.Run("entity hidden", func(t *testing.T) {
		h := newHarness(t)
		l := h.link("guest_room")
		if c := try(h, l); c != 200 {
			t.Fatal(c)
		}
		h.fake.EntityList[0].HiddenBy = "user"
		registry(h, "entity_registry_updated")
		if c := try(h, l); c != 403 {
			t.Errorf("hidden entity: %d", c)
		}
	})
	t.Run("template edited", func(t *testing.T) {
		h := newHarness(t)
		l := h.link("guest_room")
		if c := try(h, l); c != 200 {
			t.Fatal(c)
		}
		ts := testTemplates()
		ts[0].Rules[0].Actions = []string{"light.turn_off"}
		if err := h.tpl.Save(model.Templates{Version: 1, Templates: ts}); err != nil {
			t.Fatal(err)
		}
		if c := try(h, l); c != 403 {
			t.Errorf("edited template: %d", c)
		}
	})
	t.Run("template deleted", func(t *testing.T) {
		h := newHarness(t)
		l := h.link("guest_room")
		if err := h.tpl.Save(model.Templates{Version: 1, Templates: testTemplates()[1:]}); err != nil {
			t.Fatal(err)
		}
		if c := try(h, l); c != 403 {
			t.Errorf("deleted template: %d", c)
		}
	})
}

// syncBuf is a bytes.Buffer safe for the slog handler and the test.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// No slug, password, cookie or coordinate reaches the app log (at debug
// level) or the audit file, over a run that touches every guest route.
func TestSecNoSecretsInLogsOrAudit(t *testing.T) {
	logs := &syncBuf{}
	lg := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	auditPath := filepath.Join(t.TempDir(), audit.FileName)
	al, err := audit.New(audit.Options{Path: auditPath, Enabled: true, Logger: lg})
	if err != nil {
		t.Fatal(err)
	}
	defer al.Close()
	h := newHarness(t, func(_ *config.Options, d *Deps) { d.Logger, d.Audit = lg, al })
	l := h.link("guest_room", "entry")
	const secretPW, wrongPW, missSlug = "hunter2-secret-pw", "wrong-pass-xyz", "missingslugmissingslug"
	hash, _ := access.HashPassword(secretPW)
	if _, err := h.links.AddPassword(l.ID, "Anna", hash); err != nil {
		t.Fatal(err)
	}
	h.g(req{method: "GET", path: "/g/" + l.Slug})
	h.g(req{method: "GET", path: "/g/" + missSlug + "/state"})
	h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": wrongPW}})
	rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": secretPW}})
	ses := cookie(rec, sessionCookie)
	if ses == nil {
		t.Fatalf("login: %d", rec.Code)
	}
	cs := []*http.Cookie{ses}
	h.act(l, "light.guest_room", "light.turn_on", map[string]any{"brightness_pct": 31337}, cs...)
	h.act(l, "light.bedroom", "light.turn_on", nil, cs...)
	h.g(req{method: "POST", path: "/g/" + l.Slug + "/act", body: "{", cookies: cs})
	h.g(req{method: "POST", path: "/g/" + l.Slug + "/act", body: map[string]any{}, origin: "https://evil.example", cookies: cs})
	rec = h.g(req{method: "POST", path: "/g/" + l.Slug + "/where", body: map[string]any{"lat": home.Lat + 0.00123, "lon": home.Lon + 0.00456, "accuracy": 7}, cookies: cs})
	near := cookie(rec, nearCookie)
	if near == nil {
		t.Fatalf("where: %d %s", rec.Code, rec.Body)
	}
	cs = append(cs, near)
	h.act(l, "lock.front_door", "lock.unlock", nil, cs...)
	h.g(req{method: "GET", path: "/g/" + l.Slug + "/state", cookies: cs})
	h.a(req{method: "GET", path: "/api/links"})
	h.a(req{method: "POST", path: "/api/links/" + l.ID + "/passwords", body: map[string]any{"label": "Bob", "password": "bob-secret-pw"}})
	al.Close()

	file, _ := os.ReadFile(auditPath)
	out := logs.String() + string(file)
	if len(file) == 0 {
		t.Fatal("empty audit file")
	}
	for _, secret := range []string{
		l.Slug, secretPW, wrongPW, missSlug, "bob-secret-pw", ses.Value, near.Value, hash, "31337",
		fmt.Sprint(home.Lat + 0.00123), fmt.Sprint(home.Lon + 0.00456), "41.8793", "-87.6253",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("a log or audit line holds %q", secret)
		}
	}
	if st, err := os.Stat(auditPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("audit file: %v %v", st, err)
	}
}

// Slider data stays a number the catalog bounds, and big or odd numbers
// never reach HA.
func TestSecDataSmuggling(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.LimitsEnabled = false })
	l := h.link("guest_room")
	bodies := []string{
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":1e999}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":100.0000000000001}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":50,"Brightness_pct":99,"entity_id":"light.bedroom"}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":{"a":1}}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":[50]}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":"50"}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":9007199254740993}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":50,"area_id":"bedroom"}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"transition":999999}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":50,"color_temp_kelvin":99999}}`,
		`{"entity_id":"light.guest_room ","action":"light.turn_on"}`,
		`{"entity_id":"Light.guest_room","action":"light.turn_on"}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on ","data":null,"data":{"entity_id":"light.bedroom"}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":50}}{"entity_id":"light.bedroom"}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","variables":{"a":1}}`,
		`{"entity_id":"script.let_me_in","action":"script.run","data":{"variables":{"code":"1"}}}`,
	}
	for _, b := range bodies {
		rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/act", body: b})
		if rec.Code == http.StatusOK {
			t.Errorf("accepted: %s", b)
		}
	}
	if n := len(h.fake.ServiceCalls()); n != 0 {
		t.Errorf("%d calls reached HA: %+v", n, h.fake.ServiceCalls())
	}
	// A plain big integer in range is fine and arrives as an int.
	ok := h.g(req{method: "POST", path: "/g/" + l.Slug + "/act",
		body: `{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":50.0}}`})
	calls := h.fake.ServiceCalls()
	if ok.Code != 200 || len(calls) != 1 || calls[0].Data["brightness_pct"] != 50 ||
		len(calls[0].Target.EntityID) != 1 || calls[0].Target.EntityID[0] != "light.guest_room" || len(calls[0].Data) != 1 {
		t.Errorf("ok call: %d %+v", ok.Code, calls)
	}
}

// A valid slug keeps working for a client over its miss budget: limits
// on wrong links never lock out a real guest. Misses past the budget
// still get 429. Short custom slugs get a warning in the admin API
// instead (TestShortCustomSlugWarning).
func TestSecValidSlugWorksOverMissBudget(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	const ip = "203.0.113.77:4000"
	over := 0
	for i := range 14 {
		rec := h.g(req{method: "GET", path: fmt.Sprintf("/g/guess-number-%02d-aaaaaaaa/state", i), peer: ip})
		if rec.Code == http.StatusTooManyRequests {
			over++
		}
	}
	if over == 0 {
		t.Fatal("the miss limiter never answered 429")
	}
	if rec := h.g(req{method: "GET", path: fmt.Sprintf("/g/guess-number-%02d-aaaaaaaa/state", 99), peer: ip}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a miss over budget: %d", rec.Code)
	}
	if rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state", peer: ip}); rec.Code != http.StatusOK {
		t.Fatalf("a valid slug over the miss budget: %d", rec.Code)
	}
}

// The stream cap is per link. One address, or one guest of a link, can open
// every slot and lock the other guests out of live updates. A slow reader
// holds a slot for as long as it likes: there is no write deadline.
func TestSecOneClientCannotTakeEveryStreamSlot(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	var wg sync.WaitGroup
	cancels := []context.CancelFunc{}
	open := func(peer string) {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		r := httptest.NewRequest("GET", "http://guest.test/g/"+l.Slug+"/events", nil).WithContext(ctx)
		r.RemoteAddr = peer
		rec := httptest.NewRecorder()
		wg.Go(func() { h.guest.ServeHTTP(rec, r) })
	}
	// settle waits until the stream count holds still for 300 ms.
	settle := func() int {
		last, since := -1, time.Now()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if n := h.srv.streams.count(l.ID); n != last {
				last, since = n, time.Now()
			} else if time.Since(since) > 300*time.Millisecond {
				break
			}
		}
		return last
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
		wg.Wait()
	}()
	for range 12 {
		open("203.0.113.10:5000")
	}
	taken := settle()
	open("203.0.113.99:5000")
	if after := settle(); after != taken+1 {
		t.Fatalf("one address holds %d of %d stream slots; a second guest got in: %v", taken, h.srv.lim.streams, after == taken+1)
	}
}

// countingHA counts get_states calls to HA.
type countingHA struct {
	*hatest.Fake
	states atomic.Int32
}

func (c *countingHA) States(ctx context.Context) ([]ha.State, error) {
	c.states.Add(1)
	return c.Fake.States(ctx)
}

// Each guest state read asks HA for the state of the whole house. A guest
// can make that call as often as the per-link limit allows, and an
// attacker with many IPv6 addresses as often as the network carries it.
func TestSecStateReadsDoNotFetchEveryState(t *testing.T) {
	var cha *countingHA
	h := newHarness(t, func(o *config.Options, d *Deps) {
		o.LimitsEnabled = false
		cha = &countingHA{Fake: fakeHA()}
		inv := inventory.New(cha, quietLog)
		if err := inv.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
		d.Inventory = inv
	})
	l := h.link("guest_room")
	for range 40 {
		if rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"}); rec.Code != 200 {
			t.Fatalf("state: %d", rec.Code)
		}
	}
	if n := cha.states.Load(); n > 5 {
		t.Fatalf("40 state reads made %d get_states calls to HA", n)
	}
}
