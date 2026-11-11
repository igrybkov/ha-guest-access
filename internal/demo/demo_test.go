package demo

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/authz"
	"github.com/igrybkov/ha-guest-access/internal/inventory"
)

// The seeded link grants the shared rooms, the guest room, the door and
// the scripts. It leaves out the private, hidden, disabled and
// diagnostic entities, as HA's target expansion does.
func TestSeededLinkGrants(t *testing.T) {
	for _, tpl := range Templates() {
		if err := tpl.Validate(); err != nil {
			t.Fatalf("template %s: %v", tpl.ID, err)
		}
	}
	inv, err := inventory.New(House(), slog.New(slog.DiscardHandler)).Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	grants, warns := authz.Resolve(Link(time.Now()), Templates(), inv)
	if len(warns) != 0 {
		t.Errorf("warnings: %+v", warns)
	}
	got := map[string]bool{}
	for k := range grants {
		got[k.EntityID] = true
	}
	want := []string{
		"alarm_control_panel.home", "fan.guest_room", "light.guest_room", "light.kitchen", "light.living_room",
		"lock.front_door", "media_player.living_room", "script.im_leaving", "script.let_me_in",
	}
	var ids []string
	for id := range got {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, want) {
		t.Errorf("granted %v\nwant    %v", ids, want)
	}
}

// Every entity with a state is in the registry, and the disabled one has
// no state, as in HA.
func TestHouseIsConsistent(t *testing.T) {
	f := House()
	reg := map[string]bool{}
	for _, e := range f.EntityList {
		reg[e.EntityID] = true
	}
	for _, st := range f.StateList {
		if !reg[st.EntityID] {
			t.Errorf("state for %s, which is not in the registry", st.EntityID)
		}
	}
	if _, ok := f.State("light.garage"); ok {
		t.Error("the disabled light has a state")
	}
}
