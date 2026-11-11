// Package inventory builds model.Inventory snapshots from HA's registries
// and streams state changes for a set of entities.
package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// MaxAge is how long a snapshot is served without a registry event. It
// covers changes that send no event, such as a reconnect, which drops the
// ha client's caches.
const MaxAge = 5 * time.Second

// registryEvents are the events that make a snapshot stale. The ha client
// drops its own caches on the same events.
var registryEvents = []string{
	"area_registry_updated",
	"device_registry_updated",
	"entity_registry_updated",
	"label_registry_updated",
}

// Cache builds model.Inventory snapshots on demand and keeps the last one
// until a registry changes or it is MaxAge old (see SetMaxAge). Safe for concurrent use.
type Cache struct {
	client ha.Client
	now    func() time.Time
	log    *slog.Logger
	maxAge time.Duration

	mu    sync.Mutex
	inv   model.Inventory
	built time.Time // zero = no snapshot
	gen   uint64    // bumped by registry events

	watchers map[*watcher]struct{}
	watchCtx context.Context // Run's ctx; nil before Run
}

// New returns a Cache over client. Call Run to drop snapshots on registry
// events; without it, snapshots live for MaxAge.
func New(client ha.Client, log *slog.Logger) *Cache {
	if log == nil {
		log = slog.Default()
	}
	return &Cache{client: client, now: time.Now, maxAge: MaxAge, log: log.With("component", "inventory"), watchers: map[*watcher]struct{}{}}
}

// SetMaxAge sets how long a snapshot is served without a registry event,
// instead of MaxAge. A value of 0 or less means MaxAge. Call it before
// first use.
func (c *Cache) SetMaxAge(d time.Duration) {
	if d <= 0 {
		d = MaxAge
	}
	c.mu.Lock()
	c.maxAge = d
	c.mu.Unlock()
}

// Run subscribes to registry and state events until ctx is done. It
// returns once subscribed. Watch needs Run.
func (c *Cache) Run(ctx context.Context) error {
	for _, typ := range registryEvents {
		if err := c.client.SubscribeEvents(ctx, typ, func(ha.Event) { c.Invalidate() }); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.watchCtx = ctx
	c.mu.Unlock()
	return c.client.SubscribeEvents(ctx, "state_changed", c.dispatch)
}

// Invalidate drops the current snapshot.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.gen++
	c.built = time.Time{}
	c.mu.Unlock()
}

// Inventory returns the current snapshot. Its maps are shared between
// callers: treat them as read-only.
//
// Builds are not merged: two callers that miss at the same time both
// fetch. The ha client serves registries from its cache, so that costs
// little. A build that raced with a registry event is returned but not
// kept.
func (c *Cache) Inventory(ctx context.Context) (model.Inventory, error) {
	c.mu.Lock()
	if !c.built.IsZero() && c.now().Sub(c.built) < c.maxAge {
		inv := c.inv
		c.mu.Unlock()
		return inv, nil
	}
	gen := c.gen
	c.mu.Unlock()

	inv, err := Build(ctx, c.client)
	if err != nil {
		return model.Inventory{}, err
	}
	c.mu.Lock()
	if c.gen == gen {
		c.inv, c.built = inv, c.now()
	}
	c.mu.Unlock()
	return inv, nil
}

// Build fetches the registries, the states and the core config, and maps
// them to a model.Inventory.
//
//   - Entity.AreaID is the entity's own area; empty means its device's.
//   - Hidden means hidden_by is set.
//   - Disabled means the entity's disabled_by is set, or its device's.
//   - Name is the registry name, else the original name, else the
//     friendly_name attribute of its state.
//   - An entity with a state but no registry entry is included with its
//     id and friendly name only: no device, area or labels, not hidden,
//     not disabled.
func Build(ctx context.Context, client ha.Client) (model.Inventory, error) {
	areas, err := client.Areas(ctx)
	if err != nil {
		return model.Inventory{}, err
	}
	devices, err := client.Devices(ctx)
	if err != nil {
		return model.Inventory{}, err
	}
	entities, err := client.Entities(ctx)
	if err != nil {
		return model.Inventory{}, err
	}
	labels, err := client.Labels(ctx)
	if err != nil {
		return model.Inventory{}, err
	}
	states, err := client.States(ctx)
	if err != nil {
		return model.Inventory{}, err
	}
	core, err := client.Config(ctx)
	if err != nil {
		return model.Inventory{}, err
	}

	friendly := make(map[string]string, len(states))
	for _, st := range states {
		if n, ok := st.Attributes["friendly_name"].(string); ok {
			friendly[st.EntityID] = n
		}
	}
	inv := model.Inventory{
		Entities: make(map[string]model.Entity, len(entities)),
		Devices:  make(map[string]model.Device, len(devices)),
		Areas:    make(map[string]model.Area, len(areas)),
		Labels:   make(map[string]model.Label, len(labels)),
		Home:     model.Home{Lat: core.Latitude, Lon: core.Longitude},
	}
	disabledDevice := map[string]bool{}
	for _, d := range devices {
		inv.Devices[d.ID] = model.Device{
			ID: d.ID, Name: d.Name, NameByUser: d.NameByUser, AreaID: d.AreaID, Labels: slices.Clone(d.Labels),
		}
		if d.DisabledBy != "" {
			disabledDevice[d.ID] = true
		}
	}
	for _, e := range entities {
		name := e.Name
		if name == "" {
			name = e.OriginalName
		}
		if name == "" {
			name = friendly[e.EntityID]
		}
		inv.Entities[e.EntityID] = model.Entity{
			ID:             e.EntityID,
			DeviceID:       e.DeviceID,
			AreaID:         e.AreaID,
			Labels:         slices.Clone(e.Labels),
			Hidden:         e.HiddenBy != "",
			Disabled:       e.DisabledBy != "" || (e.DeviceID != "" && disabledDevice[e.DeviceID]),
			EntityCategory: e.EntityCategory,
			Name:           name,
		}
	}
	// An entity with a state but no registry entry (a YAML entity with no
	// unique_id) still exists in HA and can be named explicitly. It has no
	// device, area or labels, so only an exact entity id selects it.
	for _, st := range states {
		if _, ok := inv.Entities[st.EntityID]; !ok && st.EntityID != "" {
			inv.Entities[st.EntityID] = model.Entity{ID: st.EntityID, Name: friendly[st.EntityID]}
		}
	}
	for _, a := range areas {
		inv.Areas[a.AreaID] = model.Area{ID: a.AreaID, Name: a.Name, Labels: slices.Clone(a.Labels)}
	}
	for _, l := range labels {
		inv.Labels[l.LabelID] = model.Label{ID: l.LabelID, Name: l.Name}
	}
	return inv, nil
}

// States returns the current states of ids. Entities HA has no state for
// are left out. Attributes are HA's, unfiltered: filter them with the
// catalog's allowlist before a guest sees them.
func (c *Cache) States(ctx context.Context, ids map[string]bool) (map[string]model.State, error) {
	all, err := c.client.States(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.State, len(ids))
	for _, st := range all {
		if ids[st.EntityID] {
			out[st.EntityID] = toState(st)
		}
	}
	return out, nil
}

func toState(st ha.State) model.State {
	return model.State{EntityID: st.EntityID, State: st.State, Attributes: st.Attributes, LastChanged: st.LastChanged}
}

// watcher is one Watch call.
type watcher struct {
	ids map[string]bool
	fn  func(model.State)
}

// Watch calls fn with the new state of each entity in ids that changes,
// until ctx is done, then returns. An entity that is removed is sent with
// State "" and no attributes. fn runs on the event goroutine of the ha
// client: it must not block (hand off to a channel instead), and must
// not change the state's attributes, which other watchers share. Like
// States, attributes are unfiltered. Watch returns an error at once when
// Run was not called.
//
// All watchers share one state_changed subscription, made by Run, so
// many open streams cost one copy of the event stream.
func (c *Cache) Watch(ctx context.Context, ids map[string]bool, fn func(model.State)) error {
	c.mu.Lock()
	if c.watchCtx == nil {
		c.mu.Unlock()
		return fmt.Errorf("inventory: Watch before Run")
	}
	w := &watcher{ids: maps.Clone(ids), fn: fn}
	c.watchers[w] = struct{}{}
	runCtx := c.watchCtx
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.watchers, w)
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runCtx.Done():
		return runCtx.Err()
	}
}

// stateChanged is the data of a state_changed event.
type stateChanged struct {
	EntityID string    `json:"entity_id"`
	NewState *ha.State `json:"new_state"`
}

func (c *Cache) dispatch(ev ha.Event) {
	var d stateChanged
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		c.log.Debug("state_changed: decode", "err", err)
		return
	}
	st := model.State{EntityID: d.EntityID}
	if d.NewState != nil {
		st = toState(*d.NewState)
		st.EntityID = d.EntityID
	}
	c.mu.Lock()
	var fns []func(model.State)
	for w := range c.watchers {
		if w.ids[d.EntityID] {
			fns = append(fns, w.fn)
		}
	}
	c.mu.Unlock()
	for _, fn := range fns {
		fn(st)
	}
}
