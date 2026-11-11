package web

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// withGarage adds a "garage" template to the harness. It grants open,
// close and position on the garage door, and nothing on the guest room
// shade.
func withGarage(t *testing.T, h *harness) model.Link {
	t.Helper()
	tpl := append(testTemplates(), model.Template{ID: "garage", Name: "Garage", Rules: []model.Rule{{
		Select:  model.Selector{Entities: []string{"cover.garage_door"}},
		Actions: []string{"cover.open_cover", "cover.close_cover", "cover.set_cover_position"},
	}}})
	if err := h.tpl.Save(model.Templates{Version: 1, Templates: tpl}); err != nil {
		t.Fatal(err)
	}
	return h.link("guest_room", "garage")
}

func TestGuestActCover(t *testing.T) {
	h := newHarness(t)
	l := withGarage(t, h)
	if rec := h.act(l, "cover.garage_door", "cover.set_cover_position", map[string]any{"position": 40}); rec.Code != 200 {
		t.Fatalf("position: %d %s", rec.Code, rec.Body)
	}
	if rec := h.act(l, "cover.garage_door", "cover.open_cover", nil); rec.Code != 200 {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	calls := h.fake.ServiceCalls()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	want := &ha.Target{EntityID: []string{"cover.garage_door"}}
	if c := calls[0]; c.Domain != "cover" || c.Service != "set_cover_position" || !reflect.DeepEqual(c.Target, want) ||
		!reflect.DeepEqual(c.Data, map[string]any{"position": 40}) {
		t.Errorf("position call = %+v", c)
	}
	if c := calls[1]; c.Domain != "cover" || c.Service != "open_cover" || !reflect.DeepEqual(c.Target, want) || len(c.Data) != 0 {
		t.Errorf("open call = %+v", c)
	}
}

// Every cover escape, over HTTP, with limits off so none is a 429.
// None reaches HA.
func TestGuestActCoverEscapes(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.LimitsEnabled = false })
	l := withGarage(t, h)
	act := func(entity, action string, data any) map[string]any {
		return map[string]any{"entity_id": entity, "action": action, "data": data}
	}
	for _, tc := range []struct {
		name string
		body any
		want int
	}{
		{"position 101", act("cover.garage_door", "cover.set_cover_position", map[string]any{"position": 101}), 400},
		{"position -1", act("cover.garage_door", "cover.set_cover_position", map[string]any{"position": -1}), 400},
		{"position 50.5", act("cover.garage_door", "cover.set_cover_position", map[string]any{"position": 50.5}), 400},
		{"position as a string", act("cover.garage_door", "cover.set_cover_position", map[string]any{"position": "50"}), 400},
		{"entity_id in the data", act("cover.garage_door", "cover.open_cover", map[string]any{"entity_id": "cover.guest_room_shade"}), 400},
		{"position plus entity_id", act("cover.garage_door", "cover.set_cover_position", map[string]any{"position": 50, "entity_id": "cover.guest_room_shade"}), 400},
		{"tilt not granted", act("cover.garage_door", "cover.set_cover_tilt_position", map[string]any{"tilt_position": 50}), 403},
		{"stop not granted", act("cover.garage_door", "cover.stop_cover", nil), 403},
		{"cover in a granted area with light actions only", act("cover.guest_room_shade", "cover.open_cover", nil), 403},
		{"light action on the shade", act("cover.guest_room_shade", "light.turn_on", nil), 403},
		{"not in the catalog", act("cover.garage_door", "cover.toggle", nil), 403},
	} {
		rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/act", body: tc.body})
		if rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
		if strings.Contains(rec.Body.String(), "guest_room_shade") {
			t.Errorf("%s: reply echoes input: %s", tc.name, rec.Body)
		}
	}
	if calls := h.fake.ServiceCalls(); len(calls) != 0 {
		t.Errorf("escapes reached HA: %+v", calls)
	}
}

// A cover attribute outside the allowlist never reaches a guest, and a
// cover with no granted cover action is not shown.
func TestGuestStateFiltersCoverAttributes(t *testing.T) {
	h := newHarness(t)
	l := withGarage(t, h)
	rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"})
	if rec.Code != http.StatusOK {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, leak := range []string{"is_closed", "speed", "assumed_state", "user_id", "secret-user", "camera_proxy", "opened_by", "cover.guest_room_shade"} {
		if strings.Contains(body, leak) {
			t.Errorf("state leaks %q: %s", leak, body)
		}
	}
	v := decode[stateView](t, rec)
	st, ok := v.States["cover.garage_door"]
	if !ok || st.State != "closed" {
		t.Fatalf("garage state = %+v", v.States)
	}
	allow := catalog.AttributeAllowlist("cover")
	for k := range st.Attributes {
		if !slices.Contains(allow, k) {
			t.Errorf("attribute %q is not on the allowlist", k)
		}
	}
	if st.Attributes["device_class"] != "garage" || st.Attributes["current_position"] != 0.0 {
		t.Errorf("garage attributes = %v", st.Attributes)
	}
	e := v.Entities["cover.garage_door"]
	if e == nil || e.Domain != "cover" || len(e.Actions) != 3 {
		t.Fatalf("garage entity = %+v", e)
	}
	for _, a := range e.Actions {
		if a.ID == "cover.set_cover_position" && (a.UI != "slider" || a.Fields["position"].Max != 100) {
			t.Errorf("position action = %+v", a)
		}
	}
}
