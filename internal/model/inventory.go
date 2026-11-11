package model

import "time"

// Inventory is a snapshot of HA's registries, as the permission resolver
// sees them. Maps are keyed by id. Build a new one on every registry change;
// never change one in place, since resolvers may share it.
type Inventory struct {
	Entities map[string]Entity `json:"entities"`
	Devices  map[string]Device `json:"devices"`
	Areas    map[string]Area   `json:"areas"`
	Labels   map[string]Label  `json:"labels"`
	Home     Home              `json:"home"`
}

// Home is HA's home location.
type Home struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// Set reports whether HA has a home location. HA reports 0, 0 when none
// is set. Those coordinates are public knowledge, so a near-home check
// against them must fail.
func (h Home) Set() bool { return h != Home{} }

// Entity is an entity registry entry.
type Entity struct {
	ID       string `json:"entity_id"`
	DeviceID string `json:"device_id,omitempty"`
	// AreaID is the entity's own area. Empty means the device's area.
	AreaID string   `json:"area_id,omitempty"`
	Labels []string `json:"labels,omitempty"`
	// Hidden, Disabled and EntityCategory keep an entity out of area,
	// device and label expansion, as in HA. An explicit entity id still
	// selects it.
	Hidden         bool   `json:"hidden,omitempty"`
	Disabled       bool   `json:"disabled,omitempty"`
	EntityCategory string `json:"entity_category,omitempty"` // "config", "diagnostic" or ""
	// Name is the display name: the registry name, else the original name,
	// else the friendly_name state attribute.
	Name string `json:"name,omitempty"`
}

// Domain returns the part of the entity id before the first ".".
func (e Entity) Domain() string { return Domain(e.ID) }

// Domain returns the part of an entity or action id before the first ".".
func Domain(id string) string {
	for i := 0; i < len(id); i++ {
		if id[i] == '.' {
			return id[:i]
		}
	}
	return ""
}

// Device is a device registry entry.
type Device struct {
	ID         string   `json:"id"`
	Name       string   `json:"name,omitempty"`
	NameByUser string   `json:"name_by_user,omitempty"`
	AreaID     string   `json:"area_id,omitempty"`
	Labels     []string `json:"labels,omitempty"`
}

// DisplayName returns NameByUser if set, else Name. Device patterns match
// on it.
func (d Device) DisplayName() string {
	if d.NameByUser != "" {
		return d.NameByUser
	}
	return d.Name
}

// Area is an area registry entry.
type Area struct {
	ID     string   `json:"area_id"`
	Name   string   `json:"name"`
	Labels []string `json:"labels,omitempty"`
}

// Label is a label registry entry.
type Label struct {
	ID   string `json:"label_id"`
	Name string `json:"name"`
}

// State is an entity's current state.
type State struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged time.Time      `json:"last_changed"`
}
