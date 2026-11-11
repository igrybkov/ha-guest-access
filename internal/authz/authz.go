// Package authz turns a link into the exact (entity, action) pairs it
// grants, and checks a guest request against them.
//
// Resolve mirrors HA's target expansion. See resolve.go for the HA file and
// commit it follows, and for the few places it is stricter.
//
// Network and location checks are not done here. The caller passes them in
// as an AccessFunc, so authz needs no settings, cookies or client IPs.
package authz

import (
	"errors"

	"github.com/igrybkov/ha-guest-access/internal/catalog"
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
// The steps run in this order, and each one fails closed:
//  1. The exact (entity_id, action) pair is in g. No trimming, no case
//     folding.
//  2. The action is in the catalog and its domain is the entity's domain.
//  3. A source passes allow. A nil allow passes nothing.
//  4. The data fits the catalog (see checkData).
//
// Errors: ErrForbidden when the pair is not granted or no source passes
// allow; ErrBadData (wrapped, with the key) for an unknown key, a wrong
// type or a value out of range. A data error is only ever returned for a
// granted pair that passed access, so it can't be used to probe for ids.
//
// The returned Source and Call share no maps with g.
func Check(g Grants, req Request, st *model.State, allow AccessFunc) (Source, Call, error) {
	grant, ok := g[GrantKey{EntityID: req.EntityID, Action: req.Action}]
	if !ok || grant.EntityID != req.EntityID || grant.Action != req.Action {
		return Source{}, Call{}, ErrForbidden
	}
	act, ok := catalog.Lookup(req.Action)
	if !ok || model.Domain(act.ID) != model.Domain(grant.EntityID) {
		return Source{}, Call{}, ErrForbidden
	}
	if allow == nil {
		return Source{}, Call{}, ErrForbidden
	}
	src, ok := grant.Pick(allow)
	if !ok {
		return Source{}, Call{}, ErrForbidden
	}
	data, err := checkData(act, req, st)
	if err != nil {
		return Source{}, Call{}, err
	}
	src.Variables = cloneMap(src.Variables)
	if act.ID == model.ActionScriptRun {
		vars := cloneMap(src.Variables)
		if vars == nil {
			vars = map[string]any{}
		}
		data["variables"] = vars
	}
	return src, Call{
		Domain:   act.Domain,
		Service:  act.Service,
		EntityID: grant.EntityID,
		Data:     data,
	}, nil
}
