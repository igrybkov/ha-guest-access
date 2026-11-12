package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// inactiveRoutes is every guest route with a body that would pass on an
// active link. The page itself is checked apart.
var inactiveRoutes = []struct {
	method, route string
	body          any
}{
	{"POST", "password", map[string]any{"password": "right"}},
	{"GET", "state", nil},
	{"GET", "events", nil},
	{"GET", "party", nil},
	{"POST", "act", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}},
	{"POST", "where", map[string]any{"lat": 0, "lon": 0, "accuracy": 5}},
	{"POST", "device/request", map[string]any{}},
	{"GET", "device/status", nil},
	{"GET", "device/pending", nil},
	{"POST", "device/decide", map[string]any{"request": "x", "approve": true}},
}

// linkBody is the admin edit body for l, with inactive set.
func linkBody(l model.Link, inactive bool) map[string]any {
	b := map[string]any{
		"name": l.Name, "templates": l.TemplateIDs, "one_time": l.OneTime,
		"approval": map[string]any{"manual": l.Approval.Manual, "self_service": l.Approval.SelfService},
		"inactive": inactive,
	}
	if !l.ValidFrom.IsZero() {
		b["valid_from"] = l.ValidFrom
	}
	if !l.ValidUntil.IsZero() {
		b["valid_until"] = l.ValidUntil
	}
	return b
}

// An inactive link inside its window acts like a link outside it. Every
// guest route but the page answers 410, even with a good session and an
// approved device. Nothing reaches HA, no use is taken, no device or
// request is added, and no password attempt counts. Turning the link on
// again brings back the same session and device.
func TestGuestInactiveLink(t *testing.T) {
	h := newHarness(t, withLinkFails(3))
	l, err := h.links.Create(model.Link{
		Name: "Anna's visit", ValidFrom: t0.Add(-time.Hour), ValidUntil: t0.Add(24 * time.Hour),
		TemplateIDs: []string{"guest_room"}, Approval: model.Approval{Manual: true}, OneTime: true,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := access.HashPassword("right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.links.AddPassword(l.ID, "Anna", hash); err != nil {
		t.Fatal(err)
	}
	j := jar{}
	if rec := h.dev(j, l, "POST", "password", map[string]any{"password": "right"}); rec.Code != 200 {
		t.Fatalf("password: %d %s", rec.Code, rec.Body)
	}
	h.dev(j, l, "POST", "device/request", map[string]any{})
	h.approveNext(l, true)
	h.dev(j, l, "GET", "device/status", nil)
	if rec := h.dev(j, l, "POST", "act", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}); rec.Code != 200 {
		t.Fatalf("act: %d %s", rec.Code, rec.Body)
	}
	// A second browser waits for approval.
	other := jar{}
	if rec := h.dev(other, l, "POST", "password", map[string]any{"password": "right"}); rec.Code != 200 {
		t.Fatalf("other password: %d", rec.Code)
	}
	h.dev(other, l, "POST", "device/request", map[string]any{})

	l, _ = h.links.Get(l.ID)
	if rec := h.a(req{method: "PUT", path: "/api/links/" + l.ID, body: linkBody(l, true)}); rec.Code != 200 {
		t.Fatalf("set inactive: %d %s", rec.Code, rec.Body)
	}
	before, _ := h.links.Get(l.ID)
	if !before.Inactive || before.Status(h.clock()) != model.StatusInactive {
		t.Fatalf("link = %+v", before)
	}
	calls, events := len(h.fake.ServiceCalls()), len(h.fake.FiredEvents())

	if rec := h.g(req{method: "GET", path: "/g/" + l.Slug, cookies: j.list()}); rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="view-ended"`) {
		t.Errorf("page: %d", rec.Code)
	}
	for _, r := range inactiveRoutes {
		if rec := h.dev(j, l, r.method, r.route, r.body); rec.Code != http.StatusGone {
			t.Errorf("%s %s: %d %s", r.method, r.route, rec.Code, rec.Body)
		}
	}
	// Wrong passwords are refused before they are checked, so they never
	// count toward a lockout.
	for range 5 {
		if rec := h.tryPassword(l, "wrong", peerN(1)); rec.Code != http.StatusGone {
			t.Errorf("wrong password: %d", rec.Code)
		}
	}
	// The admin can't approve the waiting browser while the link is off.
	if v := h.srv.devices.Pending(l.ID); len(v) == 1 {
		rec := h.a(req{method: "POST", path: "/api/devices/pending/" + v[0].ID, body: map[string]any{"approve": true}})
		if rec.Code == 200 {
			t.Errorf("approved a device on an inactive link")
		}
	} else {
		t.Errorf("%d pending requests, want 1", len(v))
	}

	after, _ := h.links.Get(l.ID)
	if after.UseCount != before.UseCount || len(after.Used) != len(before.Used) || len(after.Devices) != len(before.Devices) {
		t.Errorf("link changed: uses %d→%d, used %d→%d, devices %d→%d",
			before.UseCount, after.UseCount, len(before.Used), len(after.Used), len(before.Devices), len(after.Devices))
	}
	if n := len(h.fake.ServiceCalls()); n != calls {
		t.Errorf("%d calls reached HA", n-calls)
	}
	if n := len(h.fake.FiredEvents()); n != events {
		t.Errorf("%d events fired: %+v", n-events, h.fake.FiredEvents()[events:])
	}
	if s := h.lockoutScopes(); len(s) != 0 {
		t.Errorf("lockouts: %v", s)
	}

	// On again: the same session and device work, and the used pair stays
	// used.
	if rec := h.a(req{method: "PUT", path: "/api/links/" + l.ID, body: linkBody(l, false)}); rec.Code != 200 {
		t.Fatalf("set active: %d %s", rec.Code, rec.Body)
	}
	if rec := h.dev(j, l, "GET", "state", nil); rec.Code != 200 {
		t.Errorf("state after activate: %d %s", rec.Code, rec.Body)
	}
	if rec := h.dev(j, l, "POST", "act", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}); rec.Code != http.StatusConflict {
		t.Errorf("used pair after activate: %d", rec.Code)
	}
	// A fresh address: the per-IP request limiter is spent. The two
	// sign-ins above still hold two of the three per-link slots, so the
	// five wrong tries would have slowed the link had they counted.
	if rec := h.tryPassword(l, "right", peerN(2)); rec.Code != 200 {
		t.Errorf("password after activate: %d", rec.Code)
	}
}

// An inactive link with no window is saved from the admin API, is listed
// as inactive with no valid_until, and gives guests the page and 410,
// never 404.
func TestGuestInactiveLinkWithNoWindow(t *testing.T) {
	h := newHarness(t)
	rec := h.a(req{method: "POST", path: "/api/links", body: map[string]any{
		"name": "Later", "templates": []string{"guest_room"}, "inactive": true,
	}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	v := decode[map[string]any](t, rec)
	if v["status"] != model.StatusInactive || v["inactive"] != true {
		t.Errorf("view: status %v, inactive %v", v["status"], v["inactive"])
	}
	if _, ok := v["valid_until"]; ok {
		t.Errorf("valid_until in view: %v", v["valid_until"])
	}
	if p, _ := v["problems"].([]any); len(p) != 0 {
		t.Errorf("problems: %v", p)
	}
	l, _ := h.links.Get(v["id"].(string))
	if rec := h.g(req{method: "GET", path: "/g/" + l.Slug}); rec.Code != 200 {
		t.Errorf("page: %d", rec.Code)
	}
	for _, r := range inactiveRoutes {
		if r.route == "password" {
			continue // the link has no password; that is a 400 once active
		}
		if rec := h.g(req{method: r.method, path: "/g/" + l.Slug + "/" + r.route, body: r.body}); rec.Code != http.StatusGone {
			t.Errorf("%s %s: %d", r.method, r.route, rec.Code)
		}
	}
	// The list shows it with the rest.
	list := decode[struct {
		Links []map[string]any `json:"links"`
	}](t, h.a(req{method: "GET", path: "/api/links"}))
	if len(list.Links) != 1 || list.Links[0]["status"] != model.StatusInactive {
		t.Errorf("list: %v", list.Links)
	}

	// An active link still needs an end.
	rec = h.a(req{method: "PUT", path: "/api/links/" + l.ID, body: map[string]any{"name": "Later", "templates": []string{"guest_room"}}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "valid_until") {
		t.Errorf("active with no end: %d %s", rec.Code, rec.Body)
	}
	// A time later, it is still inactive, not expired.
	h.advance(1000 * time.Hour)
	if got, _ := h.links.Get(l.ID); got.Status(h.clock()) != model.StatusInactive {
		t.Errorf("status later: %s", got.Status(h.clock()))
	}
}

// Setting a link inactive ends an open event stream, as the end of its
// window does.
func TestGuestEventsEndWhenInactive(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	ch, _ := openStream(t, h, l)
	for ev, _ := next(t, ch); ev.name != "ready"; ev, _ = next(t, ch) {
	}
	edit := l
	edit.Inactive = true
	if _, err := h.links.Update(l.ID, edit); err != nil {
		t.Fatal(err)
	}
	for {
		ev, ok := next(t, ch)
		if !ok {
			t.Fatal("stream closed without end")
		}
		if ev.name == "end" {
			break
		}
	}
	waitUntil(t, func() bool { return h.srv.streams.count(l.ID) == 0 })
}
