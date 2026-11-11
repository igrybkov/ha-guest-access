// Package catalog lists every action the app knows. An action that is not
// here can't be granted or called. Each action names the HA service it
// maps to, the data a guest may send, and the UI control that shows it.
//
// Adding a domain means adding its actions here, plus a UI control and
// tests. Nothing else in the app changes.
package catalog

import "slices"

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

// actions is the table, keyed by Action.ID. Wave 1 fills it from plan
// section 4.
var actions = map[string]Action{
	"lock.lock": {ID: "lock.lock", Domain: "lock", Service: "lock", UI: "confirm"},
}

// attributes lists, per domain, the state attributes a guest may see.
// Anything else (lock user codes, who is home) is dropped.
var attributes = map[string][]string{
	"lock": {"friendly_name"},
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
		d := domainOf(id)
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

func domainOf(id string) string {
	for i := 0; i < len(id); i++ {
		if id[i] == '.' {
			return id[:i]
		}
	}
	return id
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
