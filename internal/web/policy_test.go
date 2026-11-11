package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// homeInv is an Inventory with a fixed home, or an error.
type homeInv struct {
	*harnessInv
	home model.Home
	err  error
}

func (i homeInv) Inventory(context.Context) (model.Inventory, error) {
	return model.Inventory{Home: i.home}, i.err
}

// harnessInv lends homeInv the rest of the Inventory methods.
type harnessInv struct{ Inventory }

// Saves run the real policy: near_home_m needs a home in HA. While HA
// can't be reached, saves are not blocked.
func TestPolicyNearHomeNeedsHome(t *testing.T) {
	near := model.Template{ID: "porch", Name: "Porch", Access: &model.Access{NearHomeM: 200}, Rules: []model.Rule{{
		Select: model.Selector{Entities: []string{"light.guest_room"}}, Actions: []string{"light.turn_on"},
	}}}
	for _, c := range []struct {
		name string
		inv  homeInv
		want int
	}{
		{"home set", homeInv{home: home}, 201},
		{"no home", homeInv{}, 400},
		{"HA away", homeInv{err: errors.New("down")}, 201},
	} {
		h := newHarness(t)
		c.inv.harnessInv = &harnessInv{h.srv.deps.Inventory}
		h.srv.deps.Inventory = c.inv
		rec := h.a(req{method: "POST", path: "/api/templates", body: near})
		if rec.Code != c.want {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		if c.want == 400 && !strings.Contains(rec.Body.String(), "templates[0].access.near_home_m") {
			t.Errorf("%s: problem path: %s", c.name, rec.Body)
		}
	}
}

// A template save is checked alone: a stored template that breaks the
// policy (the office one uses local while it is off) does not block it.
func TestPolicyChecksOnlySavedTemplate(t *testing.T) {
	h := newHarness(t)
	ok := model.Template{ID: "porch", Name: "Porch", Rules: []model.Rule{{
		Select: model.Selector{Entities: []string{"light.guest_room"}}, Actions: []string{"light.turn_on"},
	}}}
	if rec := h.a(req{method: "POST", path: "/api/templates", body: ok}); rec.Code != 201 {
		t.Errorf("save next to a bad stored template: %d %s", rec.Code, rec.Body)
	}
}
