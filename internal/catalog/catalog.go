// Package catalog lists every action the app knows. An action that is not
// here can't be granted or called. Each action names the HA service it
// maps to, the data a guest may send, and the UI control that shows it.
//
// Adding a domain means adding its actions here, plus a UI control and
// tests. Nothing else in the app changes.
package catalog

import (
	"slices"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// Kind is the type of a data field.
type Kind string

const (
	KindBool   Kind = "bool"
	KindInt    Kind = "int"
	KindFloat  Kind = "float"
	KindString Kind = "string" // only values from Enum
)

// Field is one data key a guest may send with an action.
type Field struct {
	Kind Kind `json:"kind"`
	// Min and Max bound KindInt and KindFloat values, inclusive.
	Min float64 `json:"min,omitempty"`
	Max float64 `json:"max,omitempty"`
	// MinAttr and MaxAttr, when set, name state attributes of the entity
	// that narrow Min and Max (light min_color_temp_kelvin and
	// max_color_temp_kelvin). A missing attribute leaves the fixed bound.
	MinAttr string `json:"min_attr,omitempty"`
	MaxAttr string `json:"max_attr,omitempty"`
	// Enum lists the allowed KindString values.
	Enum []string `json:"enum,omitempty"`
}

// Action is one catalog entry.
type Action struct {
	ID      string `json:"id"`      // "light.turn_on"; the domain is the entity domain
	Domain  string `json:"domain"`  // HA service domain, such as "light"
	Service string `json:"service"` // HA service, such as "turn_on"
	// Data lists the keys a guest may send. Any other key is refused.
	Data map[string]Field `json:"data,omitempty"`
	// UI names the guest page control, such as "button", "toggle",
	// "slider" or "confirm".
	UI string `json:"ui"`
}

// Fixed color temperature bounds, in kelvin. A light's own
// min_color_temp_kelvin and max_color_temp_kelvin narrow them.
const (
	MinColorTempK = 1500
	MaxColorTempK = 9000
)

// actions is the table, keyed by Action.ID. It follows plan section 4.
// There is no "code" field anywhere: codes stay in HA (brief decision 9).
// lock.open is here, but like every action it is only granted when a rule
// lists it.
var actions = index([]Action{
	{ID: "lock.lock", Domain: "lock", Service: "lock", UI: "confirm"},
	{ID: "lock.unlock", Domain: "lock", Service: "unlock", UI: "confirm"},
	{ID: "lock.open", Domain: "lock", Service: "open", UI: "confirm"},

	{ID: "alarm_control_panel.alarm_disarm", Domain: "alarm_control_panel", Service: "alarm_disarm", UI: "confirm"},
	{ID: "alarm_control_panel.alarm_arm_home", Domain: "alarm_control_panel", Service: "alarm_arm_home", UI: "confirm"},
	{ID: "alarm_control_panel.alarm_arm_away", Domain: "alarm_control_panel", Service: "alarm_arm_away", UI: "confirm"},
	{ID: "alarm_control_panel.alarm_arm_night", Domain: "alarm_control_panel", Service: "alarm_arm_night", UI: "confirm"},

	{ID: "light.turn_on", Domain: "light", Service: "turn_on", UI: "slider", Data: map[string]Field{
		"brightness_pct": {Kind: KindInt, Min: 1, Max: 100},
		"color_temp_kelvin": {Kind: KindInt, Min: MinColorTempK, Max: MaxColorTempK,
			MinAttr: "min_color_temp_kelvin", MaxAttr: "max_color_temp_kelvin"},
	}},
	{ID: "light.turn_off", Domain: "light", Service: "turn_off", UI: "toggle"},
	{ID: "light.toggle", Domain: "light", Service: "toggle", UI: "toggle"},

	{ID: "fan.turn_on", Domain: "fan", Service: "turn_on", UI: "toggle"},
	{ID: "fan.turn_off", Domain: "fan", Service: "turn_off", UI: "toggle"},
	{ID: "fan.set_percentage", Domain: "fan", Service: "set_percentage", UI: "slider", Data: map[string]Field{
		"percentage": {Kind: KindInt, Min: 0, Max: 100},
	}},

	{ID: "media_player.media_play_pause", Domain: "media_player", Service: "media_play_pause", UI: "button"},
	{ID: "media_player.media_next_track", Domain: "media_player", Service: "media_next_track", UI: "button"},
	{ID: "media_player.media_previous_track", Domain: "media_player", Service: "media_previous_track", UI: "button"},
	{ID: "media_player.volume_set", Domain: "media_player", Service: "volume_set", UI: "slider", Data: map[string]Field{
		"volume_level": {Kind: KindFloat, Min: 0, Max: 1},
	}},
	{ID: "media_player.volume_mute", Domain: "media_player", Service: "volume_mute", UI: "toggle", Data: map[string]Field{
		"is_volume_muted": {Kind: KindBool},
	}},

	{ID: "switch.turn_on", Domain: "switch", Service: "turn_on", UI: "toggle"},
	{ID: "switch.turn_off", Domain: "switch", Service: "turn_off", UI: "toggle"},

	// Scripts take no guest data. script.run sends the rule's fixed
	// variables plus guest_link (brief decision 10).
	{ID: "script.run", Domain: "script", Service: "turn_on", UI: "button"},
	{ID: "script.cancel", Domain: "script", Service: "turn_off", UI: "button"},
})

// common are the attributes every domain may show.
var common = []string{"friendly_name", "supported_features"}

// attributes lists, per domain, the state attributes a guest may see.
// Anything else is dropped. Left out on purpose: entity_picture (its URL
// can carry an access token), changed_by and user ids, code_format,
// access_token, and any location (gps, latitude, longitude). Lock and
// alarm state is State.State, which is always kept.
var attributes = map[string][]string{
	"lock":                common,
	"alarm_control_panel": common,
	"light": append(slices.Clone(common), "brightness", "color_mode", "supported_color_modes",
		"color_temp_kelvin", "min_color_temp_kelvin", "max_color_temp_kelvin"),
	"fan":          append(slices.Clone(common), "percentage"),
	"media_player": append(slices.Clone(common), "volume_level", "is_volume_muted", "media_title", "media_artist"),
	"switch":       common,
	"script":       common,
}

func index(list []Action) map[string]Action {
	m := make(map[string]Action, len(list))
	for _, a := range list {
		if _, dup := m[a.ID]; dup {
			panic("catalog: duplicate action " + a.ID)
		}
		m[a.ID] = a
	}
	return m
}

// Lookup returns the action with id.
func Lookup(id string) (Action, bool) {
	a, ok := actions[id]
	if ok {
		a.Data = cloneFields(a.Data)
	}
	return a, ok
}

// All returns every action, sorted by id.
func All() []Action {
	out := make([]Action, 0, len(actions))
	for _, a := range actions {
		a.Data = cloneFields(a.Data)
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b Action) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return out
}

// Domains returns the entity domains that have actions, sorted.
func Domains() []string {
	var out []string
	for id := range actions {
		d := model.Domain(id)
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

// AttributeAllowlist returns the state attributes a guest may see for
// entities of domain. Unknown domains get none.
func AttributeAllowlist(domain string) []string {
	return slices.Clone(attributes[domain])
}

// FilterState returns a copy of st with only the attributes its domain
// allows. Every state a guest sees must pass through it. An unknown
// domain keeps no attributes. Values are deep copied, so the result
// shares nothing with st.
func FilterState(st model.State) model.State {
	allow := attributes[model.Domain(st.EntityID)]
	out := st
	out.Attributes = make(map[string]any, len(allow))
	for _, k := range allow {
		if v, ok := st.Attributes[k]; ok {
			out.Attributes[k] = cloneValue(v)
		}
	}
	return out
}

// cloneValue deep copies the JSON and YAML shapes a state or variable can
// hold. Other values (strings, numbers, bools) are immutable.
func cloneValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = cloneValue(x)
		}
		return out
	case map[any]any:
		out := make(map[any]any, len(v))
		for k, x := range v {
			out[k] = cloneValue(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = cloneValue(x)
		}
		return out
	case []string:
		return slices.Clone(v)
	}
	return v
}

func cloneFields(m map[string]Field) map[string]Field {
	if m == nil {
		return nil
	}
	out := make(map[string]Field, len(m))
	for k, f := range m {
		f.Enum = slices.Clone(f.Enum)
		out[k] = f
	}
	return out
}
