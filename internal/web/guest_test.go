package web

import (
	"encoding/json"
	"fmt"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// Every guest response carries the security headers, whatever its status.
func TestGuestHeadersOnEveryResponse(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	for _, r := range []req{
		{method: "GET", path: "/g/" + l.Slug},
		{method: "GET", path: "/g/" + l.Slug + "/state"},
		{method: "GET", path: "/g/nope-nope-nope/state"},
		{method: "GET", path: "/assets/" + h.srv.assets.version + "/guest.js"},
		{method: "GET", path: "/assets/old/guest.js"},
		{method: "GET", path: "/"},
		{method: "GET", path: "/api/links"},
		{method: "PUT", path: "/g/" + l.Slug + "/act", body: map[string]any{}},
		{method: "POST", path: "/g/" + l.Slug + "/act", body: "x", ctype: "text/plain"},
	} {
		rec := h.g(r)
		hd := rec.Header()
		if csp := hd.Get("Content-Security-Policy"); csp != guestCSP || strings.Contains(csp, "unsafe-inline") {
			t.Errorf("%s %s: CSP %q", r.method, r.path, csp)
		}
		for k, want := range map[string]string{"Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY"} {
			if hd.Get(k) != want {
				t.Errorf("%s %s: %s = %q", r.method, r.path, k, hd.Get(k))
			}
		}
		cc := hd.Get("Cache-Control")
		if strings.HasPrefix(r.path, "/assets/"+h.srv.assets.version) && rec.Code == 200 {
			if cc != assetCache {
				t.Errorf("asset Cache-Control = %q", cc)
			}
		} else if cc != "no-store" {
			t.Errorf("%s %s (%d): Cache-Control %q", r.method, r.path, rec.Code, cc)
		}
	}
}

func TestGuestPageAndAssets(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	rec := h.g(req{method: "GET", path: "/g/" + l.Slug})
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "../assets/"+h.srv.assets.version+"/guest.js") || strings.Contains(body, "{{ASSETS}}") {
		t.Fatalf("page: %d %s", rec.Code, body)
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "style=") || strings.Contains(body, "<style") {
		t.Error("page has inline script or style")
	}
	for name, ct := range map[string]string{"guest.js": "text/javascript", "lib.js": "text/javascript", "guest.css": "text/css"} {
		rec := h.g(req{method: "GET", path: "/assets/" + h.srv.assets.version + "/" + name})
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), ct) {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	for _, p := range []string{"/assets/" + h.srv.assets.version + "/index.html", "/assets/" + h.srv.assets.version + "/lib_test.mjs", "/assets/x/guest.js", "/assets/" + h.srv.assets.version + "/../guest.js"} {
		if rec := h.g(req{method: "GET", path: p}); rec.Code == 200 {
			t.Errorf("%s served", p)
		}
	}
}

// The guest port has no admin routes, the health check included.
func TestGuestHasNoAdminRoutes(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.RateLimits.GuestBurst = 1000 })
	for _, p := range []string{"/", "/healthz", "/api/links", "/api/settings", "/api/diagnostics", "/index.html", "/admin.css"} {
		for _, m := range []string{"GET", "POST", "DELETE"} {
			if rec := h.g(req{method: m, path: p, body: map[string]any{}}); rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d", m, p, rec.Code)
			}
		}
	}
}

func TestGuestStateFiltersEntitiesAndAttributes(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room", "entry")
	rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"})
	if rec.Code != 200 {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, leak := range []string{"light.bedroom", "secret-user", "camera_proxy", "user_id", "changed_by", "code_format", "lock.guest_room_window"} {
		if strings.Contains(body, leak) {
			t.Errorf("state leaks %q: %s", leak, body)
		}
	}
	v := decode[stateView](t, rec)
	if v.Entities["light.guest_room"] == nil || v.States["light.guest_room"].Attributes["friendly_name"] != "Guest lamp" {
		t.Errorf("state = %+v", v)
	}
	// The entry needs location: shown, marked, no state.
	door := v.Entities["lock.front_door"]
	if door == nil || len(door.Actions) != 2 || !door.Actions[0].NeedsLocation {
		t.Errorf("front door = %+v", door)
	}
	if _, ok := v.States["lock.front_door"]; ok {
		t.Error("state of a location-gated entity is shown")
	}
	if len(v.Groups) != 2 || v.Groups[0].ID != "guest_room" || v.Groups[1].ID != "entry" {
		t.Errorf("groups = %+v", v.Groups)
	}
}

// A local-only rule is left out of /state entirely while it does not pass.
func TestGuestStateOmitsNetworkDenied(t *testing.T) {
	h := newHarness(t)
	l := h.link("office")
	v := decode[stateView](t, h.g(req{method: "GET", path: "/g/" + l.Slug + "/state", peer: "192.168.1.5:1000"}))
	if len(v.Entities) != 0 || len(v.Groups) != 0 {
		t.Errorf("local off: %+v", v)
	}
}

func TestGuestActCallsHAWithOneEntity(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	rec := h.act(l, "light.guest_room", "light.turn_on", map[string]any{"brightness_pct": 40})
	if rec.Code != 200 {
		t.Fatalf("act: %d %s", rec.Code, rec.Body)
	}
	calls := h.fake.ServiceCalls()
	want := []string{"light.guest_room"}
	if len(calls) != 1 || calls[0].Domain != "light" || calls[0].Service != "turn_on" ||
		!reflect.DeepEqual(calls[0].Target, &ha.Target{EntityID: want}) || calls[0].Data["brightness_pct"] != 40 {
		t.Fatalf("calls = %+v", calls)
	}
	ev := h.fake.FiredEvents()
	if len(ev) != 1 || ev[0].EventType != eventAction || ev[0].Data["entity_id"] != "light.guest_room" ||
		ev[0].Data["link_id"] != l.ID || ev[0].Data["ip"] != "203.0.113.10" || ev[0].Data["guest"] != "" {
		t.Errorf("events = %+v", ev)
	}
}

// Every escape row from authz, over HTTP. None reaches HA.
func TestGuestActEscapes(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	for _, tc := range []struct {
		name string
		body any
		want int
	}{
		{"another entity", map[string]any{"entity_id": "light.bedroom", "action": "light.turn_on"}, 403},
		{"unknown entity", map[string]any{"entity_id": "light.nope", "action": "light.turn_on"}, 403},
		{"in the area, wrong domain", map[string]any{"entity_id": "lock.guest_room_window", "action": "lock.unlock"}, 403},
		{"action not granted", map[string]any{"entity_id": "light.guest_room", "action": "light.toggle"}, 403},
		{"action not in catalog", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on_all"}, 403},
		{"cross-domain action", map[string]any{"entity_id": "light.guest_room", "action": "switch.turn_on"}, 403},
		{"upper case", map[string]any{"entity_id": "Light.Guest_Room", "action": "light.turn_on"}, 403},
		{"space", map[string]any{"entity_id": " light.guest_room", "action": "light.turn_on"}, 403},
		{"trailing newline", map[string]any{"entity_id": "light.guest_room\n", "action": "light.turn_on"}, 403},
		{"entity list", map[string]any{"entity_id": []string{"light.guest_room", "light.bedroom"}, "action": "light.turn_on"}, 400},
		{"comma list", map[string]any{"entity_id": "light.guest_room,light.bedroom", "action": "light.turn_on"}, 403},
		{"extra data key", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "data": map[string]any{"flash": "long"}}, 400},
		{"area target in data", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "data": map[string]any{"area_id": "bedroom"}}, 400},
		{"entity target in data", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "data": map[string]any{"entity_id": "light.bedroom"}}, 400},
		{"out of range", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "data": map[string]any{"brightness_pct": 101}}, 400},
		{"color temp beyond the light", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "data": map[string]any{"color_temp_kelvin": 1800}}, 400},
		{"string number", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "data": map[string]any{"brightness_pct": "40"}}, 400},
		{"unknown top-level key", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "target": map[string]any{"area_id": "bedroom"}}, 400},
		{"trailing data", `{"entity_id":"light.guest_room","action":"light.turn_on"}{}`, 400},
		{"not JSON", `entity_id=light.guest_room`, 400},
		{"too large", `{"entity_id":"` + strings.Repeat("a", 20<<10) + `"}`, 413},
	} {
		rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/act", body: tc.body})
		if rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
		if b := rec.Body.String(); strings.Contains(b, "bedroom") || strings.Contains(b, "flash") || strings.Contains(b, "area_id") {
			t.Errorf("%s: reply echoes input: %s", tc.name, b)
		}
	}
	if calls := h.fake.ServiceCalls(); len(calls) != 0 {
		t.Errorf("escapes reached HA: %+v", calls)
	}
}

func TestGuestPostChecks(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	body := map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}
	for _, tc := range []struct {
		name   string
		r      req
		status int
	}{
		{"no origin", req{origin: "-"}, 403},
		{"null origin", req{origin: "null"}, 403},
		{"other origin", req{origin: "https://evil.test"}, 403},
		{"origin with other port", req{origin: "http://guest.test:8080"}, 403},
		{"text/plain", req{ctype: "text/plain"}, 415},
		{"form", req{ctype: "application/x-www-form-urlencoded"}, 415},
		{"json with charset", req{ctype: "application/json; charset=utf-8"}, 200},
	} {
		r := tc.r
		r.method, r.path, r.body = "POST", "/g/"+l.Slug+"/act", body
		if rec := h.g(r); rec.Code != tc.status {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.status)
		}
	}
}

func TestGuestLinkWindow(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	sched, _ := h.links.Create(model.Link{Name: "Later", ValidFrom: t0.Add(time.Hour), ValidUntil: t0.Add(2 * time.Hour), TemplateIDs: []string{"guest_room"}}, "")
	revoked := h.link("guest_room")
	if err := h.links.Revoke(revoked.ID); err != nil {
		t.Fatal(err)
	}
	for name, slug := range map[string]string{"scheduled": sched.Slug, "revoked": revoked.Slug} {
		if rec := h.act(model.Link{Slug: slug}, "light.guest_room", "light.turn_on", nil); rec.Code != http.StatusGone {
			t.Errorf("%s act: %d", name, rec.Code)
		}
		if rec := h.g(req{method: "GET", path: "/g/" + slug + "/state"}); rec.Code != http.StatusGone {
			t.Errorf("%s state: %d", name, rec.Code)
		}
		// The page still loads, so it can say the link is not active.
		if rec := h.g(req{method: "GET", path: "/g/" + slug}); rec.Code != 200 {
			t.Errorf("%s page: %d", name, rec.Code)
		}
	}
	h.advance(24 * time.Hour)
	if rec := h.act(l, "light.guest_room", "light.turn_on", nil); rec.Code != http.StatusGone {
		t.Errorf("expired: %d", rec.Code)
	}
	if n := len(h.fake.ServiceCalls()); n != 0 {
		t.Errorf("%d calls reached HA", n)
	}
}

func TestGuestOneTime(t *testing.T) {
	h := newHarness(t)
	l, _ := h.links.Create(model.Link{Name: "Once", ValidUntil: t0.Add(time.Hour), TemplateIDs: []string{"guest_room"}, OneTime: true}, "")
	if rec := h.act(l, "light.guest_room", "light.turn_on", nil); rec.Code != 200 {
		t.Fatalf("first: %d", rec.Code)
	}
	if rec := h.act(l, "light.guest_room", "light.turn_on", nil); rec.Code != http.StatusConflict {
		t.Errorf("second: %d", rec.Code)
	}
	// Another pair of the same link still works.
	if rec := h.act(l, "light.guest_room", "light.turn_off", nil); rec.Code != 200 {
		t.Errorf("other action: %d", rec.Code)
	}
	v := decode[stateView](t, h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"}))
	if acts := v.Entities["light.guest_room"].Actions; !acts[0].Used || !acts[1].Used {
		t.Errorf("used flags = %+v", acts)
	}
}

// Of many parallel requests for one one-time pair, exactly one reaches HA.
func TestGuestOneTimeConcurrent(t *testing.T) {
	h := newHarness(t)
	l, _ := h.links.Create(model.Link{Name: "Once", ValidUntil: t0.Add(time.Hour), TemplateIDs: []string{"guest_room"}, OneTime: true}, "")
	const n = 20
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { codes <- h.act(l, "light.guest_room", "light.turn_on", nil).Code })
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == 200 {
			ok++
		} else if c != http.StatusConflict {
			t.Errorf("status %d", c)
		}
	}
	if calls := h.fake.ServiceCalls(); ok != 1 || len(calls) != 1 {
		t.Errorf("%d successes, %d HA calls", ok, len(calls))
	}
}

func TestGuestRefundOnHAError(t *testing.T) {
	h := newHarness(t)
	var fail sync.Mutex
	failing := true
	h.fake.Respond = func(hatest.ServiceCall) (json.RawMessage, error) {
		fail.Lock()
		defer fail.Unlock()
		if failing {
			return nil, &ha.Error{Code: "home_assistant_error", Message: "secret detail"}
		}
		return nil, nil
	}
	l, _ := h.links.Create(model.Link{Name: "Once", ValidUntil: t0.Add(time.Hour), TemplateIDs: []string{"guest_room"}, OneTime: true}, "")
	rec := h.act(l, "light.guest_room", "light.turn_on", nil)
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("HA error: %d %s", rec.Code, rec.Body)
	}
	if got, _ := h.links.Get(l.ID); len(got.Used) != 0 || got.UseCount != 0 {
		t.Errorf("not refunded: %+v", got)
	}
	if ev := h.fake.FiredEvents(); len(ev) != 0 {
		t.Errorf("event fired for a failed call: %+v", ev)
	}
	fail.Lock()
	failing = false
	fail.Unlock()
	if rec := h.act(l, "light.guest_room", "light.turn_on", nil); rec.Code != 200 {
		t.Errorf("retry after refund: %d", rec.Code)
	}
}

func TestGuestScriptVariables(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.LocalAccess.Enabled = true })
	l := h.link("entry")
	rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/act", peer: "192.168.1.5:1000",
		body: map[string]any{"entity_id": "script.let_me_in", "action": "script.run"}})
	if rec.Code != 200 {
		t.Fatalf("script: %d %s", rec.Code, rec.Body)
	}
	c := h.fake.ServiceCalls()[0]
	vars, _ := c.Data["variables"].(map[string]any)
	if c.Service != "turn_on" || vars["guest_link"] != "Anna's visit" || vars["source"] != "guest_access" {
		t.Errorf("call = %+v", c)
	}
}

func TestGuestNetworkLocal(t *testing.T) {
	trustProxy := func(o *config.Options, _ *Deps) { o.Settings.TrustedProxies = []string{"172.30.33.5"} }
	on := func(o *config.Options, _ *Deps) { o.Settings.LocalAccess.Enabled = true }
	for _, tc := range []struct {
		name    string
		opts    []harnessOpt
		peer    string
		headers map[string]string
		want    int
	}{
		{"off, LAN peer", nil, "192.168.1.5:1000", nil, 403},
		{"on, LAN peer", []harnessOpt{on}, "192.168.1.5:1000", nil, 200},
		{"on, internet peer", []harnessOpt{on}, "203.0.113.9:1000", nil, 403},
		{"on, untrusted peer claims LAN", []harnessOpt{on}, "203.0.113.9:1000", map[string]string{"X-Forwarded-For": "192.168.1.5"}, 403},
		{"on, trusted proxy without XFF", []harnessOpt{on, trustProxy}, "172.30.33.5:1000", nil, 403},
		{"on, trusted proxy with garbage XFF", []harnessOpt{on, trustProxy}, "172.30.33.5:1000", map[string]string{"X-Forwarded-For": "garbage"}, 403},
		{"on, trusted proxy, internet client", []harnessOpt{on, trustProxy}, "172.30.33.5:1000", map[string]string{"X-Forwarded-For": "203.0.113.9"}, 403},
		{"on, trusted proxy, LAN client", []harnessOpt{on, trustProxy}, "172.30.33.5:1000", map[string]string{"X-Forwarded-For": "192.168.1.5"}, 200},
	} {
		h := newHarness(t, tc.opts...)
		l := h.link("office")
		rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/act", peer: tc.peer, headers: tc.headers,
			body: map[string]any{"entity_id": "switch.office", "action": "switch.turn_on"}})
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

func TestGuestNearHome(t *testing.T) {
	h := newHarness(t)
	l := h.link("entry")
	other := h.link("entry")
	unlock := func(c ...*http.Cookie) int { return h.act(l, "lock.front_door", "lock.unlock", nil, c...).Code }
	where := func(link model.Link, lat, lon, acc float64) *httptest.ResponseRecorder {
		return h.g(req{method: "POST", path: "/g/" + link.Slug + "/where", body: map[string]any{"lat": lat, "lon": lon, "accuracy": acc}})
	}
	if c := unlock(); c != 403 {
		t.Errorf("no cookie: %d", c)
	}
	if c := unlock(&http.Cookie{Name: access.LinkCookie(nearCookie, l.ID), Value: "AQMAAAAAAAAAAAAAAAA"}); c != 403 {
		t.Errorf("forged cookie: %d", c)
	}
	// A real token for another link does not work here.
	signer := access.NewSigner([]byte(strings.Repeat("\x07", 32)))
	if c := unlock(&http.Cookie{Name: access.LinkCookie(nearCookie, l.ID), Value: signer.SignNear(other.ID, 1, t0.Add(time.Minute))}); c != 403 {
		t.Errorf("other link's cookie: %d", c)
	}
	// 0.01 degrees of latitude is about 1.1 km.
	far := where(l, home.Lat+0.01, home.Lon, 10)
	if far.Code != 200 || strings.Contains(far.Body.String(), "distance") {
		t.Fatalf("far where: %d %s", far.Code, far.Body)
	}
	if c := unlock(cookie(far, nearCookie)); c != 403 {
		t.Errorf("valid cookie, too far: %d", c)
	}
	// Close, but the accuracy pushes it past 300 m.
	if c := unlock(cookie(where(l, home.Lat+0.001, home.Lon, 250), nearCookie)); c != 403 {
		t.Errorf("near with poor accuracy: %d", c)
	}
	for _, bad := range []map[string]any{
		{"lat": home.Lat, "lon": home.Lon, "accuracy": 1001},
		{"lat": home.Lat, "lon": home.Lon, "accuracy": -1},
		{"lat": 91, "lon": home.Lon, "accuracy": 5},
		{"lat": home.Lat, "lon": 181, "accuracy": 5},
		{"lat": home.Lat, "lon": home.Lon},
		{"lat": home.Lat, "lon": home.Lon, "accuracy": 5, "alt": 3},
	} {
		if rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/where", body: bad}); rec.Code != 400 || cookie(rec, nearCookie) != nil {
			t.Errorf("where %v: %d", bad, rec.Code)
		}
	}
	near := where(l, home.Lat+0.001, home.Lon, 20) // about 111 m + 20
	c := cookie(near, nearCookie)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != access.GuestCookiePath || c.MaxAge != 900 || c.Secure {
		t.Fatalf("near cookie = %+v", c)
	}
	if code := unlock(c); code != 200 {
		t.Errorf("near: %d", code)
	}
	v := decode[stateView](t, h.g(req{method: "GET", path: "/g/" + l.Slug + "/state", cookies: []*http.Cookie{c}}))
	if a := v.Entities["lock.front_door"].Actions[0]; a.NeedsLocation {
		t.Error("still needs location with a valid cookie")
	}
	h.advance(15 * time.Minute)
	if code := unlock(c); code != 403 {
		t.Errorf("expired cookie: %d", code)
	}
}

// A local client counts as near home only while local access is on.
func TestGuestNearHomeLocal(t *testing.T) {
	for _, on := range []bool{false, true} {
		h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.LocalAccess.Enabled = on })
		l := h.link("entry")
		rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/act", peer: "192.168.1.5:1000",
			body: map[string]any{"entity_id": "lock.front_door", "action": "lock.unlock"}})
		want := map[bool]int{false: 403, true: 200}[on]
		if rec.Code != want {
			t.Errorf("local access %v: %d, want %d", on, rec.Code, want)
		}
	}
}

func TestGuestPasswords(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	hash, _ := access.HashPassword("+1 555 0100")
	pw, _ := h.links.AddPassword(l.ID, "Anna", hash)
	other, _ := access.HashPassword("other")
	h.links.AddPassword(l.ID, "Carl", other) // so removing Anna's leaves the link locked
	l2 := h.link("guest_room")
	h.links.AddPassword(l2.ID, "Bob", hash)
	state := func(link model.Link, c ...*http.Cookie) *httptest.ResponseRecorder {
		return h.g(req{method: "GET", path: "/g/" + link.Slug + "/state", cookies: c})
	}
	if rec := state(l); rec.Code != 401 || !strings.Contains(rec.Body.String(), `"need":"password"`) {
		t.Errorf("no cookie: %d %s", rec.Code, rec.Body)
	}
	if rec := h.act(l, "light.guest_room", "light.turn_on", nil); rec.Code != 401 {
		t.Errorf("act without session: %d", rec.Code)
	}
	// The page loads without a session.
	if rec := h.g(req{method: "GET", path: "/g/" + l.Slug}); rec.Code != 200 {
		t.Errorf("page: %d", rec.Code)
	}
	login := func(link model.Link, p string, extra ...func(*req)) *httptest.ResponseRecorder {
		r := req{method: "POST", path: "/g/" + link.Slug + "/password", body: map[string]any{"password": p}}
		for _, f := range extra {
			f(&r)
		}
		return h.g(r)
	}
	for _, wrong := range []string{"+15550100", "+1 555 0100 ", " +1 555 0100", "+1 555 010"} {
		if rec := login(l, wrong); rec.Code != 403 || cookie(rec, sessionCookie) != nil {
			t.Errorf("wrong %q: %d", wrong, rec.Code)
		}
	}
	rec := login(l, "+1 555 0100", func(r *req) { r.peer = "203.0.113.11:1" })
	c := cookie(rec, sessionCookie)
	if rec.Code != 200 || c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != access.GuestCookiePath {
		t.Fatalf("login: %d %+v", rec.Code, c)
	}
	if rec := state(l, c); rec.Code != 200 {
		t.Errorf("with session: %d", rec.Code)
	}
	if rec := h.act(l, "light.guest_room", "light.turn_on", nil, c); rec.Code != 200 {
		t.Errorf("act with session: %d", rec.Code)
	}
	if ev := h.fake.FiredEvents(); len(ev) != 1 || ev[0].Data["guest"] != "Anna" {
		t.Errorf("event label: %+v", ev)
	}
	for name, bad := range map[string]*http.Cookie{
		"forged":       {Name: access.LinkCookie(sessionCookie, l.ID), Value: "AQIAAAAAAAAAAAAAAAAA"},
		"tampered":     {Name: access.LinkCookie(sessionCookie, l.ID), Value: c.Value[:20] + flip(c.Value[20]) + c.Value[21:]},
		"near as auth": {Name: access.LinkCookie(sessionCookie, l.ID), Value: h.srv.deps.Signer.SignNear(l.ID, 1, t0.Add(time.Hour))},
	} {
		if rec := state(l, bad); rec.Code != 401 {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	// A session of link A does not open link B, with the same password.
	if rec := state(l2, &http.Cookie{Name: access.LinkCookie(sessionCookie, l2.ID), Value: c.Value}); rec.Code != 401 {
		t.Errorf("cookie of A on B: %d", rec.Code)
	}
	// Deleting the password ends its session at once.
	if err := h.links.RemovePassword(l.ID, pw.ID); err != nil {
		t.Fatal(err)
	}
	if rec := state(l, c); rec.Code != 401 {
		t.Errorf("after removal: %d", rec.Code)
	}
}

func TestGuestPasswordSecureCookieOverHTTPS(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.TrustedProxies = []string{"172.30.33.5"} })
	l := h.link("guest_room")
	hash, _ := access.HashPassword("pw")
	h.links.AddPassword(l.ID, "Anna", hash)
	rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": "pw"},
		peer: "172.30.33.5:1", headers: map[string]string{"X-Forwarded-For": "203.0.113.4", "X-Forwarded-Proto": "https"}})
	if c := cookie(rec, sessionCookie); c == nil || !c.Secure {
		t.Errorf("cookie over https = %+v", c)
	}
}

func TestGuestPasswordLockout(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	hash, _ := access.HashPassword("right")
	h.links.AddPassword(l.ID, "Anna", hash)
	try := func(p string) int {
		return h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": p}}).Code
	}
	for i := range 5 {
		if c := try(fmt.Sprintf("wrong%d", i)); c != 403 {
			t.Fatalf("fail %d: %d", i, c)
		}
	}
	rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": "right"}})
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("locked: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	ev := h.fake.FiredEvents()
	if len(ev) != 1 || ev[0].EventType != eventLockout || ev[0].Data["link_id"] != l.ID || ev[0].Data["scope"] != lockoutScopeIP {
		t.Errorf("lockout events = %+v", ev)
	}
}

func TestGuestSlugMisses(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) {
		o.Settings.RateLimits.MissesPerHourPerIP = 3
		o.Settings.RateLimits.MissesPerHourGlobal = 5
		o.Settings.RateLimits.GuestBurst = 100
		o.Settings.RateLimits.GuestPerMin = 1000
	})
	l := h.link("guest_room")
	miss := func(peer string) *httptest.ResponseRecorder {
		return h.g(req{method: "GET", path: "/g/no-such-link-here/state", peer: peer})
	}
	for i := range 3 {
		if c := miss("203.0.113.1:1").Code; c != 404 {
			t.Fatalf("miss %d: %d", i, c)
		}
	}
	if rec := miss("203.0.113.1:1"); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("over per-IP: %d", rec.Code)
	}
	// Two more from another IP reach the global cap of 5.
	miss("203.0.113.2:1")
	miss("203.0.113.2:1")
	if c := miss("203.0.113.3:1").Code; c != 429 {
		t.Errorf("over global cap: %d", c)
	}
	// A valid slug still works for everyone.
	if c := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state", peer: "203.0.113.3:1"}).Code; c != 200 {
		t.Errorf("valid slug under the cap: %d", c)
	}
}

func TestGuestPreauthLimit(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.RateLimits.GuestBurst = 3 })
	l := h.link("guest_room")
	for i := range 3 {
		if c := h.g(req{method: "GET", path: "/g/" + l.Slug}).Code; c != 200 {
			t.Fatalf("page %d: %d", i, c)
		}
	}
	rec := h.g(req{method: "GET", path: "/g/" + l.Slug})
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "3" {
		t.Errorf("over: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// An authenticated guest uses its own limiter, and so do files.
	if c := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"}).Code; c != 200 {
		t.Errorf("state after pre-auth limit: %d", c)
	}
	if c := h.g(req{method: "GET", path: "/assets/" + h.srv.assets.version + "/guest.js"}).Code; c != 200 {
		t.Errorf("file after pre-auth limit: %d", c)
	}
	if c := h.g(req{method: "GET", path: "/g/" + l.Slug, peer: "203.0.113.99:1"}).Code; c != 200 {
		t.Errorf("another IP: %d", c)
	}
}

func TestGuestActionLimit(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.RateLimits.ActionsBurst = 2 })
	l := h.link("guest_room")
	for range 2 {
		h.act(l, "light.guest_room", "light.turn_off", nil)
	}
	if rec := h.act(l, "light.guest_room", "light.turn_off", nil); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("over: %d", rec.Code)
	}
}

// With rate_limits.enabled false, no limiter ever blocks.
func TestGuestLimitsOff(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) {
		o.LimitsEnabled = false
		o.Settings.RateLimits = model.RateLimits{GuestPerMin: 1, GuestBurst: 1, ActionsPerMin: 1, ActionsBurst: 1,
			MaxStreamsPerLink: 1, MissesPerHourPerIP: 1, MissesPerHourGlobal: 1, PasswordFailsPerIP: 1,
			PasswordFailsPerLink: 1, PasswordLockoutMin: 60, AdminPerMin: 1}
	})
	l := h.link("guest_room")
	locked := h.link("guest_room")
	hash, _ := access.HashPassword("right")
	h.links.AddPassword(locked.ID, "Anna", hash)
	for i := range 10 {
		for _, r := range []req{
			{method: "GET", path: "/g/" + l.Slug},
			{method: "GET", path: "/g/missing-slug-xx/state"},
			{method: "POST", path: "/g/" + l.Slug + "/act", body: map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}},
			{method: "POST", path: "/g/" + locked.Slug + "/password", body: map[string]any{"password": "wrong"}},
		} {
			if rec := h.g(r); rec.Code == 429 {
				t.Fatalf("round %d %s %s: 429", i, r.method, r.path)
			}
		}
		if rec := h.a(req{method: "GET", path: "/api/links"}); rec.Code == 429 {
			t.Fatalf("admin round %d: 429", i)
		}
	}
	if len(h.fake.FiredEvents()) != 10 { // ten actions, no lockout
		t.Errorf("events = %d", len(h.fake.FiredEvents()))
	}
	if rec := h.g(req{method: "POST", path: "/g/" + locked.Slug + "/password", body: map[string]any{"password": "right"}}); rec.Code != 200 {
		t.Errorf("right password after many fails: %d", rec.Code)
	}
}

func TestGuestParty(t *testing.T) {
	party := func(h *harness, l model.Link) (bool, int) {
		v := decode[stateView](t, h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"}))
		return v.Party, h.g(req{method: "GET", path: "/g/" + l.Slug + "/party"}).Code
	}
	// No MA set up.
	h := newHarness(t)
	if p, code := party(h, h.link("music")); p || code != 404 {
		t.Errorf("no MA: %v %d", p, code)
	}
	// MA set up, link without a party template.
	h = newHarness(t, withMA(stubMA{url: "https://ma.example/party?code=x"}))
	if p, code := party(h, h.link("guest_room")); p || code != 404 {
		t.Errorf("no party template: %v %d", p, code)
	}
	l := h.link("music")
	if p, code := party(h, l); !p || code != 200 {
		t.Errorf("party: %v %d", p, code)
	}
	if u := decode[map[string]string](t, h.g(req{method: "GET", path: "/g/" + l.Slug + "/party"}))["url"]; u != "https://ma.example/party?code=x" {
		t.Errorf("url = %q", u)
	}
	// MA times out: no URL, and quickly.
	h = newHarness(t, withMA(stubMA{block: true}), func(o *config.Options, _ *Deps) { o.MATimeout = 50 * time.Millisecond })
	start := time.Now()
	if _, code := party(h, h.link("music")); code != 503 || time.Since(start) > 2*time.Second {
		t.Errorf("timeout: %d after %v", code, time.Since(start))
	}
}

func TestGuestStreamCap(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.RateLimits.MaxStreamsPerLink = 1 })
	l := h.link("guest_room")
	h.srv.streams.acquire(l.ID, "198.51.100.1", 1, 0)
	rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/events"})
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("over the stream cap: %d", rec.Code)
	}
}

func TestRouteLabelHidesSlug(t *testing.T) {
	for path, want := range map[string]string{
		"/g/secret":             "g/page",
		"/g/secret/":            "g/page",
		"/g/secret/state":       "g/state",
		"/g/secret/whatever":    "g/other",
		"/g/secret/a/b":         "g/other",
		"/assets/abc/x.js":      "assets",
		"/something/else":       "other",
		"/api/links/abc/qr.svg": "/api/links/abc/qr.svg",
	} {
		if got := routeLabel(path); got != want || strings.Contains(got, "secret") {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

// flip returns a base64url character other than c.
func flip(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

// /g/{slug}/ redirects to /g/{slug} with no body, so the page's relative
// asset links work.
func TestGuestTrailingSlashRedirects(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/"})
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/g/"+l.Slug || rec.Body.Len() != 0 {
		t.Errorf("redirect: %d %q %q", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if rec := h.g(req{method: "GET", path: "/g/" + l.Slug}); rec.Code != 200 {
		t.Errorf("page: %d", rec.Code)
	}
}

// /state carries the slider interval from the app options.
func TestStateSliderInterval(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.Guest.SliderIntervalMS = 250 })
	l := h.link("guest_room")
	v := decode[stateView](t, h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"}))
	if v.Guest.SliderIntervalMS != 250 {
		t.Errorf("slider interval = %d", v.Guest.SliderIntervalMS)
	}
	d := newHarness(t)
	rec := d.g(req{method: "GET", path: "/g/" + d.link("guest_room").Slug + "/state"})
	if !strings.Contains(rec.Body.String(), `"slider_interval_ms":300`) {
		t.Errorf("default not in /state: %s", rec.Body)
	}
}
