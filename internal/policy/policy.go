// Package policy holds the checks that need more than one file: a rule in
// templates.yaml against settings.yaml and the action catalog, a link in
// links.yaml against the templates. model's Validate methods check one
// value's shape; the admin save paths call both.
//
// These checks guard saves only. A file that already breaks them still
// loads, and the guest side fails closed on it (a local rule with local
// access off denies every request).
package policy

import (
	"fmt"

	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// ValidateTemplates checks every template against settings s and the
// catalog. hasHome is whether HA has a home location; without one, no
// rule can use near_home_m. It returns a *model.ValidationError with paths
// like "templates[0].rules[1].access.network", or nil.
func ValidateTemplates(ts model.Templates, s model.Settings, hasHome bool) error {
	var c checker
	c.settings, c.hasHome = s, hasHome
	for i, t := range ts.Templates {
		at := fmt.Sprintf("templates[%d]", i)
		if t.Access != nil {
			c.access(*t.Access, at+".access")
		}
		for j, r := range t.Rules {
			c.rule(r, fmt.Sprintf("%s.rules[%d]", at, j))
		}
	}
	return c.err()
}

// ValidateLink checks a link draft against the templates ts and settings
// s: every template id exists, and its extra rules pass the same checks as
// template rules. It does not check the link's own shape (see
// model.Link.Validate); a draft has no id or slug yet. Paths are relative
// to the link, like "extra[0].access.network".
func ValidateLink(l model.Link, ts model.Templates, s model.Settings, hasHome bool) error {
	var c checker
	c.settings, c.hasHome = s, hasHome
	for i, id := range l.TemplateIDs {
		if _, ok := ts.Find(id); !ok {
			c.add(fmt.Sprintf("templates[%d]", i), "template %q does not exist", id)
		}
	}
	for i, r := range l.Extra {
		c.rule(r, fmt.Sprintf("extra[%d]", i))
	}
	return c.err()
}

type checker struct {
	settings model.Settings
	hasHome  bool
	problems []model.Problem
}

func (c *checker) add(path, format string, args ...any) {
	c.problems = append(c.problems, model.Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (c *checker) err() error {
	if len(c.problems) == 0 {
		return nil
	}
	return &model.ValidationError{Problems: c.problems}
}

func (c *checker) rule(r model.Rule, at string) {
	for i, a := range r.Actions {
		if _, ok := catalog.Lookup(a); !ok {
			c.add(fmt.Sprintf("%s.actions[%d]", at, i), "%q is not an action this app knows", a)
		}
	}
	if r.Access != nil {
		c.access(*r.Access, at+".access")
	}
}

func (c *checker) access(a model.Access, at string) {
	if a.Local() && !c.settings.LocalAccess.Enabled {
		c.add(at+".network", "local only is off; turn it on in Settings first")
	}
	if a.NearHomeM > 0 && !c.hasHome {
		c.add(at+".near_home_m", "HA has no home location; set one in HA first")
	}
}
