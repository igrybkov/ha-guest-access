package catalog

import (
	"slices"
	"strings"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

func TestLookup(t *testing.T) {
	a, ok := Lookup("lock.lock")
	if !ok || a.Domain != "lock" || a.Service != "lock" {
		t.Fatalf("Lookup(lock.lock) = %+v, %v", a, ok)
	}
	for _, id := range []string{"", "lock", "lock.LOCK", " lock.lock", "lock.lock ", "lock.*", "homeassistant.restart", "script.turn_on", "lock.set_code"} {
		if _, ok := Lookup(id); ok {
			t.Errorf("Lookup(%q) found an action", id)
		}
	}
}

func TestPlanActions(t *testing.T) {
	want := map[string]string{
		"lock.lock":                           "lock.lock",
		"lock.unlock":                         "lock.unlock",
		"lock.open":                           "lock.open",
		"alarm_control_panel.alarm_disarm":    "alarm_control_panel.alarm_disarm",
		"alarm_control_panel.alarm_arm_home":  "alarm_control_panel.alarm_arm_home",
		"alarm_control_panel.alarm_arm_away":  "alarm_control_panel.alarm_arm_away",
		"alarm_control_panel.alarm_arm_night": "alarm_control_panel.alarm_arm_night",
		"light.turn_on":                       "light.turn_on",
		"light.turn_off":                      "light.turn_off",
		"light.toggle":                        "light.toggle",
		"fan.turn_on":                         "fan.turn_on",
		"fan.turn_off":                        "fan.turn_off",
		"fan.set_percentage":                  "fan.set_percentage",
		"media_player.media_play_pause":       "media_player.media_play_pause",
		"media_player.media_next_track":       "media_player.media_next_track",
		"media_player.media_previous_track":   "media_player.media_previous_track",
		"media_player.volume_set":             "media_player.volume_set",
		"media_player.volume_mute":            "media_player.volume_mute",
		"switch.turn_on":                      "switch.turn_on",
		"switch.turn_off":                     "switch.turn_off",
		"script.run":                          "script.turn_on",
		"script.cancel":                       "script.turn_off",
		"cover.open_cover":                    "cover.open_cover",
		"cover.close_cover":                   "cover.close_cover",
		"cover.stop_cover":                    "cover.stop_cover",
		"cover.set_cover_position":            "cover.set_cover_position",
		"cover.open_cover_tilt":               "cover.open_cover_tilt",
		"cover.close_cover_tilt":              "cover.close_cover_tilt",
		"cover.stop_cover_tilt":               "cover.stop_cover_tilt",
		"cover.set_cover_tilt_position":       "cover.set_cover_tilt_position",
	}
	if n := len(All()); n != len(want) {
		t.Errorf("catalog has %d actions, want %d", n, len(want))
	}
	for id, call := range want {
		a, ok := Lookup(id)
		if !ok {
			t.Errorf("%s missing", id)
			continue
		}
		if got := a.Domain + "." + a.Service; got != call {
			t.Errorf("%s calls %s, want %s", id, got, call)
		}
	}
}

func TestDataFields(t *testing.T) {
	tests := []struct {
		action, key string
		want        Field
	}{
		{"light.turn_on", "brightness_pct", Field{Kind: KindInt, Min: 1, Max: 100}},
		{"light.turn_on", "color_temp_kelvin", Field{Kind: KindInt, Min: 1500, Max: 9000, MinAttr: "min_color_temp_kelvin", MaxAttr: "max_color_temp_kelvin"}},
		{"fan.set_percentage", "percentage", Field{Kind: KindInt, Min: 0, Max: 100}},
		{"media_player.volume_set", "volume_level", Field{Kind: KindFloat, Min: 0, Max: 1}},
		{"media_player.volume_mute", "is_volume_muted", Field{Kind: KindBool}},
		{"cover.set_cover_position", "position", Field{Kind: KindInt, Min: 0, Max: 100}},
		{"cover.set_cover_tilt_position", "tilt_position", Field{Kind: KindInt, Min: 0, Max: 100}},
	}
	for _, tt := range tests {
		a, _ := Lookup(tt.action)
		got, ok := a.Data[tt.key]
		if !ok {
			t.Errorf("%s has no %s", tt.action, tt.key)
			continue
		}
		if got.Kind != tt.want.Kind || got.Min != tt.want.Min || got.Max != tt.want.Max ||
			got.MinAttr != tt.want.MinAttr || got.MaxAttr != tt.want.MaxAttr {
			t.Errorf("%s.%s = %+v, want %+v", tt.action, tt.key, got, tt.want)
		}
	}
	for _, id := range []string{"script.run", "script.cancel", "lock.unlock", "lock.open", "alarm_control_panel.alarm_disarm",
		"cover.open_cover", "cover.close_cover", "cover.stop_cover",
		"cover.open_cover_tilt", "cover.close_cover_tilt", "cover.stop_cover_tilt"} {
		if a, _ := Lookup(id); len(a.Data) != 0 {
			t.Errorf("%s takes guest data %v", id, a.Data)
		}
	}
}

func TestEveryActionIsConsistent(t *testing.T) {
	for _, a := range All() {
		if model.Domain(a.ID) == "" || a.Domain == "" || a.Service == "" || a.UI == "" {
			t.Errorf("%s: incomplete %+v", a.ID, a)
		}
		// The id's domain is the entity domain the domain filter uses. For
		// every action we have, the HA service domain is the same.
		if model.Domain(a.ID) != a.Domain {
			t.Errorf("%s: service domain %q does not match the id", a.ID, a.Domain)
		}
		if _, ok := attributes[a.Domain]; !ok {
			t.Errorf("%s: domain has no attribute allowlist", a.ID)
		}
		for k, f := range a.Data {
			switch f.Kind {
			case KindBool:
			case KindInt, KindFloat:
				if f.Min > f.Max {
					t.Errorf("%s.%s: min %v > max %v", a.ID, k, f.Min, f.Max)
				}
			case KindString:
				if len(f.Enum) == 0 {
					t.Errorf("%s.%s: string field without enum", a.ID, k)
				}
			default:
				t.Errorf("%s.%s: unknown kind %q", a.ID, k, f.Kind)
			}
			if strings.Contains(strings.ToLower(k), "code") {
				t.Errorf("%s: has a code field %q", a.ID, k)
			}
			switch k {
			case "entity_id", "area_id", "device_id", "label_id", "floor_id", "variables":
				t.Errorf("%s: data field %q is reserved", a.ID, k)
			}
		}
	}
}

func TestLookupReturnsCopies(t *testing.T) {
	a, _ := Lookup("light.turn_on")
	delete(a.Data, "brightness_pct")
	a.Data["code"] = Field{Kind: KindBool}
	b, _ := Lookup("light.turn_on")
	if _, ok := b.Data["brightness_pct"]; !ok {
		t.Fatal("caller deleted a field from the table")
	}
	if _, ok := b.Data["code"]; ok {
		t.Fatal("caller added a field to the table")
	}
}

func TestDomainsAndAttributes(t *testing.T) {
	want := []string{"alarm_control_panel", "cover", "fan", "light", "lock", "media_player", "script", "switch"}
	if d := Domains(); !slices.Equal(d, want) {
		t.Fatalf("Domains = %v, want %v", d, want)
	}
	if got := AttributeAllowlist("nope"); got != nil {
		t.Fatalf("unknown domain attributes = %v", got)
	}
	l := AttributeAllowlist("light")
	l[0] = "entity_picture"
	if slices.Contains(AttributeAllowlist("light"), "entity_picture") {
		t.Fatal("caller changed the allowlist")
	}
}

// forbidden are attributes that leak tokens, people, places or codes.
var forbidden = []string{
	"entity_picture", "entity_picture_local", "media_image_url", "access_token", "token",
	"code", "code_format", "code_arm_required", "changed_by", "user", "user_id", "user_name",
	"context", "gps", "gps_accuracy", "latitude", "longitude", "location", "source",
}

func TestAllowlistsNeverLeak(t *testing.T) {
	for d, list := range attributes {
		for _, a := range list {
			if slices.Contains(forbidden, a) {
				t.Errorf("%s allows %q", d, a)
			}
			low := strings.ToLower(a)
			for _, bad := range []string{"token", "code", "picture", "image", "user", "gps", "latitude", "longitude"} {
				if strings.Contains(low, bad) {
					t.Errorf("%s allows %q (contains %q)", d, a, bad)
				}
			}
		}
	}
}

func TestFilterState(t *testing.T) {
	modes := []any{"color_temp", "brightness"}
	st := model.State{
		EntityID: "light.guest",
		State:    "on",
		Attributes: map[string]any{
			"friendly_name":         "Guest",
			"brightness":            200.0,
			"supported_color_modes": modes,
			"entity_picture":        "/api/camera_proxy/x?token=secret",
			"access_token":          "secret",
			"latitude":              1.0,
		},
	}
	got := FilterState(st)
	if got.State != "on" || got.EntityID != "light.guest" {
		t.Fatalf("state lost: %+v", got)
	}
	if len(got.Attributes) != 3 {
		t.Fatalf("attributes = %v", got.Attributes)
	}
	for _, k := range []string{"entity_picture", "access_token", "latitude"} {
		if _, ok := got.Attributes[k]; ok {
			t.Errorf("%s leaked", k)
		}
	}
	// The result shares nothing with the input.
	got.Attributes["supported_color_modes"].([]any)[0] = "x"
	if modes[0] != "color_temp" {
		t.Fatal("FilterState aliased a slice")
	}
	got.Attributes["friendly_name"] = "changed"
	if st.Attributes["friendly_name"] != "Guest" {
		t.Fatal("FilterState aliased the map")
	}

	lock := FilterState(model.State{EntityID: "lock.front", State: "locked", Attributes: map[string]any{
		"friendly_name": "Front", "changed_by": "Illia", "code_format": `^\d{4}$`,
	}})
	if lock.State != "locked" || len(lock.Attributes) != 1 {
		t.Fatalf("lock = %+v", lock)
	}

	// A cover keeps its position, tilt and class. Speed, is_closed and
	// anything else HA or an integration adds are dropped.
	cover := FilterState(model.State{EntityID: "cover.garage", State: "opening", Attributes: map[string]any{
		"friendly_name": "Garage", "supported_features": 15.0, "device_class": "garage",
		"current_position": 40.0, "current_tilt_position": 10.0,
		"is_closed": false, "speed": "fast", "supported_speeds": []any{"fast"}, "assumed_state": true,
		"entity_picture": "/api/camera_proxy/x?token=secret", "user_id": "u1", "changed_by": "Illia",
	}})
	wantCover := []string{"current_position", "current_tilt_position", "device_class", "friendly_name", "supported_features"}
	var gotCover []string
	for k := range cover.Attributes {
		gotCover = append(gotCover, k)
	}
	slices.Sort(gotCover)
	if cover.State != "opening" || !slices.Equal(gotCover, wantCover) {
		t.Fatalf("cover = %+v", cover)
	}

	unknown := FilterState(model.State{EntityID: "person.illia", State: "home", Attributes: map[string]any{
		"friendly_name": "Illia", "latitude": 1.0,
	}})
	if unknown.Attributes == nil || len(unknown.Attributes) != 0 {
		t.Fatalf("unknown domain kept %v", unknown.Attributes)
	}
}

// Exactly the slider actions are continuous.
func TestContinuousActions(t *testing.T) {
	want := map[string]bool{
		"light.turn_on": true, "fan.set_percentage": true, "media_player.volume_set": true,
		"cover.set_cover_position": true, "cover.set_cover_tilt_position": true,
	}
	for _, a := range All() {
		if a.Continuous != want[a.ID] {
			t.Errorf("%s: continuous %v", a.ID, a.Continuous)
		}
	}
}
