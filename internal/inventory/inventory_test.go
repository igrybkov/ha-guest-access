package inventory

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

var quietLog = slog.New(slog.DiscardHandler)

func fake() *hatest.Fake {
	return &hatest.Fake{
		AreaList: []ha.Area{
			{AreaID: "guest_room", Name: "Guest room", Labels: []string{"guest"}},
			{AreaID: "office", Name: "Office"},
		},
		DeviceList: []ha.Device{
			{ID: "dev1", Name: "Sonos One", NameByUser: "Guest speaker", AreaID: "guest_room", Labels: []string{"guest_music"}},
			{ID: "dev2", Name: "Old lamp", AreaID: "office", DisabledBy: "user"},
		},
		EntityList: []ha.Entity{
			{EntityID: "media_player.guest", DeviceID: "dev1", OriginalName: "Speaker"},
			{EntityID: "light.lamp", DeviceID: "dev2", AreaID: "guest_room", Name: "Lamp", OriginalName: "Old lamp light"},
			{EntityID: "switch.heater", HiddenBy: "user", Labels: []string{"guest"}},
			{EntityID: "sensor.battery", DeviceID: "dev1", EntityCategory: "diagnostic", DisabledBy: "integration"},
		},
		LabelList: []ha.Label{{LabelID: "guest", Name: "Guest"}, {LabelID: "guest_music", Name: "Guest music"}},
		StateList: []ha.State{
			{EntityID: "switch.heater", State: "off", Attributes: map[string]any{"friendly_name": "Heater"}},
			{EntityID: "media_player.guest", State: "playing", Attributes: map[string]any{"friendly_name": "Guest speaker", "volume_level": 0.3}},
		},
		Core: ha.Config{Latitude: 41.88, Longitude: -87.63, TimeZone: "America/Chicago"},
	}
}

func TestBuild(t *testing.T) {
	inv, err := Build(context.Background(), fake())
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]model.Entity{
		// No own area: the device's area applies later, in the resolver.
		// The friendly name wins over the original name: it carries the
		// device's name_by_user.
		"media_player.guest": {ID: "media_player.guest", DeviceID: "dev1", Name: "Guest speaker"},
		// Own area overrides; a disabled device disables its entities.
		"light.lamp": {ID: "light.lamp", DeviceID: "dev2", AreaID: "guest_room", Name: "Lamp", Disabled: true},
		// No registry name: the friendly name from the state.
		"switch.heater": {ID: "switch.heater", Labels: []string{"guest"}, Hidden: true, Name: "Heater"},
		// No state and no names: the device's display name.
		"sensor.battery": {ID: "sensor.battery", DeviceID: "dev1", EntityCategory: "diagnostic", Disabled: true, Name: "Guest speaker"},
	} {
		got := inv.Entities[id]
		if got.ID != want.ID || got.DeviceID != want.DeviceID || got.AreaID != want.AreaID || got.Name != want.Name ||
			got.Hidden != want.Hidden || got.Disabled != want.Disabled || got.EntityCategory != want.EntityCategory ||
			len(got.Labels) != len(want.Labels) {
			t.Errorf("%s = %+v\nwant %+v", id, got, want)
		}
	}
	if d := inv.Devices["dev1"]; d.DisplayName() != "Guest speaker" || d.AreaID != "guest_room" || d.Labels[0] != "guest_music" {
		t.Errorf("dev1 = %+v", d)
	}
	if a := inv.Areas["guest_room"]; a.Name != "Guest room" || a.Labels[0] != "guest" {
		t.Errorf("guest_room = %+v", a)
	}
	if l := inv.Labels["guest_music"]; l.Name != "Guest music" {
		t.Errorf("label = %+v", l)
	}
	if inv.Home != (model.Home{Lat: 41.88, Lon: -87.63}) {
		t.Errorf("home = %+v", inv.Home)
	}
	if inv.TimeZone != "America/Chicago" {
		t.Errorf("time zone = %q", inv.TimeZone)
	}
	if len(inv.Entities) != 4 || len(inv.Devices) != 2 || len(inv.Areas) != 2 || len(inv.Labels) != 2 {
		t.Errorf("sizes %d %d %d %d", len(inv.Entities), len(inv.Devices), len(inv.Areas), len(inv.Labels))
	}
}

// failing is a Fake whose registry calls fail.
type failing struct{ *hatest.Fake }

func (failing) Entities(context.Context) ([]ha.Entity, error) { return nil, ha.ErrDisconnected }

func TestBuildError(t *testing.T) {
	if _, err := Build(context.Background(), failing{fake()}); !errors.Is(err, ha.ErrDisconnected) {
		t.Errorf("err = %v", err)
	}
	c := New(failing{fake()}, quietLog)
	if _, err := c.Inventory(context.Background()); err == nil {
		t.Error("Inventory hid the error")
	}
}

func TestCacheMemo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := fake()
	f.StateList = nil // so the entity count follows the registry alone
	c := New(f, quietLog)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	if err := c.Run(ctx); err != nil {
		t.Fatal(err)
	}

	inv, err := c.Inventory(ctx)
	if err != nil || len(inv.Entities) != 4 {
		t.Fatalf("first: %d, %v", len(inv.Entities), err)
	}
	f.EntityList = f.EntityList[:1]
	if inv, _ := c.Inventory(ctx); len(inv.Entities) != 4 {
		t.Error("snapshot not kept")
	}

	// A registry event drops it.
	f.EmitData("entity_registry_updated", map[string]any{"action": "remove"})
	if inv, _ := c.Inventory(ctx); len(inv.Entities) != 1 {
		t.Errorf("after event: %d entities", len(inv.Entities))
	}

	// So does age, for changes with no event.
	f.EntityList = fake().EntityList
	now = now.Add(MaxAge - time.Second)
	if inv, _ := c.Inventory(ctx); len(inv.Entities) != 1 {
		t.Error("snapshot dropped too early")
	}
	now = now.Add(time.Second)
	if inv, _ := c.Inventory(ctx); len(inv.Entities) != 4 {
		t.Errorf("after MaxAge: %d entities", len(inv.Entities))
	}
}

// A build that raced with a registry event is not kept.
func TestCacheDropsRacedBuild(t *testing.T) {
	f := &racing{Fake: fake()}
	c := New(f, quietLog)
	f.during = c.Invalidate
	if _, err := c.Inventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	kept := !c.built.IsZero()
	c.mu.Unlock()
	if kept {
		t.Error("kept a snapshot built across an invalidation")
	}
}

type racing struct {
	*hatest.Fake
	during func()
}

func (r *racing) Labels(ctx context.Context) ([]ha.Label, error) {
	r.during()
	return r.Fake.Labels(ctx)
}

func TestStates(t *testing.T) {
	c := New(fake(), quietLog)
	got, err := c.States(context.Background(), map[string]bool{"switch.heater": true, "light.unknown": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["switch.heater"].State != "off" {
		t.Errorf("states = %+v", got)
	}
}

func TestWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := fake()
	c := New(f, quietLog)
	if err := c.Watch(ctx, map[string]bool{"x.y": true}, func(model.State) {}); err == nil {
		t.Fatal("Watch before Run: no error")
	}
	if err := c.Run(ctx); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []model.State
	ids := map[string]bool{"light.lamp": true, "switch.heater": true}
	wctx, stop := context.WithCancel(ctx)
	done := make(chan error)
	go func() {
		done <- c.Watch(wctx, ids, func(s model.State) { mu.Lock(); got = append(got, s); mu.Unlock() })
	}()
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.watchers) == 1 })
	ids["lock.front_door"] = true // the caller's map is copied

	f.EmitData("state_changed", map[string]any{"entity_id": "light.lamp", "new_state": map[string]any{
		"entity_id": "light.lamp", "state": "on", "attributes": map[string]any{"brightness": 128},
		"last_changed": "2026-11-11T12:00:00Z",
	}})
	f.EmitData("state_changed", map[string]any{"entity_id": "lock.front_door", "new_state": map[string]any{"entity_id": "lock.front_door", "state": "unlocked"}})
	f.EmitData("state_changed", map[string]any{"entity_id": "switch.heater", "new_state": nil})
	f.Emit(ha.Event{EventType: "state_changed", Data: []byte(`{`)})

	mu.Lock()
	if len(got) != 2 || got[0].EntityID != "light.lamp" || got[0].State != "on" || got[0].Attributes["brightness"] != float64(128) ||
		got[1].EntityID != "switch.heater" || got[1].State != "" {
		t.Errorf("got %+v", got)
	}
	mu.Unlock()

	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Watch returned %v", err)
	}
	c.mu.Lock()
	n := len(c.watchers)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("%d watchers left", n)
	}
	if f.Subscribers("state_changed") != 1 {
		t.Errorf("%d state_changed subscriptions, want 1 shared", f.Subscribers("state_changed"))
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

// A YAML entity with no unique_id has a state but no registry entry. It
// must be in the inventory, so an explicit entity id can grant it.
func TestBuildIncludesStateOnlyEntities(t *testing.T) {
	f := fake()
	f.StateList = append(f.StateList,
		ha.State{EntityID: "light.yaml_porch", State: "off", Attributes: map[string]any{"friendly_name": "Porch"}},
		ha.State{EntityID: "script.let_me_in", State: "off"},
	)
	inv, err := Build(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{"light.yaml_porch": "Porch", "script.let_me_in": ""} {
		got, ok := inv.Entities[id]
		want := model.Entity{ID: id, Name: name}
		if !ok || got.ID != want.ID || got.Name != want.Name || got.DeviceID != "" || got.AreaID != "" ||
			got.Labels != nil || got.Hidden || got.Disabled || got.EntityCategory != "" {
			t.Errorf("%s = %+v, %v", id, got, ok)
		}
	}
	// A registry entry wins over its state.
	if got := inv.Entities["switch.heater"]; !got.Hidden {
		t.Errorf("registry entity replaced by its state: %+v", got)
	}
	if len(inv.Entities) != 6 {
		t.Errorf("%d entities, want 6", len(inv.Entities))
	}
}

func TestCacheSetMaxAge(t *testing.T) {
	ctx := context.Background()
	f := fake()
	f.StateList = nil
	c := New(f, quietLog)
	c.SetMaxAge(time.Minute)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	if inv, _ := c.Inventory(ctx); len(inv.Entities) != 4 {
		t.Fatalf("first: %d", len(inv.Entities))
	}
	f.EntityList = f.EntityList[:1]
	now = now.Add(59 * time.Second)
	if inv, _ := c.Inventory(ctx); len(inv.Entities) != 4 {
		t.Error("dropped before the set age")
	}
	now = now.Add(time.Second)
	if inv, _ := c.Inventory(ctx); len(inv.Entities) != 1 {
		t.Error("kept past the set age")
	}
	c.SetMaxAge(0)
	if c.maxAge != MaxAge {
		t.Errorf("SetMaxAge(0) = %v", c.maxAge)
	}
}

// An unknown time zone name is dropped, so no client gets a name it
// can't use.
func TestBuildTimeZone(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Europe/Kyiv", "Europe/Kyiv"},
		{"", ""},
		{"Mars/Olympus_Mons", ""},
		{"Local", ""},
	} {
		f := fake()
		f.Core.TimeZone = tc.in
		inv, err := Build(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if inv.TimeZone != tc.want {
			t.Errorf("%q: time zone = %q, want %q", tc.in, inv.TimeZone, tc.want)
		}
	}
}
