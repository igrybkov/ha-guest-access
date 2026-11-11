package web

import (
	"fmt"

	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// basicPolicy is the Policy used when Deps.Policy is nil. It is a stopgap
// until the policy package is wired in. It checks:
//
//   - every action is in the catalog;
//   - no rule needs network: local while local access is off (brief
//     decision 2);
//   - every template a link names exists.
type basicPolicy struct{}

func (basicPolicy) ValidateTemplates(ts model.Templates, st model.Settings) error {
	var p []model.Problem
	for i, t := range ts.Templates {
		for j := range t.Rules {
			p = append(p, ruleProblems(fmt.Sprintf("templates[%d].rules[%d]", i, j), t.Rules[j], t.RuleAccess(j), st)...)
		}
		if t.Access != nil && t.Access.Local() && !st.LocalAccess.Enabled {
			p = append(p, localOff(fmt.Sprintf("templates[%d].access.network", i)))
		}
	}
	return problemsErr(p)
}

func (basicPolicy) ValidateLink(l model.Link, ts model.Templates, st model.Settings) error {
	var p []model.Problem
	for i, id := range l.TemplateIDs {
		if _, ok := ts.Find(id); !ok {
			p = append(p, model.Problem{Path: fmt.Sprintf("templates[%d]", i), Message: fmt.Sprintf("template %q does not exist", id)})
		}
	}
	for j, r := range l.Extra {
		var acc model.Access
		if r.Access != nil {
			acc = *r.Access
		}
		p = append(p, ruleProblems(fmt.Sprintf("extra[%d]", j), r, acc, st)...)
	}
	return problemsErr(p)
}

func ruleProblems(path string, r model.Rule, acc model.Access, st model.Settings) []model.Problem {
	var p []model.Problem
	for k, a := range r.Actions {
		if _, ok := catalog.Lookup(a); !ok {
			p = append(p, model.Problem{Path: fmt.Sprintf("%s.actions[%d]", path, k), Message: fmt.Sprintf("%q is not a known action", a)})
		}
	}
	if acc.Local() && !st.LocalAccess.Enabled {
		p = append(p, localOff(path+".access.network"))
	}
	return p
}

func localOff(path string) model.Problem {
	return model.Problem{Path: path, Message: "local only needs local access turned on in the app options"}
}

func problemsErr(p []model.Problem) error {
	if len(p) == 0 {
		return nil
	}
	return &model.ValidationError{Problems: p}
}
