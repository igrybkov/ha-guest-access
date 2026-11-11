package inventory

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
)

// counting counts get_states calls. during, if set, runs inside one.
type counting struct {
	*hatest.Fake
	n      atomic.Int32
	mu     sync.Mutex
	during func()
}

func (c *counting) States(ctx context.Context) ([]ha.State, error) {
	c.n.Add(1)
	c.mu.Lock()
	during := c.during
	c.during = nil
	c.mu.Unlock()
	out, err := c.Fake.States(ctx)
	if during != nil {
		during()
	}
	return out, err
}

func runCache(t *testing.T, client ha.Client) *Cache {
	t.Helper()
	c := New(client, quietLog)
	if err := c.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	return c
}

func changed(f *hatest.Fake, id, state string) {
	f.EmitData("state_changed", map[string]any{"entity_id": id,
		"new_state": map[string]any{"entity_id": id, "state": state, "attributes": map[string]any{}}})
}

func stateOf(t *testing.T, c *Cache, id string) (string, bool) {
	t.Helper()
	got, err := c.States(t.Context(), map[string]bool{id: true})
	if err != nil {
		t.Fatal(err)
	}
	st, ok := got[id]
	return st.State, ok
}

// After Run, one get_states seeds the cache, and state_changed keeps it
// current: many reads and a snapshot cost one fetch.
func TestStateCacheSeedsOnce(t *testing.T) {
	f := &counting{Fake: fake()}
	c := runCache(t, f)
	for range 50 {
		if s, _ := stateOf(t, c, "switch.heater"); s != "off" {
			t.Fatalf("state = %q", s)
		}
	}
	if _, err := c.Inventory(t.Context()); err != nil {
		t.Fatal(err)
	}
	changed(f.Fake, "switch.heater", "on")
	if s, _ := stateOf(t, c, "switch.heater"); s != "on" {
		t.Errorf("after state_changed: %q", s)
	}
	// A new entity and a removed one.
	changed(f.Fake, "light.new", "on")
	f.EmitData("state_changed", map[string]any{"entity_id": "media_player.guest", "new_state": nil})
	if s, ok := stateOf(t, c, "light.new"); !ok || s != "on" {
		t.Errorf("new entity: %q %v", s, ok)
	}
	if _, ok := stateOf(t, c, "media_player.guest"); ok {
		t.Error("removed entity still has a state")
	}
	if n := f.n.Load(); n != 1 {
		t.Errorf("%d get_states calls, want 1", n)
	}
}

// Lost events (a reconnect) and an entity registry change make the next
// read fetch again. Other registry events do not.
func TestStateCacheStartsOver(t *testing.T) {
	f := &counting{Fake: fake()}
	c := runCache(t, f)
	stateOf(t, c, "switch.heater")
	// HA changed a state while the connection was down: no event.
	f.SetState(ha.State{EntityID: "switch.heater", State: "on"})
	if s, _ := stateOf(t, c, "switch.heater"); s != "off" {
		t.Fatalf("cache changed with no event: %q", s)
	}
	f.LoseEvents()
	if s, _ := stateOf(t, c, "switch.heater"); s != "on" {
		t.Errorf("after lost events: %q", s)
	}
	if n := f.n.Load(); n != 2 {
		t.Errorf("%d get_states calls, want 2", n)
	}
	f.EmitData("area_registry_updated", map[string]any{})
	stateOf(t, c, "switch.heater")
	if n := f.n.Load(); n != 2 {
		t.Errorf("an area change refetched states: %d calls", n)
	}
	f.EmitData("entity_registry_updated", map[string]any{})
	stateOf(t, c, "switch.heater")
	if n := f.n.Load(); n != 3 {
		t.Errorf("an entity registry change did not refetch: %d calls", n)
	}
}

// An event that arrives while the seed fetch is in flight is newer than
// the fetch's answer, so it wins.
func TestStateCacheEventDuringSeed(t *testing.T) {
	f := &counting{Fake: fake()}
	c := runCache(t, f)
	f.during = func() { changed(f.Fake, "switch.heater", "on") }
	if s, _ := stateOf(t, c, "switch.heater"); s != "on" {
		t.Errorf("first read: %q, want the event's state", s)
	}
	if s, _ := stateOf(t, c, "switch.heater"); s != "on" {
		t.Errorf("cached: %q", s)
	}
	if n := f.n.Load(); n != 1 {
		t.Errorf("%d get_states calls", n)
	}
}

// A fetch that started before the cache started over is returned but not
// kept.
func TestStateCacheDropsRacedSeed(t *testing.T) {
	f := &counting{Fake: fake()}
	c := runCache(t, f)
	f.during = f.LoseEvents
	stateOf(t, c, "switch.heater")
	stateOf(t, c, "switch.heater")
	if n := f.n.Load(); n != 2 {
		t.Errorf("%d get_states calls, want 2: the raced seed was kept", n)
	}
}

// Parallel first reads share one fetch.
func TestStateCacheParallelSeed(t *testing.T) {
	f := &counting{Fake: fake()}
	c := runCache(t, f)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := c.States(context.Background(), map[string]bool{"switch.heater": true}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := f.n.Load(); n > 2 {
		t.Errorf("%d get_states calls for 20 parallel reads", n)
	}
}

// Before Run there are no events to keep a cache current, so each read
// asks HA.
func TestStatesBeforeRunFetch(t *testing.T) {
	f := &counting{Fake: fake()}
	c := New(f, quietLog)
	stateOf(t, c, "switch.heater")
	stateOf(t, c, "switch.heater")
	if n := f.n.Load(); n != 2 {
		t.Errorf("%d get_states calls, want 2", n)
	}
}
