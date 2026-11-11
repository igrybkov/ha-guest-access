package authz

import (
	"fmt"
	"slices"
	"strings"

	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// Expansion mirrors HA core's homeassistant/helpers/target.py at commit
// eb1faafe4a1ed615990577aab662c412f5968197 (the last commit to touch that
// file on dev, read in November 2026), functions
// async_extract_referenced_entity_ids and _async_extract_referenced_entity_ids.
//
// What we take from HA:
//   - An exact entity id is "referenced" with no registry filter, so it
//     selects the entity even when it is hidden, disabled or has an
//     entity_category. It must still be in the inventory. (HA can't act on
//     a disabled entity anyway, since it has no state.)
//   - A device selects every entity of that device, whatever the entity's
//     own area.
//   - An area selects entities whose own area is that area, plus entities
//     of devices in that area that have no area of their own. So an
//     entity's area wins over its device's area.
//   - A label selects entities with that label, devices with that label
//     (expanded like a selected device) and areas with that label
//     (expanded like a selected area).
//   - Device and area expansion skip hidden entities and entities with an
//     entity_category ("primary entities only").
//   - Exclude resolves with include_hidden and without the primary filter,
//     so it removes hidden and categorized entities too. It wins over
//     select.
//
// Where we differ, always on the strict side:
//   - Expansion also skips disabled entities. HA lists them and then finds
//     no state for them.
//   - A label on an entity skips categorized entities too. HA keeps them on
//     that one path. Brief decision 7 asks for the stricter rule.
//   - "*" patterns are ours. An entity pattern with "*" counts as expansion
//     and uses the same skip rules. Area, label and device patterns match
//     ids; device patterns also match the display name.
//   - HA also pulls in child devices (via_device) and floors, and expands
//     groups. The inventory has none of these, so we don't.

// skippedInExpansion reports whether expansion (anything but an exact
// entity id) must leave e out.
func skippedInExpansion(e model.Entity) bool {
	return e.Hidden || e.Disabled || e.EntityCategory != ""
}

// expander resolves one selector against an inventory.
type expander struct {
	sel model.Selector
	inv model.Inventory
	// all turns off the skip rules. Exclude uses it.
	all     bool
	areas   map[string]bool // memo: area id -> selected
	devices map[string]bool // memo: device id -> selected
}

func newExpander(sel model.Selector, inv model.Inventory, all bool) *expander {
	return &expander{sel: sel, inv: inv, all: all, areas: map[string]bool{}, devices: map[string]bool{}}
}

func (x *expander) labelHit(labels []string) bool {
	for _, l := range labels {
		if matchAny(x.sel.Labels, l) {
			return true
		}
	}
	return false
}

// areaHit reports whether area id is selected, by id or by one of its
// labels. An area that is not in the registry can still match by id, as
// in HA.
func (x *expander) areaHit(id string) bool {
	if id == "" {
		return false
	}
	if hit, ok := x.areas[id]; ok {
		return hit
	}
	hit := matchAny(x.sel.Areas, id) || x.labelHit(x.inv.Areas[id].Labels)
	x.areas[id] = hit
	return hit
}

// deviceHit reports whether device id is selected, by id, display name
// or one of its labels.
func (x *expander) deviceHit(id string) bool {
	if id == "" {
		return false
	}
	if hit, ok := x.devices[id]; ok {
		return hit
	}
	d, known := x.inv.Devices[id]
	hit := matchAny(x.sel.Devices, id)
	if known && !hit {
		name := d.DisplayName()
		hit = (name != "" && matchAny(x.sel.Devices, name)) || x.labelHit(d.Labels)
	}
	x.devices[id] = hit
	return hit
}

// selects reports whether the selector picks e.
func (x *expander) selects(e model.Entity) bool {
	for _, p := range x.sel.Entities {
		if isExact(p) && p == e.ID {
			return true
		}
	}
	if !x.all && skippedInExpansion(e) {
		return false
	}
	for _, p := range x.sel.Entities {
		if !isExact(p) && Match(p, e.ID) {
			return true
		}
	}
	if x.labelHit(e.Labels) || x.deviceHit(e.DeviceID) || x.areaHit(e.AreaID) {
		return true
	}
	// The device's area counts only when the entity has no area itself.
	return e.AreaID == "" && e.DeviceID != "" && x.areaHit(x.inv.Devices[e.DeviceID].AreaID)
}

// ruleMatches returns the entity ids r selects after exclude, before and
// after the domain filter. Both are sorted.
func ruleMatches(r model.Rule, inv model.Inventory) (picked, granted []string) {
	domains := map[string]bool{}
	for _, a := range r.Actions {
		if _, ok := catalog.Lookup(a); ok {
			domains[model.Domain(a)] = true
		}
	}
	sel := newExpander(r.Select, inv, false)
	exc := newExpander(r.Exclude, inv, true)
	for id, e := range inv.Entities {
		// The map key is the id. Trust it over the struct field, so a
		// mismatched snapshot can't grant one id under another.
		e.ID = id
		if !sel.selects(e) || exc.selects(e) {
			continue
		}
		picked = append(picked, id)
		if domains[model.Domain(id)] {
			granted = append(granted, id)
		}
	}
	slices.Sort(picked)
	slices.Sort(granted)
	return picked, granted
}

// RuleEntities returns the entity ids a rule selects in inv, after exclude
// and the domain filter, sorted. The editor shows it next to each rule.
func RuleEntities(r model.Rule, inv model.Inventory) []string {
	_, granted := ruleMatches(r, inv)
	return granted
}

// Resolve expands the link's templates and extra rules against inv. A
// pair is granted only when the action is in the catalog and its domain
// is the entity's domain.
//
// Sources come in a fixed order: templates in the link's order, rules in
// file order, then the link's extra rules. A template the link names twice
// counts once. Template rules use model.Template.RuleAccess; an extra rule
// uses its own access, or none.
//
// Problems never stop it. Each gives a Warning instead:
//   - a template id the link names that templates does not hold
//   - an action that is not in the catalog
//   - a rule that grants nothing
//   - a rule with network: local (Resolve can't see settings; the
//     AccessFunc enforces it)
//   - script variables with a "code" key (the rule's script.run is dropped)
//
// It does not look at the link's window, revocation or one-time use.
// Nothing in the result shares a map with templates.
func Resolve(link model.Link, templates []model.Template, inv model.Inventory) (Grants, []Warning) {
	r := resolver{link: link, inv: inv, grants: Grants{}}
	seen := map[string]bool{}
	for _, id := range link.TemplateIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		i := slices.IndexFunc(templates, func(t model.Template) bool { return t.ID == id })
		if i < 0 {
			r.warn(id, -1, "template not found")
			continue
		}
		t := templates[i]
		for j, rule := range t.Rules {
			r.addRule(id, j, rule, t.RuleAccess(j))
		}
	}
	for j, rule := range link.Extra {
		var acc model.Access
		if rule.Access != nil {
			acc = *rule.Access
		}
		r.addRule("", j, rule, acc)
	}
	return r.grants, r.warnings
}

type resolver struct {
	link     model.Link
	inv      model.Inventory
	grants   Grants
	warnings []Warning
}

func (r *resolver) warn(template string, rule int, format string, args ...any) {
	r.warnings = append(r.warnings, Warning{Template: template, Rule: rule, Message: fmt.Sprintf(format, args...)})
}

func (r *resolver) addRule(template string, i int, rule model.Rule, acc model.Access) {
	if acc.Local() {
		r.warn(template, i, "uses network: local; it denies every request unless local access is turned on")
	}
	var actions []string
	for _, a := range rule.Actions {
		switch {
		case slices.Contains(actions, a):
		case !known(a):
			r.warn(template, i, "action %q is not in the catalog", a)
		case a == model.ActionScriptRun && hasCodeKey(rule.Variables):
			r.warn(template, i, "script variables hold a code; %s is not granted", a)
		default:
			actions = append(actions, a)
		}
	}
	picked, granted := ruleMatches(rule, r.inv)
	switch {
	case len(picked) == 0:
		r.warn(template, i, "selects no entities")
	case len(granted) == 0:
		r.warn(template, i, "selects %d entities, but none fit its actions' domains", len(picked))
	}
	added := false
	for _, id := range granted {
		for _, a := range actions {
			if model.Domain(a) != model.Domain(id) {
				continue
			}
			key := GrantKey{EntityID: id, Action: a}
			g := r.grants[key]
			g.EntityID, g.Action = id, a
			src := Source{Template: template, Rule: i, Access: acc}
			if a == model.ActionScriptRun {
				src.Variables = cloneMap(rule.Variables)
				if src.Variables == nil {
					src.Variables = map[string]any{}
				}
				// Set last, so a template can't spoof it.
				src.Variables["guest_link"] = r.link.Name
			}
			g.Sources = append(g.Sources, src)
			r.grants[key] = g
			added = true
		}
	}
	if !added && len(granted) > 0 {
		r.warn(template, i, "grants nothing")
	}
}

func known(action string) bool {
	_, ok := catalog.Lookup(action)
	return ok
}

// hasCodeKey reports whether v holds a key named "code" (any case) at any
// depth. Validation refuses these; this is the second line.
func hasCodeKey(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if strings.EqualFold(k, "code") || hasCodeKey(x) {
				return true
			}
		}
	case map[any]any:
		for k, x := range v {
			if s, ok := k.(string); (ok && strings.EqualFold(s, "code")) || hasCodeKey(x) {
				return true
			}
		}
	case []any:
		for _, x := range v {
			if hasCodeKey(x) {
				return true
			}
		}
	}
	return false
}

// cloneMap deep copies a variables map. Nil stays nil.
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return cloneValue(m).(map[string]any)
}

// cloneValue deep copies the shapes YAML and JSON decode into. Other
// values (strings, numbers, bools) are immutable.
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
