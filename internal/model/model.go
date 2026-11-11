// Package model holds the data types shared by all packages: templates,
// links and settings as stored in YAML, and the HA inventory snapshot the
// permission resolver works on.
//
// Validate methods check shape only: required fields, known enum values,
// pattern guard rails. They never look at other files or at HA. Checks
// that need context (a template id that exists, an action in the catalog,
// local access turned on in settings) belong to the caller.
package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Network values for Access.Network.
const (
	NetworkAny   = "any"
	NetworkLocal = "local"
)

// MaxNearHomeM caps Access.NearHomeM. A radius beyond this is not "near".
const MaxNearHomeM = 100_000

// Access limits where a rule's actions may be used from.
//
// A nil *Access means "no limits". A rule's access replaces its template's
// access as a whole; the two are never merged.
type Access struct {
	// Network is NetworkAny or NetworkLocal. Empty means NetworkAny.
	// NetworkLocal only works when Settings.LocalAccess.Enabled is true;
	// otherwise a rule with it denies every request (fail closed).
	Network string `yaml:"network,omitempty" json:"network,omitempty"`
	// NearHomeM is the radius around HA's home, in meters, that the guest
	// must prove with the location cookie. 0 means off.
	NearHomeM int `yaml:"near_home_m,omitempty" json:"near_home_m,omitempty"`
}

// Local reports whether a requires the local network.
func (a Access) Local() bool { return a.Network == NetworkLocal }

// Validate checks the network value and the radius.
func (a Access) Validate() error {
	var p problems
	a.validate(&p, "")
	return p.err()
}

func (a Access) validate(p *problems, path string) {
	switch a.Network {
	case "", NetworkAny, NetworkLocal:
	default:
		p.add(join(path, "network"), "must be %q or %q", NetworkAny, NetworkLocal)
	}
	if a.NearHomeM < 0 || a.NearHomeM > MaxNearHomeM {
		p.add(join(path, "near_home_m"), "must be between 0 and %d", MaxNearHomeM)
	}
}

// Selector picks entities. Each key holds exact ids or patterns where "*"
// matches any run of characters, none included. Entities, areas and labels
// match on ids. Devices match on id or name (name_by_user if set, else
// name). The keys combine as a union.
type Selector struct {
	Entities []string `yaml:"entities,omitempty" json:"entities,omitempty"`
	Areas    []string `yaml:"areas,omitempty" json:"areas,omitempty"`
	Labels   []string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Devices  []string `yaml:"devices,omitempty" json:"devices,omitempty"`
}

// Empty reports whether s has no patterns at all.
func (s Selector) Empty() bool {
	return len(s.Entities)+len(s.Areas)+len(s.Labels)+len(s.Devices) == 0
}

// WildcardOnly reports whether pattern is made only of "*" and "." (for
// example "*" or "*.*"). Such a pattern would expose a whole domain or the
// whole house, so select refuses it.
func WildcardOnly(pattern string) bool {
	return strings.Trim(pattern, "*.") == ""
}

// validate checks every pattern. inSelect turns on the wildcard guard rail.
func (s Selector) validate(p *problems, path string, inSelect bool) {
	for _, key := range []struct {
		name string
		list []string
	}{{"entities", s.Entities}, {"areas", s.Areas}, {"labels", s.Labels}, {"devices", s.Devices}} {
		for i, pat := range key.list {
			at := fmt.Sprintf("%s[%d]", join(path, key.name), i)
			switch {
			case pat == "":
				p.add(at, "is empty")
			case strings.TrimSpace(pat) != pat:
				p.add(at, "has leading or trailing spaces")
			case inSelect && WildcardOnly(pat):
				p.add(at, "%q matches too much; name ids or add a prefix", pat)
			}
		}
	}
}

// Script actions. Variables only make sense with ActionScriptRun.
const (
	ActionScriptRun    = "script.run"
	ActionScriptCancel = "script.cancel"
)

// actionRe is the shape of a catalog action id: "domain.action".
var actionRe = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// Rule is a selector plus the actions allowed on what it selects. An
// expanded entity only counts when one of Actions is in its domain.
type Rule struct {
	Select  Selector `yaml:"select" json:"select"`
	Exclude Selector `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	// Actions are catalog action ids, such as "light.turn_on".
	Actions []string `yaml:"actions" json:"actions"`
	// Access, when set, replaces the template's access for this rule.
	Access *Access `yaml:"access,omitempty" json:"access,omitempty"`
	// Variables are fixed script variables for ActionScriptRun. The app
	// adds "guest_link" itself. A "code" key is refused at any depth.
	Variables map[string]any `yaml:"variables,omitempty" json:"variables,omitempty"`
}

// Validate checks the rule's shape.
func (r Rule) Validate() error {
	var p problems
	r.validate(&p, "")
	return p.err()
}

func (r Rule) validate(p *problems, path string) {
	if r.Select.Empty() {
		p.add(join(path, "select"), "picks nothing")
	}
	r.Select.validate(p, join(path, "select"), true)
	r.Exclude.validate(p, join(path, "exclude"), false)
	if len(r.Actions) == 0 {
		p.add(join(path, "actions"), "is empty")
	}
	seen := map[string]bool{}
	hasRun := false
	for i, a := range r.Actions {
		at := fmt.Sprintf("%s[%d]", join(path, "actions"), i)
		switch {
		case !actionRe.MatchString(a):
			p.add(at, "%q is not an action id like light.turn_on", a)
		case seen[a]:
			p.add(at, "%q is listed twice", a)
		}
		seen[a] = true
		hasRun = hasRun || a == ActionScriptRun
	}
	if r.Access != nil {
		r.Access.validate(p, join(path, "access"))
	}
	if len(r.Variables) > 0 && !hasRun {
		p.add(join(path, "variables"), "only apply to %s", ActionScriptRun)
	}
	if k := codeKey(r.Variables); k != "" {
		p.add(join(path, "variables"), "%q is not allowed: codes stay in HA", k)
	}
}

// UsesLocalNetwork reports whether the rule's own access needs the local
// network.
func (r Rule) UsesLocalNetwork() bool { return r.Access != nil && r.Access.Local() }

// codeKey returns the first key named "code" (any case) at any depth of v,
// or "".
func codeKey(v any) string {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if strings.EqualFold(k, "code") {
				return k
			}
			if c := codeKey(x); c != "" {
				return c
			}
		}
	case map[any]any:
		for k, x := range v {
			if s, ok := k.(string); ok && strings.EqualFold(s, "code") {
				return s
			}
			if c := codeKey(x); c != "" {
				return c
			}
		}
	case []any:
		for _, x := range v {
			if c := codeKey(x); c != "" {
				return c
			}
		}
	}
	return ""
}

// idRe is the shape of template ids: lower case, digits, "_" and "-".
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Template is a named set of rules. Templates are live: an edit applies to
// every open link that uses it.
type Template struct {
	ID   string `yaml:"id" json:"id"`
	Name string `yaml:"name" json:"name"`
	// Icon is an MDI icon name such as "mdi:door". Display only.
	Icon string `yaml:"icon,omitempty" json:"icon,omitempty"`
	// Access is the default for rules that have none of their own.
	Access *Access `yaml:"access,omitempty" json:"access,omitempty"`
	Rules  []Rule  `yaml:"rules" json:"rules"`
	// Party shows the Music Assistant party link on the guest page. It does
	// nothing while Music Assistant is not set up.
	Party bool `yaml:"party,omitempty" json:"party,omitempty"`
	// EntityOrder lists entity ids in the order the guest page shows them
	// in this template's group. Entities not listed follow, by name. An id
	// that no longer resolves is skipped.
	EntityOrder []string `yaml:"entity_order,omitempty" json:"entity_order,omitempty"`
}

// MarshalJSON writes rules as [] when there are none. Go writes a nil
// slice as null, and clients expect a list.
func (t Template) MarshalJSON() ([]byte, error) {
	type plain Template
	if t.Rules == nil {
		t.Rules = []Rule{}
	}
	return json.Marshal(plain(t))
}

// Validate checks the template's shape.
func (t Template) Validate() error {
	var p problems
	t.validate(&p, "")
	return p.err()
}

func (t Template) validate(p *problems, path string) {
	if !idRe.MatchString(t.ID) {
		p.add(join(path, "id"), "must be 1-64 of a-z, 0-9, _ and -, starting with a letter or digit")
	}
	if strings.TrimSpace(t.Name) == "" {
		p.add(join(path, "name"), "is empty")
	}
	if t.Access != nil {
		t.Access.validate(p, join(path, "access"))
	}
	// A template with no rules is valid. It grants nothing, so the admin
	// can save it and add rules later.
	for i, r := range t.Rules {
		r.validate(p, fmt.Sprintf("%s[%d]", join(path, "rules"), i))
	}
	validateEntityOrder(p, join(path, "entity_order"), t.EntityOrder)
}

// entityIDRe is the shape of an HA entity id: "domain.object_id". The
// object id may start with a digit.
var entityIDRe = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

// validateEntityOrder checks that each entry looks like an entity id and
// is listed once. It never checks that the entity exists.
func validateEntityOrder(p *problems, path string, ids []string) {
	seen := map[string]bool{}
	for i, id := range ids {
		at := fmt.Sprintf("%s[%d]", path, i)
		switch {
		case !entityIDRe.MatchString(id):
			p.add(at, "%q is not an entity id like light.kitchen", id)
		case seen[id]:
			p.add(at, "%q is listed twice", id)
		}
		seen[id] = true
	}
}

// RuleAccess returns the access that applies to rule i: the rule's own,
// else the template's, else none (the zero Access).
func (t Template) RuleAccess(i int) Access {
	if a := t.Rules[i].Access; a != nil {
		return *a
	}
	if t.Access != nil {
		return *t.Access
	}
	return Access{}
}

// UsesLocalNetwork reports whether any rule ends up needing the local
// network.
func (t Template) UsesLocalNetwork() bool {
	for i := range t.Rules {
		if t.RuleAccess(i).Local() {
			return true
		}
	}
	return false
}

// Templates is templates.yaml, an ordered list of templates.
type Templates struct {
	Version   int        `yaml:"version" json:"version"`
	Templates []Template `yaml:"templates" json:"templates"`
}

// Find returns the template with id.
func (ts Templates) Find(id string) (Template, bool) {
	for _, t := range ts.Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// Validate checks every template and that ids are unique.
func (ts Templates) Validate() error {
	var p problems
	seen := map[string]bool{}
	for i, t := range ts.Templates {
		at := fmt.Sprintf("templates[%d]", i)
		t.validate(&p, at)
		if t.ID != "" && seen[t.ID] {
			p.add(join(at, "id"), "%q is used twice", t.ID)
		}
		seen[t.ID] = true
	}
	return p.err()
}
