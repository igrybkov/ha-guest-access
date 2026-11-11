package web

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// mixedRules grant four entities whose names sort apart from their ids:
// Bedroom (light.bedroom), Garage door (cover.garage_door), Guest lamp
// (light.guest_room) and Office (switch.office).
func mixedRules() []model.Rule {
	return []model.Rule{
		{Select: model.Selector{Entities: []string{"light.guest_room", "light.bedroom"}}, Actions: []string{"light.turn_on"}},
		{Select: model.Selector{Entities: []string{"switch.office", "cover.garage_door"}}, Actions: []string{"switch.turn_on", "cover.open_cover"}},
	}
}

// orderHarness adds the template "mixed" with the given entity order.
func orderHarness(t *testing.T, order []string) *harness {
	t.Helper()
	h := newHarness(t)
	ts := append(testTemplates(), model.Template{ID: "mixed", Name: "Mixed", Rules: mixedRules(), EntityOrder: order})
	if err := h.tpl.Save(model.Templates{Version: 1, Templates: ts}); err != nil {
		t.Fatal(err)
	}
	return h
}

func groupEntities(t *testing.T, h *harness, l model.Link) map[string][]string {
	t.Helper()
	rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"})
	if rec.Code != 200 {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	out := map[string][]string{}
	for _, g := range decode[stateView](t, rec).Groups {
		out[g.ID] = g.Entities
	}
	return out
}

// With no order, a group lists its entities by name, not by id.
func TestGuestGroupDefaultsToName(t *testing.T) {
	h := orderHarness(t, nil)
	got := groupEntities(t, h, h.link("mixed"))["mixed"]
	want := []string{"light.bedroom", "cover.garage_door", "light.guest_room", "switch.office"}
	if !slices.Equal(got, want) {
		t.Errorf("mixed = %v, want %v", got, want)
	}
}

// A template's entity_order puts its entities first. An entry that does
// not resolve is skipped. The rest follow by name.
func TestGuestTemplateEntityOrder(t *testing.T) {
	h := orderHarness(t, []string{"switch.office", "light.gone", "light.guest_room"})
	got := groupEntities(t, h, h.link("mixed"))["mixed"]
	want := []string{"switch.office", "light.guest_room", "light.bedroom", "cover.garage_door"}
	if !slices.Equal(got, want) {
		t.Errorf("mixed = %v, want %v", got, want)
	}
}

// A template's order touches only its own group, and a link's order
// touches only the group of its own rules. An entity that a template
// grants stays in the template's group even when the link's order names
// it.
func TestGuestLinkEntityOrder(t *testing.T) {
	h := orderHarness(t, []string{"switch.office"})
	l, err := h.links.Create(model.Link{
		Name: "Anna's visit", ValidFrom: t0.Add(-time.Hour), ValidUntil: t0.Add(24 * time.Hour),
		TemplateIDs: []string{"guest_room"},
		Extra: []model.Rule{
			{Select: model.Selector{Entities: []string{"light.guest_room", "light.bedroom"}}, Actions: []string{"light.turn_on"}},
			{Select: model.Selector{Entities: []string{"switch.office", "cover.garage_door"}}, Actions: []string{"switch.turn_on", "cover.open_cover"}},
		},
		EntityOrder: []string{"cover.garage_door", "light.guest_room", "sensor.unknown"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := groupEntities(t, h, l)
	if !slices.Equal(got["guest_room"], []string{"light.guest_room"}) {
		t.Errorf("guest_room = %v", got["guest_room"])
	}
	want := []string{"cover.garage_door", "light.bedroom", "switch.office"}
	if !slices.Equal(got[""], want) {
		t.Errorf("own rules = %v, want %v", got[""], want)
	}
}

// The strict admin decoders take entity_order on template and link saves
// and on the preview, and the saved value comes back. A bad entry is a
// problem at its path.
func TestAdminSavesEntityOrder(t *testing.T) {
	h := newHarness(t)
	tpl := `{"id":"mixed","name":"Mixed","rules":[{"select":{"entities":["switch.office"]},"actions":["switch.turn_on"]}],"entity_order":["switch.office","light.gone"]}`
	if rec := h.a(req{method: "POST", path: "/api/templates", body: tpl}); rec.Code != 201 {
		t.Fatalf("create template: %d %s", rec.Code, rec.Body)
	}
	tpl2 := `{"id":"mixed","name":"Mixed","rules":[{"select":{"entities":["switch.office"]},"actions":["switch.turn_on"]}],"entity_order":["light.gone","switch.office"]}`
	if rec := h.a(req{method: "PUT", path: "/api/templates/mixed", body: tpl2}); rec.Code != 200 {
		t.Fatalf("update template: %d %s", rec.Code, rec.Body)
	}
	ts, _ := h.tpl.Templates()
	if got, _ := ts.Find("mixed"); !slices.Equal(got.EntityOrder, []string{"light.gone", "switch.office"}) {
		t.Errorf("stored template order = %v", got.EntityOrder)
	}
	bad := `{"id":"bad","name":"Bad","rules":[{"select":{"entities":["switch.office"]},"actions":["switch.turn_on"]}],"entity_order":["office"]}`
	rec := h.a(req{method: "POST", path: "/api/templates", body: bad})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "entity_order[0]") {
		t.Errorf("bad order: %d %s", rec.Code, rec.Body)
	}

	link := `{"name":"Anna","valid_until":"` + t0.Add(24*time.Hour).Format(time.RFC3339) + `","templates":["mixed"],` +
		`"extra":[{"select":{"entities":["light.bedroom"]},"actions":["light.turn_on"]}],"entity_order":["light.bedroom"]}`
	rec = h.a(req{method: "POST", path: "/api/links", body: link})
	if rec.Code != 201 {
		t.Fatalf("create link: %d %s", rec.Code, rec.Body)
	}
	v := decode[linkView](t, rec)
	if !slices.Equal(v.EntityOrder, []string{"light.bedroom"}) {
		t.Errorf("link view order = %v", v.EntityOrder)
	}
	edit := strings.Replace(link, `"entity_order":["light.bedroom"]`, `"entity_order":["light.bedroom","switch.office"]`, 1)
	if rec := h.a(req{method: "PUT", path: "/api/links/" + v.ID, body: edit}); rec.Code != 200 {
		t.Fatalf("update link: %d %s", rec.Code, rec.Body)
	}
	if got, err := h.links.Get(v.ID); err != nil || !slices.Equal(got.EntityOrder, []string{"light.bedroom", "switch.office"}) {
		t.Errorf("stored link order = %v %v", got.EntityOrder, err)
	}
	if rec := h.a(req{method: "POST", path: "/api/preview", body: `{"link":` + link + `}`}); rec.Code != 200 {
		t.Errorf("preview: %d %s", rec.Code, rec.Body)
	}
}

func TestSortGroup(t *testing.T) {
	ents := map[string]*entityView{
		"light.b":  {Name: "beta"},
		"light.a":  {Name: "Beta"},
		"light.c":  {Name: "alpha"},
		"switch.z": {Name: "Zulu"},
		"fan.x":    {Name: "Xray"},
	}
	for _, tc := range []struct {
		name  string
		order []string
		want  []string
	}{
		{"by name then id", nil, []string{"light.c", "light.a", "light.b", "fan.x", "switch.z"}},
		{"order first", []string{"switch.z", "light.b"}, []string{"switch.z", "light.b", "light.c", "light.a", "fan.x"}},
		{"unknown and repeated entries", []string{"x.gone", "fan.x", "fan.x", "light.a"}, []string{"fan.x", "light.a", "light.c", "light.b", "switch.z"}},
	} {
		ids := []string{"switch.z", "light.b", "fan.x", "light.a", "light.c"}
		sortGroup(ids, tc.order, ents)
		if !slices.Equal(ids, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, ids, tc.want)
		}
	}
}
