package web

import (
	"bytes"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

func TestAdminPeerCheck(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		peer    string
		headers map[string]string
		want    int
	}{
		{"172.30.32.2:1234", nil, 200},
		{"[::ffff:172.30.32.2]:1234", nil, 200},
		{"172.30.32.1:1234", nil, 403},
		{"127.0.0.1:1234", nil, 403},
		{"192.168.1.5:1234", map[string]string{"X-Forwarded-For": "172.30.32.2"}, 403},
		{"garbage", nil, 403},
	} {
		if rec := h.a(req{method: "GET", path: "/api/settings", peer: tc.peer, headers: tc.headers}); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.peer, rec.Code, tc.want)
		}
	}
	// The health check is open: the -healthcheck probe uses loopback.
	if rec := h.a(req{method: "GET", path: "/healthz", peer: "127.0.0.1:1"}); rec.Code != 200 {
		t.Errorf("healthz: %d", rec.Code)
	}
	dev := newHarness(t, func(_ *config.Options, d *Deps) { d.DevAdminAnyPeer = true })
	if rec := dev.a(req{method: "GET", path: "/api/settings", peer: "127.0.0.1:1"}); rec.Code != 200 {
		t.Errorf("dev any peer: %d", rec.Code)
	}
}

func TestAdminWritesNeedJSON(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	for _, r := range []req{
		{method: "POST", path: "/api/links/" + l.ID + "/revoke"},
		{method: "DELETE", path: "/api/links/" + l.ID},
		{method: "POST", path: "/api/templates", body: `{"id":"x"}`, ctype: "text/plain"},
		{method: "POST", path: "/api/links", body: "name=x", ctype: "application/x-www-form-urlencoded"},
	} {
		if rec := h.a(r); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s %s: %d", r.method, r.path, rec.Code)
		}
	}
	if got, _ := h.links.Get(l.ID); got.Revoked {
		t.Error("revoked without JSON")
	}
}

func TestAdminLimit(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.RateLimits.AdminPerMin = 2 })
	h.a(req{method: "GET", path: "/api/settings"})
	h.a(req{method: "GET", path: "/api/settings"})
	if rec := h.a(req{method: "GET", path: "/api/settings"}); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("over: %d", rec.Code)
	}
}

func TestAdminSettingsReadOnly(t *testing.T) {
	h := newHarness(t, withMA(stubMA{}), func(o *config.Options, _ *Deps) { o.LimitsEnabled = false })
	rec := h.a(req{method: "GET", path: "/api/settings"})
	body := rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, `"tok"`) || !strings.Contains(body, `"ma_token":{"set":true,"refused":false}`) ||
		!strings.Contains(body, `"limits_disabled":true`) || !strings.Contains(body, `"public_base_url":"https://guest.example.com"`) {
		t.Errorf("settings: %d %s", rec.Code, body)
	}
	v := decode[map[string]any](t, rec)
	if _, ok := v["rate_limits"]; !ok {
		t.Errorf("no rate_limits: %v", v)
	}
	if rec := h.a(req{method: "PUT", path: "/api/settings", body: map[string]any{}}); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("PUT settings: %d", rec.Code)
	}
}

func TestAdminTemplates(t *testing.T) {
	h := newHarness(t)
	tpl := model.Template{ID: "kitchen", Name: "Kitchen", Rules: []model.Rule{{
		Select: model.Selector{Entities: []string{"light.bedroom"}}, Actions: []string{"light.turn_on"}}}}
	if rec := h.a(req{method: "POST", path: "/api/templates", body: tpl}); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := h.a(req{method: "POST", path: "/api/templates", body: tpl}); rec.Code != 409 {
		t.Errorf("duplicate: %d", rec.Code)
	}
	bad := tpl
	bad.ID = "bad"
	bad.Rules = []model.Rule{{Select: model.Selector{Entities: []string{"*"}}, Actions: []string{"light.turn_on"}}}
	rec := h.a(req{method: "POST", path: "/api/templates", body: bad})
	if v := decode[adminBody](t, rec); rec.Code != 400 || len(v.Problems) == 0 || v.Problems[0].Path != "rules[0].select.entities[0]" {
		t.Errorf("wildcard: %d %+v", rec.Code, v)
	}
	local := tpl
	local.ID = "local"
	local.Access = &model.Access{Network: model.NetworkLocal}
	if rec := h.a(req{method: "POST", path: "/api/templates", body: local}); rec.Code != 400 || !strings.Contains(rec.Body.String(), "local access") {
		t.Errorf("local while off: %d %s", rec.Code, rec.Body)
	}
	unknown := tpl
	unknown.ID = "unknown"
	unknown.Rules = []model.Rule{{Select: model.Selector{Entities: []string{"light.x"}}, Actions: []string{"light.explode"}}}
	if rec := h.a(req{method: "POST", path: "/api/templates", body: unknown}); rec.Code != 400 {
		t.Errorf("unknown action: %d", rec.Code)
	}
	tpl.Name = "Kitchen 2"
	if rec := h.a(req{method: "PUT", path: "/api/templates/kitchen", body: tpl}); rec.Code != 200 {
		t.Errorf("update: %d %s", rec.Code, rec.Body)
	}
	tpl.ID = "other"
	if rec := h.a(req{method: "PUT", path: "/api/templates/kitchen", body: tpl}); rec.Code != 400 {
		t.Errorf("id change: %d", rec.Code)
	}
	if rec := h.a(req{method: "PUT", path: "/api/templates/missing", body: model.Template{ID: "missing", Name: "M", Rules: bad.Rules[:0]}}); rec.Code != 404 && rec.Code != 400 {
		t.Errorf("update missing: %d", rec.Code)
	}
	if rec := h.a(req{method: "POST", path: "/api/templates", body: `{"id":"x","name":"X","rules":[],"extra":1}`}); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}
	l := h.link("kitchen")
	rec = h.a(req{method: "DELETE", path: "/api/templates/kitchen", body: map[string]any{}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), l.Name) {
		t.Errorf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := h.a(req{method: "DELETE", path: "/api/templates/kitchen", body: map[string]any{}}); rec.Code != 404 {
		t.Errorf("delete again: %d", rec.Code)
	}
	list := decode[templatesView](t, h.a(req{method: "GET", path: "/api/templates"}))
	if len(list.Templates) != len(testTemplates()) {
		t.Errorf("list = %d templates", len(list.Templates))
	}
	// The stored office template uses local while it is off: listed as a
	// problem, still served.
	if !strings.Contains(problemsString(list.Problems), "local access") {
		t.Errorf("problems = %+v", list.Problems)
	}
}

func problemsString(p []model.Problem) string {
	var b strings.Builder
	for _, x := range p {
		b.WriteString(x.String() + "; ")
	}
	return b.String()
}

func TestAdminPreview(t *testing.T) {
	h := newHarness(t)
	tpl := model.Template{ID: "guest_room", Name: "Guest room", Rules: []model.Rule{{
		Select:  model.Selector{Areas: []string{"guest_room"}},
		Exclude: model.Selector{Entities: []string{"lock.*"}},
		Actions: []string{"light.turn_on", "lock.unlock"},
	}}}
	rec := h.a(req{method: "POST", path: "/api/preview", body: map[string]any{"template": tpl}})
	v := decode[previewView](t, rec)
	if rec.Code != 200 || len(v.Rules) != 1 || strings.Join(v.Rules[0].Entities, ",") != "light.guest_room" {
		t.Fatalf("preview: %d %+v", rec.Code, v)
	}
	if len(v.Grants) != 1 || v.Grants[0].EntityID != "light.guest_room" || v.Grants[0].Name != "Guest lamp" {
		t.Errorf("grants = %+v", v.Grants)
	}
	// A link draft with a missing template and an extra rule.
	rec = h.a(req{method: "POST", path: "/api/preview", body: map[string]any{"link": model.Link{
		TemplateIDs: []string{"entry", "nope"},
		Extra:       []model.Rule{{Select: model.Selector{Entities: []string{"switch.office"}}, Actions: []string{"switch.turn_on"}}},
	}}})
	v = decode[previewView](t, rec)
	if rec.Code != 200 || len(v.Problems) == 0 || len(v.Warnings) == 0 || len(v.Grants) != 4 {
		t.Errorf("link preview: %d %+v", rec.Code, v)
	}
	if rec := h.a(req{method: "POST", path: "/api/preview", body: map[string]any{}}); rec.Code != 400 {
		t.Errorf("empty preview: %d", rec.Code)
	}
}

func TestAdminLinks(t *testing.T) {
	h := newHarness(t)
	draft := map[string]any{"name": "Anna", "valid_until": t0.Add(48 * time.Hour), "templates": []string{"guest_room"}, "slug": "anna-visit-2026"}
	rec := h.a(req{method: "POST", path: "/api/links", body: draft})
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	l := decode[linkView](t, rec)
	if l.Slug != "anna-visit-2026" || l.URL != "https://guest.example.com/g/anna-visit-2026" || l.Path != "/g/anna-visit-2026" || l.Status != "active" {
		t.Errorf("link = %+v", l)
	}
	if rec := h.a(req{method: "POST", path: "/api/links", body: draft}); rec.Code != 409 {
		t.Errorf("slug taken: %d", rec.Code)
	}
	draft["slug"] = "Short"
	if rec := h.a(req{method: "POST", path: "/api/links", body: draft}); rec.Code != 400 {
		t.Errorf("bad slug: %d", rec.Code)
	}
	draft["slug"] = ""
	draft["templates"] = []string{"nope"}
	if rec := h.a(req{method: "POST", path: "/api/links", body: draft}); rec.Code != 400 {
		t.Errorf("missing template: %d %s", rec.Code, rec.Body)
	}

	// Passwords: hashed, never returned.
	rec = h.a(req{method: "POST", path: "/api/links/" + l.ID + "/passwords", body: map[string]any{"label": "Anna", "password": "+1 555 0100"}})
	if rec.Code != 201 || strings.Contains(rec.Body.String(), "argon2") || strings.Contains(rec.Body.String(), "555") {
		t.Fatalf("add password: %d %s", rec.Code, rec.Body)
	}
	pw := decode[model.LinkPassword](t, rec)
	if rec := h.a(req{method: "POST", path: "/api/links/" + l.ID + "/passwords", body: map[string]any{"label": "Bob", "password": ""}}); rec.Code != 400 {
		t.Errorf("empty password: %d", rec.Code)
	}
	list := h.a(req{method: "GET", path: "/api/links"})
	if strings.Contains(list.Body.String(), "argon2") || !strings.Contains(list.Body.String(), `"label":"Anna"`) {
		t.Errorf("list leaks or lacks: %s", list.Body)
	}
	if rec := h.a(req{method: "DELETE", path: "/api/links/" + l.ID + "/passwords/" + pw.ID, body: map[string]any{}}); rec.Code != 200 {
		t.Errorf("remove password: %d", rec.Code)
	}

	edit := map[string]any{"name": "Anna B", "valid_until": t0.Add(72 * time.Hour), "templates": []string{"guest_room", "entry"}}
	if rec := h.a(req{method: "PUT", path: "/api/links/" + l.ID, body: edit}); rec.Code != 200 || decode[linkView](t, rec).Name != "Anna B" {
		t.Errorf("update: %d %s", rec.Code, rec.Body)
	}
	rec = h.a(req{method: "POST", path: "/api/links/" + l.ID + "/rotate", body: map[string]any{}})
	if rot := decode[linkView](t, rec); rec.Code != 200 || rot.Slug == l.Slug || len(rot.Slug) != 22 {
		t.Errorf("rotate: %d %+v", rec.Code, rot)
	}
	if u := decode[map[string]string](t, h.a(req{method: "GET", path: "/api/links/" + l.ID + "/url"})); !strings.HasPrefix(u["url"], "https://guest.example.com/g/") {
		t.Errorf("url = %v", u)
	}
	rec = h.a(req{method: "GET", path: "/api/links/" + l.ID + "/qr.svg"})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(rec.Body.String(), "<svg") {
		t.Errorf("qr svg: %d %.60s", rec.Code, rec.Body)
	}
	rec = h.a(req{method: "GET", path: "/api/links/" + l.ID + "/qr.png"})
	if img, err := png.Decode(bytes.NewReader(rec.Body.Bytes())); rec.Code != 200 || err != nil || img.Bounds().Dx() < 100 {
		t.Errorf("qr png: %d %v", rec.Code, err)
	}
	if rec := h.a(req{method: "POST", path: "/api/links/" + l.ID + "/revoke", body: map[string]any{}}); rec.Code != 200 || decode[linkView](t, rec).Status != "revoked" {
		t.Errorf("revoke: %d", rec.Code)
	}
	if rec := h.a(req{method: "DELETE", path: "/api/links/" + l.ID, body: map[string]any{}}); rec.Code != 200 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := h.a(req{method: "GET", path: "/api/links/" + l.ID}); rec.Code != 404 {
		t.Errorf("get deleted: %d", rec.Code)
	}
}

func TestAdminQRNeedsBaseURL(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.PublicBaseURL = "" })
	l := h.link("guest_room")
	if rec := h.a(req{method: "GET", path: "/api/links/" + l.ID + "/qr.svg"}); rec.Code != 409 {
		t.Errorf("qr without base: %d", rec.Code)
	}
	if v := decode[linkView](t, h.a(req{method: "GET", path: "/api/links/" + l.ID})); v.URL != "" || v.Path != "/g/"+l.Slug {
		t.Errorf("link view = %+v", v)
	}
}

func TestAdminCatalogAndPickers(t *testing.T) {
	h := newHarness(t)
	c := decode[catalogView](t, h.a(req{method: "GET", path: "/api/catalog"}))
	if len(c.Actions) == 0 || len(c.Attributes["light"]) == 0 {
		t.Errorf("catalog = %+v", c)
	}
	ents := decode[[]pickerEntity](t, h.a(req{method: "GET", path: "/api/entities"}))
	var lamp, window *pickerEntity
	for i := range ents {
		switch ents[i].EntityID {
		case "light.guest_room":
			lamp = &ents[i]
		case "lock.guest_room_window":
			window = &ents[i]
		}
	}
	if lamp == nil || lamp.AreaID != "guest_room" || lamp.Domain != "light" || window == nil || !window.NeedsCode {
		t.Errorf("entities = %+v", ents)
	}
	if a := decode[[]model.Area](t, h.a(req{method: "GET", path: "/api/areas"})); len(a) != 3 {
		t.Errorf("areas = %+v", a)
	}
	if d := decode[[]pickerDevice](t, h.a(req{method: "GET", path: "/api/devices"})); len(d) != 2 || d[0].Name != "Front lock" {
		t.Errorf("devices = %+v", d)
	}
	if rec := h.a(req{method: "GET", path: "/api/labels"}); rec.Code != 200 {
		t.Errorf("labels: %d", rec.Code)
	}
}

func TestAdminDiagnostics(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.DiagRequests = 3 })
	l := h.link("guest_room")
	h.g(req{method: "GET", path: "/g/" + l.Slug + "/state", headers: map[string]string{"X-Forwarded-For": "198.51.100.7"}})
	for range 3 {
		h.g(req{method: "GET", path: "/g/" + l.Slug, peer: "203.0.113.20:1"})
	}
	v := decode[diagnosticsView](t, h.a(req{method: "GET", path: "/api/diagnostics"}))
	if len(v.Requests) != 3 || v.Requests[0].Route != "g/page" || v.Requests[0].Client != "203.0.113.20" {
		t.Errorf("requests = %+v", v.Requests)
	}
	if strings.Contains(h.a(req{method: "GET", path: "/api/diagnostics"}).Body.String(), l.Slug) {
		t.Error("diagnostics show the slug")
	}
	// The untrusted forwarding entry fell out of the ring of 3.
	if len(v.Warnings) != 0 {
		t.Errorf("warnings = %v", v.Warnings)
	}
	h.g(req{method: "GET", path: "/g/" + l.Slug, headers: map[string]string{"X-Forwarded-For": "198.51.100.7"}})
	v = decode[diagnosticsView](t, h.a(req{method: "GET", path: "/api/diagnostics"}))
	if !v.Requests[0].UntrustedForwarding || len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "trusted_proxies") {
		t.Errorf("untrusted forwarding: %+v", v)
	}
}

func TestAdminPage(t *testing.T) {
	h := newHarness(t)
	rec := h.a(req{method: "GET", path: "/"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Guest Access") || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Errorf("admin page: %d %s", rec.Code, rec.Header())
	}
	if rec := h.a(req{method: "GET", path: "/api/nope"}); rec.Code != 404 {
		t.Errorf("unknown api: %d", rec.Code)
	}
}
