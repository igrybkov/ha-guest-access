package web

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

func getEntityText(t *testing.T, h *harness) map[string]model.EntityText {
	t.Helper()
	rec := h.a(req{method: "GET", path: "/api/entity-text"})
	if rec.Code != 200 {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	return decode[entityTextsView](t, rec).Entities
}

// The admin sets, changes and removes an entity's text. It lands in
// templates.yaml and leaves the templates alone.
func TestAdminEntityText(t *testing.T) {
	h := newHarness(t)
	if rec := h.a(req{method: "GET", path: "/api/entity-text"}); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"entities":{}}` {
		t.Fatalf("empty GET: %d %s", rec.Code, rec.Body)
	}
	rec := h.a(req{method: "PUT", path: "/api/entity-text/light.guest_room", body: map[string]any{"name": "  Reading lamp ", "description": "By the bed\nDimmable\n"}})
	if rec.Code != 200 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	want := model.EntityText{Name: "Reading lamp", Description: "By the bed\nDimmable"}
	if got := decode[model.EntityText](t, rec); got != want {
		t.Errorf("PUT reply = %+v", got)
	}
	// An entity HA does not know is kept.
	if rec := h.a(req{method: "PUT", path: "/api/entity-text/light.not_in_ha", body: map[string]any{"description": "Later"}}); rec.Code != 200 {
		t.Fatalf("unknown entity: %d %s", rec.Code, rec.Body)
	}
	got := getEntityText(t, h)
	if len(got) != 2 || got["light.guest_room"] != want || got["light.not_in_ha"].Description != "Later" {
		t.Errorf("GET = %+v", got)
	}
	ts, err := h.tpl.Templates()
	if err != nil || len(ts.Templates) != len(testTemplates()) || ts.Entities["light.guest_room"] != want {
		t.Errorf("stored = %+v, %v", ts, err)
	}
	// Both empty, or only spaces, removes the entry.
	for _, id := range []string{"light.guest_room", "light.not_in_ha"} {
		if rec := h.a(req{method: "PUT", path: "/api/entity-text/" + id, body: map[string]any{"name": " ", "description": ""}}); rec.Code != 200 {
			t.Fatalf("remove %s: %d %s", id, rec.Code, rec.Body)
		}
	}
	if got := getEntityText(t, h); len(got) != 0 {
		t.Errorf("after remove = %+v", got)
	}
	if ts, _ := h.tpl.Templates(); ts.Entities != nil {
		t.Errorf("empty map kept: %+v", ts.Entities)
	}
	// Removing what is not there is fine.
	if rec := h.a(req{method: "PUT", path: "/api/entity-text/light.bedroom", body: map[string]any{}}); rec.Code != 200 {
		t.Errorf("remove missing: %d", rec.Code)
	}
}

func TestAdminEntityTextRefuses(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name, path string
		body       any
		code       int
		problem    string
	}{
		{"bad id", "/api/entity-text/Light.Guest", map[string]any{"name": "x"}, 400, ""},
		{"no domain", "/api/entity-text/guest_room", map[string]any{"name": "x"}, 400, ""},
		{"dots", "/api/entity-text/light.a.b", map[string]any{"name": "x"}, 400, ""},
		{"escaped slash", "/api/entity-text/light.a%2Fb", map[string]any{"name": "x"}, 400, ""},
		{"no id", "/api/entity-text/", map[string]any{"name": "x"}, 404, ""},
		{"name too long", "/api/entity-text/light.guest_room", map[string]any{"name": strings.Repeat("a", model.MaxEntityNameLen+1)}, 400, "name"},
		{"name two lines", "/api/entity-text/light.guest_room", map[string]any{"name": "a\nb"}, 400, "name"},
		{"description too long", "/api/entity-text/light.guest_room", map[string]any{"description": strings.Repeat("a", model.MaxEntityDescriptionLen+1)}, 400, "description"},
		{"description control", "/api/entity-text/light.guest_room", map[string]any{"description": "a\x1bb"}, 400, "description"},
		{"unknown field", "/api/entity-text/light.guest_room", `{"name":"x","html":"<b>"}`, 400, ""},
		{"not an object", "/api/entity-text/light.guest_room", `"x"`, 400, ""},
		{"trailing data", "/api/entity-text/light.guest_room", `{"name":"x"}{}`, 400, ""},
	} {
		rec := h.a(req{method: "PUT", path: tc.path, body: tc.body})
		if rec.Code != tc.code {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
			continue
		}
		if tc.problem != "" {
			ps := decode[adminBody](t, rec).Problems
			if len(ps) != 1 || ps[0].Path != tc.problem {
				t.Errorf("%s: problems %+v", tc.name, ps)
			}
		}
	}
	// A cap counts bytes, not letters.
	if rec := h.a(req{method: "PUT", path: "/api/entity-text/light.guest_room", body: map[string]any{"name": strings.Repeat("é", model.MaxEntityNameLen/2+1)}}); rec.Code != 400 {
		t.Errorf("bytes cap: %d", rec.Code)
	}
	// The JSON rule holds as on every admin write.
	if rec := h.a(req{method: "PUT", path: "/api/entity-text/light.guest_room", body: `{"name":"x"}`, ctype: "text/plain"}); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", rec.Code)
	}
	// Only the ingress peer gets in.
	for _, m := range []string{"GET", "PUT"} {
		r := req{method: m, path: "/api/entity-text", peer: "203.0.113.5:1"}
		if m == "PUT" {
			r.path, r.body = "/api/entity-text/light.guest_room", map[string]any{"name": "x"}
		}
		if rec := h.a(r); rec.Code != http.StatusForbidden {
			t.Errorf("%s from another peer: %d", m, rec.Code)
		}
	}
	if got := getEntityText(t, h); len(got) != 0 {
		t.Errorf("a refused write landed: %+v", got)
	}
}

// The guest port has no admin routes: neither path reaches a handler.
func TestGuestCannotReachEntityText(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	for _, r := range []req{
		{method: "GET", path: "/api/entity-text"},
		{method: "PUT", path: "/api/entity-text/light.guest_room", body: map[string]any{"name": "Hacked"}},
		{method: "PUT", path: "/g/" + l.Slug + "/api/entity-text/light.guest_room", body: map[string]any{"name": "Hacked"}},
		{method: "GET", path: "/g/" + l.Slug + "/entity-text"},
	} {
		if rec := h.g(r); rec.Code == 200 || strings.Contains(rec.Body.String(), "entities") {
			t.Errorf("guest %s %s: %d %s", r.method, r.path, rec.Code, rec.Body)
		}
	}
	if got := getEntityText(t, h); len(got) != 0 {
		t.Errorf("guest wrote text: %+v", got)
	}
}

// A template save keeps the entity text, and a text save keeps the
// templates, also when both run at once.
func TestEntityTextAndTemplatesKeepEachOther(t *testing.T) {
	h := newHarness(t)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			id := "light.lamp_" + string(rune('a'+i))
			if rec := h.a(req{method: "PUT", path: "/api/entity-text/" + id, body: map[string]any{"name": "Lamp"}}); rec.Code != 200 {
				t.Errorf("text %s: %d %s", id, rec.Code, rec.Body)
			}
		}()
		go func() {
			defer wg.Done()
			tpl := model.Template{ID: "extra_" + string(rune('a'+i)), Name: "Extra", Rules: []model.Rule{}}
			if rec := h.a(req{method: "POST", path: "/api/templates", body: tpl}); rec.Code != 201 {
				t.Errorf("template %s: %d %s", tpl.ID, rec.Code, rec.Body)
			}
		}()
	}
	wg.Wait()
	ts, err := h.tpl.Templates()
	if err != nil {
		t.Fatal(err)
	}
	if len(ts.Entities) != 10 || len(ts.Templates) != len(testTemplates())+10 {
		t.Errorf("entities %d, templates %d", len(ts.Entities), len(ts.Templates))
	}
	// An update of an existing template keeps them too.
	g, _ := ts.Find("guest_room")
	g.Name = "Guest room 2"
	if rec := h.a(req{method: "PUT", path: "/api/templates/guest_room", body: g}); rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if ts, _ := h.tpl.Templates(); len(ts.Entities) != 10 {
		t.Errorf("update dropped entities: %+v", ts.Entities)
	}
}

// A text change is audited with the entity id, never the text.
func TestAuditEntityText(t *testing.T) {
	ah := newAuditHarness(t)
	user := map[string]string{"X-Remote-User-Name": "illia"}
	ah.a(req{method: "PUT", path: "/api/entity-text/light.guest_room", body: map[string]any{"name": "Secret lamp name"}, headers: user})
	ah.a(req{method: "PUT", path: "/api/entity-text/light.guest_room", body: map[string]any{"name": "a\nb"}, headers: user})
	ah.a(req{method: "GET", path: "/api/entity-text", headers: user})
	recs := ah.records()
	if len(recs) != 2 {
		t.Fatalf("records = %+v", recs)
	}
	if r := recs[0]; r.Event != "admin.entity_text.update" || r.Result != "ok" || r.Target != "light.guest_room" ||
		r.Route != "PUT /api/entity-text/{id}" || r.User == nil || r.User.Name != "illia" {
		t.Errorf("record = %+v", r)
	}
	if r := recs[1]; r.Event != "admin.entity_text.update" || r.Result != "failed" || r.Reason != "invalid" {
		t.Errorf("failed record = %+v", r)
	}
}

// The guest page shows the admin's name and description. Order by name
// uses the shown name. Other entities keep their HA names.
func TestGuestStateEntityText(t *testing.T) {
	// The file is written by hand, as a git pull on HA would, so it can
	// hold entries the admin API refuses.
	dir := t.TempDir()
	tpl := config.NewTemplatesStore(migrate.OpenConfig(dir, quietLog))
	h := newHarness(t, func(_ *config.Options, d *Deps) { d.Templates = tpl })
	h.tpl = tpl
	ts := model.Templates{Version: 1, Templates: append(testTemplates(), model.Template{ID: "mixed", Name: "Mixed", Rules: mixedRules()})}
	ts.Entities = map[string]model.EntityText{
		// Guest lamp sorts after Bedroom by HA name; "A lamp" sorts first.
		"light.guest_room": {Name: " A lamp ", Description: "By the bed\nDimmable"},
		"switch.office":    {Description: "Desk fan"},
		// Bad entries never reach guests.
		"cover.garage_door": {Name: "Bad\x00name", Description: "hidden"},
		"light.bedroom":     {Name: "   "},
	}
	if err := tpl.Save(ts); err == nil {
		t.Fatal("saved a bad entry")
	}
	raw, err := yaml.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, migrate.TemplatesFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.Templates(); err == nil {
		t.Fatal("the hand edit validates")
	}

	l := h.link("mixed")
	rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"})
	if rec.Code != 200 {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	v := decode[stateView](t, rec)
	for id, want := range map[string]entityView{
		"light.guest_room":  {Name: "A lamp", Description: "By the bed\nDimmable"},
		"switch.office":     {Name: "Office", Description: "Desk fan"},
		"cover.garage_door": {Name: "Garage door"},
		"light.bedroom":     {Name: "Bedroom"},
	} {
		e := v.Entities[id]
		if e == nil || e.Name != want.Name || e.Description != want.Description {
			t.Errorf("%s = %+v, want %+v", id, e, want)
		}
	}
	if strings.Contains(rec.Body.String(), "hidden") {
		t.Errorf("a bad entry reached the guest: %s", rec.Body)
	}
	if strings.Count(rec.Body.String(), `"description"`) != 2 {
		t.Errorf("description sent where none is set: %s", rec.Body)
	}
	var got []string
	for _, g := range v.Groups {
		if g.ID == "mixed" {
			got = g.Entities
		}
	}
	want := []string{"light.guest_room", "light.bedroom", "cover.garage_door", "switch.office"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	// The text is only on entities the link grants.
	l2 := h.link("guest_room")
	body := h.g(req{method: "GET", path: "/g/" + l2.Slug + "/state"}).Body.String()
	if strings.Contains(body, "Desk fan") || !strings.Contains(body, "A lamp") {
		t.Errorf("guest_room link state: %s", body)
	}
}
