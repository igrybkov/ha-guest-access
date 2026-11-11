package authz

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

func TestPickFirstAllowedSource(t *testing.T) {
	g := Grant{EntityID: "lock.front_door", Action: "lock.unlock", Sources: []Source{
		{Template: "music", Access: model.Access{Network: model.NetworkLocal}},
		{Template: "house_entry", Rule: 1, Access: model.Access{NearHomeM: 300}},
	}}
	notLocal := func(a model.Access) bool { return !a.Local() }
	s, ok := g.Pick(notLocal)
	if !ok || s.Template != "house_entry" || s.Rule != 1 {
		t.Fatalf("Pick = %+v, %v", s, ok)
	}
	if _, ok := g.Pick(func(model.Access) bool { return false }); ok {
		t.Fatal("Pick passed with every source denied")
	}
	if _, ok := (Grant{}).Pick(func(model.Access) bool { return true }); ok {
		t.Fatal("Pick passed with no sources")
	}
}

func guestGrants(t testing.TB) Grants {
	t.Helper()
	g, _ := Resolve(guestLink(), guestTemplates(), house())
	if len(g) == 0 {
		t.Fatal("fixture grants nothing")
	}
	return g
}

// TestCheckEscapes is the escape table. Every row is an attack, and every
// row must fail with the given error and a zero Call.
func TestCheckEscapes(t *testing.T) {
	denyAll := func(model.Access) bool { return false }
	notNear := func(a model.Access) bool { return a.NearHomeM == 0 && !a.Local() }
	g := guestGrants(t)
	// A pair for an action the catalog lacks, as if a bug let one in.
	g[GrantKey{EntityID: "light.guest_ceiling", Action: "light.set_anything"}] = Grant{
		EntityID: "light.guest_ceiling", Action: "light.set_anything", Sources: []Source{{Template: "room"}},
	}
	// A pair whose action domain is not the entity domain.
	g[GrantKey{EntityID: "lock.guest_room_door", Action: "light.turn_on"}] = Grant{
		EntityID: "lock.guest_room_door", Action: "light.turn_on", Sources: []Source{{Template: "room"}},
	}

	tests := []struct {
		name   string
		entity string
		action string
		data   map[string]any
		allow  AccessFunc
		want   error
	}{
		{"entity not granted", "light.office", "light.turn_on", nil, allowAll, ErrForbidden},
		{"entity not in house", "light.nope", "light.turn_on", nil, allowAll, ErrForbidden},
		{"lock in granted area, light actions only", "lock.guest_room_door", "lock.unlock", nil, allowAll, ErrForbidden},
		{"lock in granted area, light action", "lock.guest_room_door", "light.turn_on", nil, allowAll, ErrForbidden},
		{"hidden light in granted area", "light.guest_hidden", "light.turn_on", nil, allowAll, ErrForbidden},
		{"config switch in granted area", "switch.guest_config", "switch.turn_on", nil, allowAll, ErrForbidden},
		{"action granted for another entity", "lock.front_door", "lock.lock", nil, allowAll, ErrForbidden},
		{"open is not granted with unlock", "lock.front_door", "lock.open", nil, allowAll, ErrForbidden},
		{"fan action on a light", "light.guest_ceiling", "fan.set_percentage", map[string]any{"percentage": 50.0}, allowAll, ErrForbidden},
		{"script not granted", "script.im_leaving", "script.run", nil, allowAll, ErrForbidden},
		{"upper case entity", "Light.guest_ceiling", "light.turn_on", nil, allowAll, ErrForbidden},
		{"mixed case entity", "light.Guest_Ceiling", "light.turn_on", nil, allowAll, ErrForbidden},
		{"upper case action", "light.guest_ceiling", "light.TURN_ON", nil, allowAll, ErrForbidden},
		{"leading space entity", " light.guest_ceiling", "light.turn_on", nil, allowAll, ErrForbidden},
		{"trailing space entity", "light.guest_ceiling ", "light.turn_on", nil, allowAll, ErrForbidden},
		{"trailing newline entity", "light.guest_ceiling\n", "light.turn_on", nil, allowAll, ErrForbidden},
		{"tab before action", "light.guest_ceiling", "\tlight.turn_on", nil, allowAll, ErrForbidden},
		{"trailing space action", "light.guest_ceiling", "light.turn_on ", nil, allowAll, ErrForbidden},
		{"NUL after entity", "light.guest_ceiling\x00", "light.turn_on", nil, allowAll, ErrForbidden},
		{"wildcard entity against wildcard grant", "light.*", "light.toggle", nil, allowAll, ErrForbidden},
		{"the grant pattern itself", "light.guest_*", "light.toggle", nil, allowAll, ErrForbidden},
		{"wildcard action", "light.guest_ceiling", "light.*", nil, allowAll, ErrForbidden},
		{"entity list", "light.guest_ceiling,light.office", "light.turn_on", nil, allowAll, ErrForbidden},
		{"area as entity", "guest_room", "light.turn_on", nil, allowAll, ErrForbidden},
		{"cyrillic i lookalike", "light.guest_ceil\u0456ng", "light.turn_on", nil, allowAll, ErrForbidden},
		{"zero width space", "light.guest\u200b_ceiling", "light.turn_on", nil, allowAll, ErrForbidden},
		{"fullwidth dot", "light\uff0eguest_ceiling", "light.turn_on", nil, allowAll, ErrForbidden},
		{"action not in catalog", "light.guest_ceiling", "light.set_anything", nil, allowAll, ErrForbidden},
		{"service name, not action id", "script.let_me_in", "script.turn_on", nil, allowAll, ErrForbidden},
		{"admin service", "light.guest_ceiling", "homeassistant.restart", nil, allowAll, ErrForbidden},
		{"empty request", "", "", nil, allowAll, ErrForbidden},
		{"empty action", "light.guest_ceiling", "", nil, allowAll, ErrForbidden},
		{"access func denies", "light.guest_ceiling", "light.turn_on", nil, denyAll, ErrForbidden},
		{"nil access func", "light.guest_ceiling", "light.turn_on", nil, nil, ErrForbidden},
		{"not near home", "lock.front_door", "lock.unlock", nil, notNear, ErrForbidden},
		{"local rule while local is off", "media_player.living", "media_player.volume_set", map[string]any{"volume_level": 0.5}, localOff, ErrForbidden},
		{"forbidden beats bad data", "light.office", "light.turn_on", map[string]any{"entity_id": "light.office"}, allowAll, ErrForbidden},
		{"denied access beats bad data", "light.guest_ceiling", "light.turn_on", map[string]any{"nope": 1.0}, denyAll, ErrForbidden},

		{"unknown data key", "light.guest_ceiling", "light.turn_on", map[string]any{"transition": 1.0}, allowAll, ErrBadData},
		{"HA attribute name, not field", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness": 255.0}, allowAll, ErrBadData},
		{"data on an action with no fields", "lock.front_door", "lock.unlock", map[string]any{"x": 1.0}, allowAll, ErrBadData},
		{"entity_id in data", "light.guest_ceiling", "light.turn_on", map[string]any{"entity_id": "lock.front_door"}, allowAll, ErrBadData},
		{"entity_id list in data", "light.guest_ceiling", "light.turn_on", map[string]any{"entity_id": []any{"light.office"}}, allowAll, ErrBadData},
		{"area_id in data", "light.guest_ceiling", "light.turn_on", map[string]any{"area_id": "office"}, allowAll, ErrBadData},
		{"device_id in data", "light.guest_ceiling", "light.turn_on", map[string]any{"device_id": "dev_office"}, allowAll, ErrBadData},
		{"label_id in data", "light.guest_ceiling", "light.turn_on", map[string]any{"label_id": "private"}, allowAll, ErrBadData},
		{"floor_id in data", "light.guest_ceiling", "light.turn_on", map[string]any{"floor_id": "upstairs"}, allowAll, ErrBadData},
		{"good key plus target key", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": 50.0, "entity_id": "light.office"}, allowAll, ErrBadData},
		{"code in data", "lock.front_door", "lock.unlock", map[string]any{"code": "1234"}, allowAll, ErrBadData},
		{"array value", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": []any{50.0}}, allowAll, ErrBadData},
		{"object value", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": map[string]any{"v": 50.0}}, allowAll, ErrBadData},
		{"null value", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": nil}, allowAll, ErrBadData},
		{"string number", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": "50"}, allowAll, ErrBadData},
		{"bool as number", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": true}, allowAll, ErrBadData},
		{"number as bool", "media_player.living", "media_player.volume_mute", map[string]any{"is_volume_muted": 1.0}, allowAll, ErrBadData},
		{"string as bool", "media_player.living", "media_player.volume_mute", map[string]any{"is_volume_muted": "true"}, allowAll, ErrBadData},
		{"Go int, not a JSON number", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": 50}, allowAll, ErrBadData},
		{"below range", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": 0.0}, allowAll, ErrBadData},
		{"above range", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": 101.0}, allowAll, ErrBadData},
		{"negative", "fan.guest", "fan.set_percentage", map[string]any{"percentage": -1.0}, allowAll, ErrBadData},
		{"huge", "fan.guest", "fan.set_percentage", map[string]any{"percentage": 1e300}, allowAll, ErrBadData},
		{"fraction for int", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": 50.5}, allowAll, ErrBadData},
		{"NaN", "light.guest_ceiling", "light.turn_on", map[string]any{"brightness_pct": math.NaN()}, allowAll, ErrBadData},
		{"+Inf", "media_player.living", "media_player.volume_set", map[string]any{"volume_level": math.Inf(1)}, allowAll, ErrBadData},
		{"-Inf", "media_player.living", "media_player.volume_set", map[string]any{"volume_level": math.Inf(-1)}, allowAll, ErrBadData},
		{"json.Number NaN", "media_player.living", "media_player.volume_set", map[string]any{"volume_level": json.Number("NaN")}, allowAll, ErrBadData},
		{"json.Number junk", "media_player.living", "media_player.volume_set", map[string]any{"volume_level": json.Number("0.5x")}, allowAll, ErrBadData},
		{"volume above 1", "media_player.living", "media_player.volume_set", map[string]any{"volume_level": 1.01}, allowAll, ErrBadData},
		{"kelvin below fixed range", "light.guest_ceiling", "light.turn_on", map[string]any{"color_temp_kelvin": 1000.0}, allowAll, ErrBadData},
		{"kelvin above fixed range", "light.guest_ceiling", "light.turn_on", map[string]any{"color_temp_kelvin": 10000.0}, allowAll, ErrBadData},
		{"script.run with variables", "script.let_me_in", "script.run", map[string]any{"variables": map[string]any{"x": 1.0}}, allowAll, ErrBadData},
		{"script.run with any data", "script.let_me_in", "script.run", map[string]any{"guest_link": "Mallory"}, allowAll, ErrBadData},
		{"script.cancel with data", "script.let_me_in", "script.cancel", map[string]any{"x": 1.0}, allowAll, ErrBadData},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, call, err := Check(g, Request{EntityID: tt.entity, Action: tt.action, Data: tt.data}, nil, tt.allow)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(call, Call{}) || !reflect.DeepEqual(src, Source{}) {
				t.Fatalf("got call %+v, source %+v with error", call, src)
			}
		})
	}
}

func TestCheckBuildsCall(t *testing.T) {
	g := guestGrants(t)
	tests := []struct {
		name string
		req  Request
		want Call
	}{
		{"no data", Request{EntityID: "light.guest_ceiling", Action: "light.turn_off"},
			Call{Domain: "light", Service: "turn_off", EntityID: "light.guest_ceiling", Data: map[string]any{}}},
		{"int fields become int", Request{EntityID: "light.guest_ceiling", Action: "light.turn_on", Data: map[string]any{"brightness_pct": 40.0, "color_temp_kelvin": 2700.0}},
			Call{Domain: "light", Service: "turn_on", EntityID: "light.guest_ceiling", Data: map[string]any{"brightness_pct": 40, "color_temp_kelvin": 2700}}},
		{"bounds are inclusive", Request{EntityID: "fan.guest", Action: "fan.set_percentage", Data: map[string]any{"percentage": 0.0}},
			Call{Domain: "fan", Service: "set_percentage", EntityID: "fan.guest", Data: map[string]any{"percentage": 0}}},
		{"json.Number", Request{EntityID: "fan.guest", Action: "fan.set_percentage", Data: map[string]any{"percentage": json.Number("100")}},
			Call{Domain: "fan", Service: "set_percentage", EntityID: "fan.guest", Data: map[string]any{"percentage": 100}}},
		{"float field", Request{EntityID: "media_player.living", Action: "media_player.volume_set", Data: map[string]any{"volume_level": 0.25}},
			Call{Domain: "media_player", Service: "volume_set", EntityID: "media_player.living", Data: map[string]any{"volume_level": 0.25}}},
		{"bool field", Request{EntityID: "media_player.living", Action: "media_player.volume_mute", Data: map[string]any{"is_volume_muted": true}},
			Call{Domain: "media_player", Service: "volume_mute", EntityID: "media_player.living", Data: map[string]any{"is_volume_muted": true}}},
		{"wildcard grant, exact request", Request{EntityID: "light.guest_lamp", Action: "light.toggle"},
			Call{Domain: "light", Service: "toggle", EntityID: "light.guest_lamp", Data: map[string]any{}}},
		{"script.run sends variables", Request{EntityID: "script.let_me_in", Action: "script.run", Data: map[string]any{}},
			Call{Domain: "script", Service: "turn_on", EntityID: "script.let_me_in", Data: map[string]any{"variables": map[string]any{
				"source": "guest_access", "nested": map[string]any{"a": []any{"b"}}, "guest_link": "Anna",
			}}}},
		{"script.cancel sends nothing", Request{EntityID: "script.let_me_in", Action: "script.cancel"},
			Call{Domain: "script", Service: "turn_off", EntityID: "script.let_me_in", Data: map[string]any{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, call, err := Check(g, tt.req, nil, allowAll)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(call, tt.want) {
				t.Fatalf("call = %#v\nwant %#v", call, tt.want)
			}
		})
	}
}

func TestCheckPicksSourceInOrder(t *testing.T) {
	g := guestGrants(t)
	src, _, err := Check(g, Request{EntityID: "lock.front_door", Action: "lock.unlock"}, nil, allowAll)
	if err != nil || src.Template != "entry" || src.Rule != 0 || src.Access.NearHomeM != 300 {
		t.Fatalf("source = %+v, %v", src, err)
	}
}

func TestCheckDoesNotAliasGrants(t *testing.T) {
	g := guestGrants(t)
	req := Request{EntityID: "script.let_me_in", Action: "script.run"}
	src, call, err := Check(g, req, nil, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	call.Data["variables"].(map[string]any)["guest_link"] = "Mallory"
	call.Data["variables"].(map[string]any)["nested"].(map[string]any)["a"].([]any)[0] = "x"
	src.Variables["guest_link"] = "Mallory"
	src2, call2, _ := Check(g, req, nil, allowAll)
	vars := call2.Data["variables"].(map[string]any)
	if vars["guest_link"] != "Anna" || src2.Variables["guest_link"] != "Anna" {
		t.Fatal("caller changed guest_link in the grants")
	}
	if vars["nested"].(map[string]any)["a"].([]any)[0] != "b" {
		t.Fatal("caller changed nested variables in the grants")
	}
}

func TestCheckColorTempBounds(t *testing.T) {
	g := guestGrants(t)
	st := &model.State{EntityID: "light.guest_ceiling", Attributes: map[string]any{
		"min_color_temp_kelvin": 2700.0, "max_color_temp_kelvin": 6500,
	}}
	tests := []struct {
		name string
		st   *model.State
		k    float64
		ok   bool
	}{
		{"inside light range", st, 3000, true},
		{"at light min", st, 2700, true},
		{"at light max (int attribute)", st, 6500, true},
		{"below light min", st, 2000, false},
		{"above light max", st, 7000, false},
		{"fixed bounds without state", nil, 2000, true},
		{"fixed bounds still apply", nil, 1499, false},
		{"state of another entity is ignored", &model.State{EntityID: "light.office", Attributes: st.Attributes}, 2000, true},
		{"attributes never widen", &model.State{EntityID: "light.guest_ceiling", Attributes: map[string]any{
			"min_color_temp_kelvin": 1000.0, "max_color_temp_kelvin": 20000.0}}, 1000, false},
		{"bad attribute keeps fixed bound", &model.State{EntityID: "light.guest_ceiling", Attributes: map[string]any{
			"min_color_temp_kelvin": "x", "max_color_temp_kelvin": math.NaN()}}, 8000, true},
		{"crossed bounds refuse all", &model.State{EntityID: "light.guest_ceiling", Attributes: map[string]any{
			"min_color_temp_kelvin": 6000.0, "max_color_temp_kelvin": 3000.0}}, 4000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := Request{EntityID: "light.guest_ceiling", Action: "light.turn_on", Data: map[string]any{"color_temp_kelvin": tt.k}}
			_, _, err := Check(g, req, tt.st, allowAll)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if err != nil && !errors.Is(err, ErrBadData) {
				t.Fatalf("err = %v, want ErrBadData", err)
			}
		})
	}
}

func TestCheckLongKeyIsCut(t *testing.T) {
	g := guestGrants(t)
	long := string(make([]byte, 10_000))
	_, _, err := Check(g, Request{EntityID: "light.guest_ceiling", Action: "light.turn_on", Data: map[string]any{long: 1.0}}, nil, allowAll)
	if !errors.Is(err, ErrBadData) || len(err.Error()) > 500 {
		t.Fatalf("err = %.100v (len %d)", err, len(err.Error()))
	}
}
