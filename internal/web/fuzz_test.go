package web

import (
	"net/http"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/config"
)

// FuzzActBody posts random bodies to /act through the whole guest
// handler. Whatever the body, the answer is one of the known statuses,
// and any HA call targets exactly one granted entity.
func FuzzActBody(f *testing.F) {
	for _, s := range []string{
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":40}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"color_temp_kelvin":3000.5}}`,
		`{"entity_id":"light.bedroom","action":"light.turn_on"}`,
		`{"entity_id":["light.guest_room"],"action":"light.turn_on"}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"area_id":"bedroom"}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","data":{"brightness_pct":1e400}}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on"}{}`,
		`{"entity_id":"light.guest_room","action":"light.turn_on","Entity_ID":"light.bedroom"}`,
		`{"entity_id":"light.guest_room","entity_id":"light.bedroom","action":"light.turn_on"}`,
		`null`, `[]`, `"x"`, `{`, ``,
	} {
		f.Add(s)
	}
	h := newHarness(f, func(o *config.Options, _ *Deps) { o.LimitsEnabled = false })
	l := h.link("guest_room")
	granted := map[string]bool{"light.guest_room": true}
	f.Fuzz(func(t *testing.T, body string) {
		h.t = t
		h.fake.Reset()
		rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/act", body: body})
		switch rec.Code {
		case http.StatusOK, http.StatusBadRequest, http.StatusForbidden, http.StatusRequestEntityTooLarge:
		default:
			t.Fatalf("body %q: status %d", body, rec.Code)
		}
		calls := h.fake.ServiceCalls()
		if rec.Code != http.StatusOK && len(calls) > 0 {
			t.Fatalf("body %q: refused but called HA", body)
		}
		for _, c := range calls {
			if c.Target == nil || len(c.Target.EntityID) != 1 || !granted[c.Target.EntityID[0]] ||
				len(c.Target.AreaID) > 0 || len(c.Target.DeviceID) > 0 {
				t.Fatalf("body %q: call %+v", body, c)
			}
			for _, k := range []string{"entity_id", "area_id", "device_id", "label_id"} {
				if _, ok := c.Data[k]; ok {
					t.Fatalf("body %q: data has %s", body, k)
				}
			}
		}
	})
}
