package authz

import (
	"errors"
	"reflect"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// coverHouse is house() plus covers:
//   - cover.guest_room_shade sits in the guest room, which the "room"
//     template grants for light and fan actions only;
//   - cover.living_blind is granted tilt actions only;
//   - cover.living_curtain sits in the living room, which "lights"
//     grants for light actions only;
//   - cover.garage is granted open, close and position.
func coverHouse() model.Inventory {
	inv := house()
	for _, e := range []model.Entity{
		{ID: "cover.guest_room_shade", AreaID: "guest_room"},
		{ID: "cover.living_blind", AreaID: "living_room"},
		{ID: "cover.living_curtain", AreaID: "living_room"},
		{ID: "cover.garage"},
	} {
		inv.Entities[e.ID] = e
	}
	return inv
}

func coverGrants(t testing.TB) Grants {
	t.Helper()
	tpl := append(guestTemplates(),
		model.Template{ID: "lights", Name: "Living room lights", Rules: []model.Rule{{
			Select:  model.Selector{Areas: []string{"living_room"}},
			Actions: []string{"light.turn_on"},
		}}},
		model.Template{ID: "covers", Name: "Covers", Rules: []model.Rule{
			{Select: model.Selector{Entities: []string{"cover.living_blind"}},
				Actions: []string{"cover.open_cover_tilt", "cover.close_cover_tilt", "cover.set_cover_tilt_position"}},
			{Select: model.Selector{Entities: []string{"cover.garage"}},
				Actions: []string{"cover.open_cover", "cover.close_cover", "cover.set_cover_position"}},
		}},
	)
	l := guestLink()
	l.TemplateIDs = append(l.TemplateIDs, "lights", "covers")
	g, _ := Resolve(l, tpl, coverHouse())
	return g
}

// TestCheckCoverEscapes runs the escape table for covers. The cover
// domain adds no special code: the domain filter, the data checks and
// the one-entity target catch every row.
func TestCheckCoverEscapes(t *testing.T) {
	g := coverGrants(t)
	pos := func(v any) map[string]any { return map[string]any{"position": v} }
	tests := []struct {
		name   string
		entity string
		action string
		data   map[string]any
		want   error
	}{
		{"position 101", "cover.garage", "cover.set_cover_position", pos(101.0), ErrBadData},
		{"position -1", "cover.garage", "cover.set_cover_position", pos(-1.0), ErrBadData},
		{"position 50.5", "cover.garage", "cover.set_cover_position", pos(50.5), ErrBadData},
		{"position as a string", "cover.garage", "cover.set_cover_position", pos("50"), ErrBadData},
		{"position as a bool", "cover.garage", "cover.set_cover_position", pos(true), ErrBadData},
		{"position null", "cover.garage", "cover.set_cover_position", pos(nil), ErrBadData},
		{"tilt key on the position action", "cover.garage", "cover.set_cover_position", map[string]any{"tilt_position": 50.0}, ErrBadData},
		{"position key on the tilt action", "cover.living_blind", "cover.set_cover_tilt_position", pos(50.0), ErrBadData},
		{"tilt 101", "cover.living_blind", "cover.set_cover_tilt_position", map[string]any{"tilt_position": 101.0}, ErrBadData},
		{"speed is not a guest field", "cover.garage", "cover.open_cover", map[string]any{"speed": "fast"}, ErrBadData},
		{"entity_id in the data", "cover.garage", "cover.set_cover_position", map[string]any{"position": 50.0, "entity_id": "cover.living_curtain"}, ErrBadData},
		{"entity_id on open", "cover.garage", "cover.open_cover", map[string]any{"entity_id": "cover.living_curtain"}, ErrBadData},
		{"area_id in the data", "cover.garage", "cover.close_cover", map[string]any{"area_id": "living_room"}, ErrBadData},

		{"tilt granted, position not", "cover.living_blind", "cover.set_cover_position", pos(50.0), ErrForbidden},
		{"tilt granted, open not", "cover.living_blind", "cover.open_cover", nil, ErrForbidden},
		{"stop not granted", "cover.garage", "cover.stop_cover", nil, ErrForbidden},
		{"cover in a granted area without cover actions", "cover.guest_room_shade", "cover.open_cover", nil, ErrForbidden},
		{"granted area action on its cover", "cover.guest_room_shade", "fan.set_percentage", map[string]any{"percentage": 50.0}, ErrForbidden},
		{"cover in an area with only light actions", "cover.living_curtain", "cover.open_cover", nil, ErrForbidden},
		{"light action on a cover", "cover.living_curtain", "light.turn_on", nil, ErrForbidden},
		{"cover action on a light", "light.guest_ceiling", "cover.open_cover", nil, ErrForbidden},
		{"service toggle is not in the catalog", "cover.garage", "cover.toggle", nil, ErrForbidden},
		{"entity list", "cover.garage,cover.living_curtain", "cover.open_cover", nil, ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, call, err := Check(g, Request{EntityID: tt.entity, Action: tt.action, Data: tt.data}, nil, allowAll)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(call, Call{}) || !reflect.DeepEqual(src, Source{}) {
				t.Fatalf("got call %+v, source %+v with error", call, src)
			}
		})
	}
}

func TestCheckCoverBuildsCall(t *testing.T) {
	g := coverGrants(t)
	tests := []struct {
		name string
		req  Request
		want Call
	}{
		{"position is an int", Request{EntityID: "cover.garage", Action: "cover.set_cover_position", Data: map[string]any{"position": 40.0}},
			Call{Domain: "cover", Service: "set_cover_position", EntityID: "cover.garage", Data: map[string]any{"position": 40}}},
		{"position bounds are inclusive", Request{EntityID: "cover.garage", Action: "cover.set_cover_position", Data: map[string]any{"position": 0.0}},
			Call{Domain: "cover", Service: "set_cover_position", EntityID: "cover.garage", Data: map[string]any{"position": 0}}},
		{"tilt", Request{EntityID: "cover.living_blind", Action: "cover.set_cover_tilt_position", Data: map[string]any{"tilt_position": 100.0}},
			Call{Domain: "cover", Service: "set_cover_tilt_position", EntityID: "cover.living_blind", Data: map[string]any{"tilt_position": 100}}},
		{"open", Request{EntityID: "cover.garage", Action: "cover.open_cover"},
			Call{Domain: "cover", Service: "open_cover", EntityID: "cover.garage", Data: map[string]any{}}},
		{"close tilt", Request{EntityID: "cover.living_blind", Action: "cover.close_cover_tilt"},
			Call{Domain: "cover", Service: "close_cover_tilt", EntityID: "cover.living_blind", Data: map[string]any{}}},
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
	// The domain filter keeps the covers that no cover action reaches.
	for k := range g {
		if k.EntityID == "cover.guest_room_shade" || k.EntityID == "cover.living_curtain" {
			t.Errorf("granted %+v", k)
		}
	}
}
