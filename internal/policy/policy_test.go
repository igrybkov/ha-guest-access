package policy

import (
	"errors"
	"slices"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

func local() *model.Access { return &model.Access{Network: model.NetworkLocal} }
func near() *model.Access  { return &model.Access{NearHomeM: 300} }
func anyNet() *model.Access {
	return &model.Access{Network: model.NetworkAny}
}

func rule(access *model.Access, actions ...string) model.Rule {
	if len(actions) == 0 {
		actions = []string{"light.turn_on"}
	}
	return model.Rule{Select: model.Selector{Entities: []string{"light.guest"}}, Actions: actions, Access: access}
}

func settings(localOn bool) model.Settings {
	s := model.DefaultSettings()
	s.LocalAccess.Enabled = localOn
	return s
}

// paths returns the problem paths of err, which must be nil or a
// *model.ValidationError.
func paths(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		return nil
	}
	var ve *model.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error is %T, want *model.ValidationError: %v", err, err)
	}
	var out []string
	for _, p := range ve.Problems {
		out = append(out, p.Path)
	}
	return out
}

func TestValidateTemplates(t *testing.T) {
	tpl := func(access *model.Access, rules ...model.Rule) model.Templates {
		return model.Templates{Templates: []model.Template{{ID: "t", Name: "T", Access: access, Rules: rules}}}
	}
	for _, tc := range []struct {
		name    string
		ts      model.Templates
		localOn bool
		hasHome bool
		want    []string
	}{
		{"plain", tpl(nil, rule(nil)), false, false, nil},
		{"any network", tpl(anyNet(), rule(anyNet())), false, false, nil},
		{"no templates", model.Templates{}, false, false, nil},
		{"template local, off", tpl(local(), rule(nil)), false, true, []string{"templates[0].access.network"}},
		// "Anywhere": the template default counts even when every rule
		// replaces it.
		{"template local under rule override, off", tpl(local(), rule(anyNet())), false, true, []string{"templates[0].access.network"}},
		{"rule local, off", tpl(nil, rule(nil), rule(local())), false, true, []string{"templates[0].rules[1].access.network"}},
		{"local, on", tpl(local(), rule(local())), true, true, nil},
		{"near home, no home", tpl(near(), rule(near())), false, false,
			[]string{"templates[0].access.near_home_m", "templates[0].rules[0].access.near_home_m"}},
		{"near home, home", tpl(near(), rule(near())), false, true, nil},
		{"near home 0, no home", tpl(&model.Access{NearHomeM: 0}, rule(nil)), false, false, nil},
		{"unknown action", tpl(nil, rule(nil, "light.turn_on", "light.explode")), false, false, []string{"templates[0].rules[0].actions[1]"}},
		{"unknown domain", tpl(nil, rule(nil, "vacuum.start")), false, false, []string{"templates[0].rules[0].actions[0]"}},
		{"service name, not catalog id", tpl(nil, rule(nil, "script.turn_on")), false, false, []string{"templates[0].rules[0].actions[0]"}},
		{"everything wrong", tpl(local(), rule(&model.Access{Network: model.NetworkLocal, NearHomeM: 10}, "x.y")), false, false,
			[]string{"templates[0].access.network", "templates[0].rules[0].actions[0]", "templates[0].rules[0].access.network", "templates[0].rules[0].access.near_home_m"}},
	} {
		got := paths(t, ValidateTemplates(tc.ts, settings(tc.localOn), tc.hasHome))
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: problems at %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidateTemplatesSecondTemplatePath(t *testing.T) {
	ts := model.Templates{Templates: []model.Template{
		{ID: "a", Name: "A", Rules: []model.Rule{rule(nil)}},
		{ID: "b", Name: "B", Rules: []model.Rule{rule(nil), rule(local())}},
	}}
	if got := paths(t, ValidateTemplates(ts, settings(false), true)); !slices.Equal(got, []string{"templates[1].rules[1].access.network"}) {
		t.Errorf("paths = %v", got)
	}
}

func TestValidateLink(t *testing.T) {
	ts := model.Templates{Templates: []model.Template{
		{ID: "house_entry", Name: "House", Rules: []model.Rule{rule(nil)}},
		// A template that breaks policy does not make the link fail: the
		// template's own save path reports it.
		{ID: "music", Name: "Music", Access: local(), Rules: []model.Rule{rule(nil)}},
	}}
	link := func(ids []string, extra ...model.Rule) model.Link {
		return model.Link{TemplateIDs: ids, Extra: extra}
	}
	for _, tc := range []struct {
		name    string
		l       model.Link
		localOn bool
		hasHome bool
		want    []string
	}{
		{"templates exist", link([]string{"house_entry", "music"}), false, false, nil},
		{"draft without id or slug", link(nil, rule(nil)), false, false, nil},
		{"missing template", link([]string{"house_entry", "gone"}), false, false, []string{"templates[1]"}},
		{"template id case", link([]string{"House_Entry"}), false, false, []string{"templates[0]"}},
		{"extra local, off", link(nil, rule(nil), rule(local())), false, true, []string{"extra[1].access.network"}},
		{"extra local, on", link(nil, rule(local())), true, true, nil},
		{"extra near home, no home", link(nil, rule(near())), true, false, []string{"extra[0].access.near_home_m"}},
		{"extra near home, home", link(nil, rule(near())), true, true, nil},
		{"extra unknown action", link(nil, rule(nil, "lock.smash")), true, true, []string{"extra[0].actions[0]"}},
		{"all at once", link([]string{"nope"}, rule(&model.Access{Network: model.NetworkLocal, NearHomeM: 5}, "a.b")), false, false,
			[]string{"templates[0]", "extra[0].actions[0]", "extra[0].access.network", "extra[0].access.near_home_m"}},
	} {
		got := paths(t, ValidateLink(tc.l, ts, settings(tc.localOn), tc.hasHome))
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: problems at %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Every action in the catalog passes, so the check can't drift from it.
func TestEveryCatalogActionPasses(t *testing.T) {
	r := rule(nil)
	r.Actions = nil
	for _, a := range catalog.All() {
		r.Actions = append(r.Actions, a.ID)
	}
	if err := ValidateLink(model.Link{Extra: []model.Rule{r}}, model.Templates{}, settings(false), false); err != nil {
		t.Error(err)
	}
}
