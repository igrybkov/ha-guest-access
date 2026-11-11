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

// The seeded link grants the shared rooms, the guest room, the door, the
// garage door and the scripts. It leaves out the private, hidden, disabled and
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
		"alarm_control_panel.home", "cover.garage_door", "cover.living_room_blind", "fan.guest_room", "light.guest_room", "light.kitchen", "light.living_room",
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
	// The blind gets every cover action. The garage door opens, closes
	// and stops, and has no position slider.
	count := map[string]int{}
	for k := range grants {
		count[k.EntityID]++
	}
	if count["cover.living_room_blind"] != 8 || count["cover.garage_door"] != 3 {
		t.Errorf("cover actions: blind %d, garage %d", count["cover.living_room_blind"], count["cover.garage_door"])
	}
	if _, ok := grants[authz.GrantKey{EntityID: "cover.garage_door", Action: "cover.set_cover_position"}]; ok {
		t.Error("garage door has a position action")
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
