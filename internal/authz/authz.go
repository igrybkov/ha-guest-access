// Package authz turns a link into the exact (entity, action) pairs it
// grants, and checks a guest request against them.
//
// Resolve mirrors HA's target expansion (homeassistant/helpers/target.py):
// an entity's own area wins over its device's area; area, device and label
// expansion skip hidden entities, disabled entities and entities with an
// entity_category; explicit entity ids select even hidden ones. Wave 1
// notes the HA version it mirrored.
//
// Network and location checks are not done here. The caller passes them in
// as an AccessFunc, so authz needs no settings, cookies or client IPs.
package authz

import (
	"errors"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// Sentinel errors of Check. A missing pair and a forbidden pair are the
// same ErrForbidden, so a guest can't probe for entity ids.
var (
	ErrForbidden = errors.New("forbidden")
	ErrBadData   = errors.New("bad data")
)

// GrantKey is one (entity, action) pair. Both parts are compared exactly:
// no trimming, no case folding.
type GrantKey struct {
	EntityID string
	Action   string // catalog action id, such as "light.turn_on"
}

// Source is one rule that grants a pair. Two rules may grant the same pair
// with different access or variables; each is a Source.
type Source struct {
	// Template is the template id, or "" for a link's extra rule.
	Template string
	// Rule is the rule's index in the template's Rules, or in the link's
	// Extra when Template is "".
	Rule int
	// Access is the access that applies to the rule (see
	// model.Template.RuleAccess). The zero Access means no limits.
	Access model.Access
	// Variables are the script variables for script.run: the rule's fixed
	// variables plus "guest_link" (the link name). Nil for other actions.
	Variables map[string]any
}

// Grant is everything a link allows for one pair.
type Grant struct {
	EntityID string
	Action   string
	// Sources are in a fixed order: templates in the link's order, rules in
	// file order, then the link's extra rules.
	Sources []Source
}

// Grants maps each granted pair to its Grant.
type Grants map[GrantKey]Grant

// Warning is a problem Resolve skipped over, for the admin UI: a missing
// template, an action not in the catalog, a selector that matches nothing.
type Warning struct {
	Template string // "" for a link's extra rule
	Rule     int    // -1 when the warning is about the whole template
	Message  string
}

// AccessFunc reports whether the current request passes a rule's access
// (network and location). It must deny a "local" rule while local access
// is turned off in settings.
type AccessFunc func(model.Access) bool

// Pick returns the first source whose access passes.
func (g Grant) Pick(allow AccessFunc) (Source, bool) {
	for _, s := range g.Sources {
		if allow(s.Access) {
			return s, true
		}
	}
	return Source{}, false
}

// Resolve expands the link's templates and extra rules against inv. A
// pair is granted only when the action is in the catalog and its domain
// is the entity's domain. Template ids the link names but templates does
// not hold give a Warning, not an error.
//
// It does not look at the link's window, revocation or one-time use.
//
// TODO(wave 1): implement. The stub grants nothing.
func Resolve(link model.Link, templates []model.Template, inv model.Inventory) (Grants, []Warning) {
	return Grants{}, nil
}

// RuleEntities returns the entity ids a rule selects in inv, after exclude
// and the domain filter, sorted. The editor shows it next to each rule.
//
// TODO(wave 1): implement. The stub selects nothing.
func RuleEntities(r model.Rule, inv model.Inventory) []string {
	return nil
}

// Match reports whether s matches pattern, where "*" matches any run of
// characters (none included) and every other character matches itself.
//
// TODO(wave 1): implement. The stub matches nothing.
func Match(pattern, s string) bool {
	return false
}

// Request is what a guest sends to POST /g/{slug}/act.
type Request struct {
	EntityID string         `json:"entity_id"`
	Action   string         `json:"action"`
	Data     map[string]any `json:"data"`
}

// Call is the service call the server makes for an allowed request. The
// target is always exactly one entity id.
type Call struct {
	Domain   string
	Service  string
	EntityID string
	Data     map[string]any
}

// Check finds the request's pair in g, picks the first source whose access
// passes allow, checks the data against the catalog, and builds the call.
// st is the entity's current state, for fields bounded by attributes (light
// color temperature); nil means fixed bounds only.
//
// Errors: ErrForbidden when the pair is not granted or no source passes
// allow; ErrBadData (wrapped, with the key) for an unknown key, a wrong
// type or a value out of range.
//
// TODO(wave 1): implement. The stub forbids everything.
func Check(g Grants, req Request, st *model.State, allow AccessFunc) (Source, Call, error) {
	return Source{}, Call{}, ErrForbidden
}
