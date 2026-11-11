package authz

import (
	"encoding/json"
	"math"
	"slices"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// FuzzCheck sends random request JSON against a fixed grant set. Whatever
// the input, Check may only return a call for a granted pair, aimed at
// exactly that one entity, with data the catalog allows.
func FuzzCheck(f *testing.F) {
	seeds := []string{
		`{"entity_id":"light.guest_ceiling","action":"light.turn_on","data":{"brightness_pct":50}}`,
		`{"entity_id":"light.guest_ceiling","action":"light.turn_on","data":{"color_temp_kelvin":2700}}`,
		`{"entity_id":"light.guest_ceiling","action":"light.turn_on","data":{"entity_id":"lock.front_door"}}`,
		`{"entity_id":"lock.front_door","action":"lock.unlock","data":{"code":"1234"}}`,
		`{"entity_id":"lock.guest_room_door","action":"lock.unlock"}`,
		`{"entity_id":"script.let_me_in","action":"script.run","data":{"variables":{"guest_link":"x"}}}`,
		`{"entity_id":"script.let_me_in","action":"script.run"}`,
		`{"entity_id":"media_player.living","action":"media_player.volume_set","data":{"volume_level":0.5}}`,
		`{"entity_id":"media_player.living","action":"media_player.volume_mute","data":{"is_volume_muted":true}}`,
		`{"entity_id":"fan.guest","action":"fan.set_percentage","data":{"percentage":1e400}}`,
		`{"entity_id":"light.*","action":"light.toggle"}`,
		`{"entity_id":["light.guest_ceiling"],"action":"light.turn_on"}`,
		`{"entity_id":"light.guest_lamp","action":"light.toggle","data":[]}`,
		`{}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s), false)
		f.Add([]byte(s), true)
	}
	g := guestGrants(f)
	st := &model.State{EntityID: "light.guest_ceiling", Attributes: map[string]any{
		"min_color_temp_kelvin": 2700.0, "max_color_temp_kelvin": 6500.0,
	}}
	f.Fuzz(func(t *testing.T, raw []byte, withState bool) {
		var req Request
		if err := json.Unmarshal(raw, &req); err != nil {
			return
		}
		var s *model.State
		if withState {
			s = st
		}
		src, call, err := Check(g, req, s, localOff)
		if err != nil {
			if !reflect0(call) {
				t.Fatalf("error %v with call %+v", err, call)
			}
			return
		}
		grant, ok := g[GrantKey{EntityID: req.EntityID, Action: req.Action}]
		if !ok {
			t.Fatalf("call for ungranted pair %q %q", req.EntityID, req.Action)
		}
		if call.EntityID != req.EntityID || call.EntityID != grant.EntityID {
			t.Fatalf("call targets %q, request %q, grant %q", call.EntityID, req.EntityID, grant.EntityID)
		}
		if src.Access.Local() {
			t.Fatal("local source passed while local is off")
		}
		act, ok := catalog.Lookup(req.Action)
		if !ok || call.Domain != act.Domain || call.Service != act.Service {
			t.Fatalf("call %s.%s for action %q", call.Domain, call.Service, req.Action)
		}
		if model.Domain(call.EntityID) != model.Domain(act.ID) {
			t.Fatalf("domain mismatch: %q %q", call.EntityID, act.ID)
		}
		for k, v := range call.Data {
			if slices.Contains(targetKeys, k) || k == "code" {
				t.Fatalf("call data has %q", k)
			}
			if k == "variables" && act.ID == model.ActionScriptRun {
				continue
			}
			fd, ok := act.Data[k]
			if !ok {
				t.Fatalf("call data key %q not in catalog for %s", k, act.ID)
			}
			switch fd.Kind {
			case catalog.KindInt:
				n, ok := v.(int)
				if !ok || float64(n) < fd.Min || float64(n) > fd.Max {
					t.Fatalf("%s = %#v out of %v..%v", k, v, fd.Min, fd.Max)
				}
			case catalog.KindFloat:
				x, ok := v.(float64)
				if !ok || math.IsNaN(x) || x < fd.Min || x > fd.Max {
					t.Fatalf("%s = %#v out of %v..%v", k, v, fd.Min, fd.Max)
				}
			case catalog.KindBool:
				if _, ok := v.(bool); !ok {
					t.Fatalf("%s = %#v", k, v)
				}
			}
		}
		if act.ID == model.ActionScriptRun {
			if len(req.Data) != 0 {
				t.Fatal("script.run passed guest data")
			}
			vars, _ := call.Data["variables"].(map[string]any)
			if vars["guest_link"] != "Anna" {
				t.Fatalf("variables = %v", call.Data["variables"])
			}
		}
	})
}

func reflect0(c Call) bool {
	return c.Domain == "" && c.Service == "" && c.EntityID == "" && c.Data == nil
}
