package authz

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

func TestRuleEntities(t *testing.T) {
	lights := []string{"light.turn_on"}
	media := []string{"media_player.volume_set"}
	tests := []struct {
		name string
		rule model.Rule
		want []string
	}{
		{"area: own area wins over device area",
			model.Rule{Select: model.Selector{Areas: []string{"guest_room"}}, Actions: lights},
			// guest_lamp: device in office, own area guest_room -> in.
			// bedside: device in guest_room, own area office -> out.
			[]string{"light.guest_ceiling", "light.guest_lamp"}},
		{"area: the other side of the override",
			model.Rule{Select: model.Selector{Areas: []string{"office"}}, Actions: lights},
			[]string{"light.bedside", "light.office"}},
		{"area: through a device with no own area",
			model.Rule{Select: model.Selector{Areas: []string{"guest_room"}}, Actions: []string{"fan.turn_on"}},
			[]string{"fan.guest"}},
		{"area: skips hidden, disabled and categorized",
			model.Rule{Select: model.Selector{Areas: []string{"guest_room"}}, Actions: []string{"light.turn_on", "switch.turn_on", "fan.turn_on"}},
			[]string{"fan.guest", "light.guest_ceiling", "light.guest_lamp"}},
		{"area: wildcard on id",
			model.Rule{Select: model.Selector{Areas: []string{"guest_*"}}, Actions: lights},
			[]string{"light.guest_ceiling", "light.guest_lamp"}},
		{"area: unknown area selects nothing",
			model.Rule{Select: model.Selector{Areas: []string{"attic"}}, Actions: lights}, nil},
		{"area: matching is case-sensitive",
			model.Rule{Select: model.Selector{Areas: []string{"Guest_room"}}, Actions: lights}, nil},
		{"explicit: hidden, disabled and categorized are kept",
			model.Rule{Select: model.Selector{Entities: []string{"light.guest_hidden", "light.guest_disabled", "switch.guest_config"}},
				Actions: []string{"light.turn_on", "switch.turn_on"}},
			[]string{"light.guest_disabled", "light.guest_hidden", "switch.guest_config"}},
		{"explicit: must exist",
			model.Rule{Select: model.Selector{Entities: []string{"light.ghost"}}, Actions: lights}, nil},
		{"wildcard entity: skips hidden, disabled and categorized",
			model.Rule{Select: model.Selector{Entities: []string{"light.guest_*", "switch.guest_*"}}, Actions: []string{"light.turn_on", "switch.turn_on"}},
			[]string{"light.guest_ceiling", "light.guest_lamp"}},
		{"device: by id",
			model.Rule{Select: model.Selector{Devices: []string{"dev_sonos"}}, Actions: media},
			[]string{"media_player.living"}},
		{"device: by name_by_user",
			model.Rule{Select: model.Selector{Devices: []string{"Living Sonos"}}, Actions: media},
			[]string{"media_player.living"}},
		{"device: name is ignored when name_by_user is set",
			model.Rule{Select: model.Selector{Devices: []string{"Sonos One"}}, Actions: media}, nil},
		{"device: by name with wildcard",
			model.Rule{Select: model.Selector{Devices: []string{"Office*"}}, Actions: media},
			[]string{"media_player.office"}},
		{"device: entity area does not matter, categorized skipped",
			model.Rule{Select: model.Selector{Devices: []string{"Guest Fan", "Lamp"}}, Actions: []string{"fan.turn_on", "light.turn_on"}},
			[]string{"fan.guest", "light.guest_lamp"}},
		{"label: on entities, devices and areas",
			model.Rule{Select: model.Selector{Labels: []string{"guest_music", "guest_area"}}, Actions: media},
			// direct and private by label, tagged via its device, living and
			// living_tv via the labeled area. config_tagged and hidden_tagged
			// are skipped.
			[]string{"media_player.direct", "media_player.living", "media_player.living_tv", "media_player.private", "media_player.tagged"}},
		{"label: wildcard on id",
			model.Rule{Select: model.Selector{Labels: []string{"guest_s*"}}, Actions: []string{"script.run"}},
			[]string{"script.im_leaving", "script.let_me_in"}},
		{"label on an area expands through its devices",
			model.Rule{Select: model.Selector{Labels: []string{"guest_area"}}, Actions: media},
			[]string{"media_player.living", "media_player.living_tv"}},
		{"exclude by entity wins over explicit select",
			model.Rule{Select: model.Selector{Entities: []string{"light.guest_ceiling", "light.office"}},
				Exclude: model.Selector{Entities: []string{"light.office"}}, Actions: lights},
			[]string{"light.guest_ceiling"}},
		{"exclude by entity wildcard reaches hidden ones",
			model.Rule{Select: model.Selector{Entities: []string{"light.guest_hidden", "light.office"}},
				Exclude: model.Selector{Entities: []string{"light.guest_*"}}, Actions: lights},
			[]string{"light.office"}},
		{"exclude by label",
			model.Rule{Select: model.Selector{Labels: []string{"guest_music"}},
				Exclude: model.Selector{Labels: []string{"priv*"}}, Actions: media},
			[]string{"media_player.direct", "media_player.tagged"}},
		{"exclude by device name wildcard",
			model.Rule{Select: model.Selector{Labels: []string{"guest_music"}, Entities: []string{"media_player.office"}},
				Exclude: model.Selector{Devices: []string{"Office*", "Tag*"}}, Actions: media},
			[]string{"media_player.direct", "media_player.private"}},
		{"exclude by device id",
			model.Rule{Select: model.Selector{Areas: []string{"guest_room"}},
				Exclude: model.Selector{Devices: []string{"dev_lamp"}}, Actions: lights},
			[]string{"light.guest_ceiling"}},
		{"exclude by area keeps entity area override",
			model.Rule{Select: model.Selector{Entities: []string{"light.*"}},
				Exclude: model.Selector{Areas: []string{"office"}}, Actions: lights},
			// guest_lamp's device is in the office, but the lamp is not.
			[]string{"light.guest_ceiling", "light.guest_lamp"}},
		{"exclude by area wildcard and hidden explicit",
			model.Rule{Select: model.Selector{Entities: []string{"light.guest_hidden", "light.office"}},
				Exclude: model.Selector{Areas: []string{"guest*"}}, Actions: lights},
			[]string{"light.office"}},
		{"exclude may be all wildcards",
			model.Rule{Select: model.Selector{Areas: []string{"guest_room"}},
				Exclude: model.Selector{Entities: []string{"*"}}, Actions: lights}, nil},
		{"domain filter drops entities no action fits",
			model.Rule{Select: model.Selector{Areas: []string{"guest_room"}}, Actions: []string{"fan.set_percentage"}},
			[]string{"fan.guest"}},
		{"domain filter ignores actions not in the catalog",
			model.Rule{Select: model.Selector{Areas: []string{"guest_room"}}, Actions: []string{"lock.set_code", "light.dim"}}, nil},
		{"union of keys",
			model.Rule{Select: model.Selector{Entities: []string{"lock.front_door"}, Areas: []string{"guest_room"}}, Actions: []string{"lock.unlock"}},
			[]string{"lock.front_door", "lock.guest_room_door"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RuleEntities(tt.rule, house())
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got  %v\nwant %v", got, tt.want)
			}
		})
	}
}

// A snapshot whose map key and entity id disagree grants the key only.
func TestRuleEntitiesTrustsMapKey(t *testing.T) {
	inv := model.Inventory{Entities: map[string]model.Entity{
		"light.a": {ID: "lock.front_door", AreaID: "x"},
	}}
	got := RuleEntities(model.Rule{Select: model.Selector{Areas: []string{"x"}}, Actions: []string{"lock.unlock", "light.turn_on"}}, inv)
	if !slices.Equal(got, []string{"light.a"}) {
		t.Fatalf("got %v", got)
	}
}

func TestResolveSourcesInOrder(t *testing.T) {
	tpl := []model.Template{
		{ID: "b", Name: "B", Access: &model.Access{NearHomeM: 100}, Rules: []model.Rule{
			{Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock"}},
			{Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock", "lock.unlock"}, Access: local()},
		}},
		{ID: "a", Name: "A", Rules: []model.Rule{
			{Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock"}},
		}},
	}
	link := model.Link{Name: "L", TemplateIDs: []string{"b", "a", "b"}, Extra: []model.Rule{
		{Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock"}, Access: &model.Access{NearHomeM: 5}},
		{Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock"}},
	}}
	g, _ := Resolve(link, tpl, house())
	got := g[GrantKey{EntityID: "lock.front_door", Action: "lock.unlock"}]
	want := Grant{EntityID: "lock.front_door", Action: "lock.unlock", Sources: []Source{
		{Template: "b", Rule: 0, Access: model.Access{NearHomeM: 100}},
		{Template: "b", Rule: 1, Access: model.Access{Network: model.NetworkLocal}},
		{Template: "a", Rule: 0},
		{Template: "", Rule: 0, Access: model.Access{NearHomeM: 5}},
		{Template: "", Rule: 1},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if len(g) != 1 {
		t.Fatalf("grants = %v", g)
	}
}

func TestResolveGrantsOnlyCatalogPairsInDomain(t *testing.T) {
	g, _ := Resolve(guestLink(), guestTemplates(), house())
	var keys []string
	for k, v := range g {
		if k.EntityID != v.EntityID || k.Action != v.Action || len(v.Sources) == 0 {
			t.Errorf("bad grant %+v under %+v", v, k)
		}
		keys = append(keys, k.EntityID+" "+k.Action)
	}
	slices.Sort(keys)
	want := []string{
		"fan.guest fan.set_percentage",
		"light.guest_ceiling light.toggle",
		"light.guest_ceiling light.turn_off",
		"light.guest_ceiling light.turn_on",
		"light.guest_lamp light.toggle",
		"light.guest_lamp light.turn_off",
		"light.guest_lamp light.turn_on",
		"lock.front_door lock.unlock",
		"media_player.living media_player.volume_mute",
		"media_player.living media_player.volume_set",
		"script.let_me_in script.cancel",
		"script.let_me_in script.run",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("got  %v\nwant %v", keys, want)
	}
}

func TestResolveScriptVariables(t *testing.T) {
	tpl := guestTemplates()
	g, _ := Resolve(guestLink(), tpl, house())
	run := g[GrantKey{EntityID: "script.let_me_in", Action: "script.run"}].Sources[0].Variables
	if run["guest_link"] != "Anna" || run["source"] != "guest_access" {
		t.Fatalf("variables = %v", run)
	}
	if v := g[GrantKey{EntityID: "script.let_me_in", Action: "script.cancel"}].Sources[0].Variables; v != nil {
		t.Fatalf("cancel has variables %v", v)
	}
	if v := g[GrantKey{EntityID: "lock.front_door", Action: "lock.unlock"}].Sources[0].Variables; v != nil {
		t.Fatalf("lock has variables %v", v)
	}

	// The output never aliases template maps, at any depth.
	run["source"] = "changed"
	run["nested"].(map[string]any)["a"].([]any)[0] = "changed"
	vars := tpl[1].Rules[1].Variables
	if vars["source"] != "guest_access" || vars["nested"].(map[string]any)["a"].([]any)[0] != "b" {
		t.Fatalf("template changed: %v", vars)
	}
	if _, ok := vars["guest_link"]; ok {
		t.Fatal("guest_link was written into the template")
	}

	// A template can't spoof guest_link, and a script rule without
	// variables still gets it.
	spoof := []model.Template{{ID: "s", Name: "S", Rules: []model.Rule{
		{Select: model.Selector{Entities: []string{"script.let_me_in"}}, Actions: []string{"script.run"}, Variables: map[string]any{"guest_link": "Illia"}},
		{Select: model.Selector{Entities: []string{"script.im_leaving"}}, Actions: []string{"script.run"}},
	}}}
	g, _ = Resolve(model.Link{Name: "Anna", TemplateIDs: []string{"s"}}, spoof, house())
	for _, id := range []string{"script.let_me_in", "script.im_leaving"} {
		if v := g[GrantKey{EntityID: id, Action: "script.run"}].Sources[0].Variables["guest_link"]; v != "Anna" {
			t.Errorf("%s guest_link = %v", id, v)
		}
	}
}

func TestResolveWarnings(t *testing.T) {
	tpl := []model.Template{
		{ID: "t", Name: "T", Rules: []model.Rule{
			{Select: model.Selector{Entities: []string{"light.ghost"}}, Actions: []string{"light.turn_on"}},
			{Select: model.Selector{Areas: []string{"guest_room"}}, Actions: []string{"lock.set_code", "light.turn_on"}},
			{Select: model.Selector{Areas: []string{"guest_room"}}, Actions: []string{"vacuum.start"}},
			{Select: model.Selector{Entities: []string{"script.let_me_in"}}, Actions: []string{"script.run"},
				Variables: map[string]any{"nested": map[string]any{"Code": "1234"}}},
		}},
		{ID: "loc", Name: "Local", Access: local(), Rules: []model.Rule{
			{Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock"}},
		}},
	}
	link := model.Link{Name: "L", TemplateIDs: []string{"missing", "t", "loc"}, Extra: []model.Rule{
		{Select: model.Selector{Entities: []string{"light.office"}}, Actions: []string{"light.turn_on"}, Access: local()},
	}}
	g, warns := Resolve(link, tpl, house())
	type w struct {
		tpl  string
		rule int
		has  string
	}
	want := []w{
		{"missing", -1, "not found"},
		{"t", 0, "selects no entities"},
		{"t", 1, `"lock.set_code" is not in the catalog`},
		{"t", 2, `"vacuum.start" is not in the catalog`},
		{"t", 2, "none fit"},
		{"t", 3, "code"},
		{"t", 3, "grants nothing"},
		{"loc", 0, "local"},
		{"", 0, "local"},
	}
	if len(warns) != len(want) {
		t.Fatalf("warnings = %+v", warns)
	}
	for i, x := range want {
		got := warns[i]
		if got.Template != x.tpl || got.Rule != x.rule || !strings.Contains(got.Message, x.has) {
			t.Errorf("warning %d = %+v, want %+v", i, got, x)
		}
	}
	// Warnings never stop the rest: local rules still grant, Check enforces.
	if _, ok := g[GrantKey{EntityID: "lock.front_door", Action: "lock.unlock"}]; !ok {
		t.Error("local rule not passed through")
	}
	if _, ok := g[GrantKey{EntityID: "light.office", Action: "light.turn_on"}]; !ok {
		t.Error("local extra rule not passed through")
	}
	if _, ok := g[GrantKey{EntityID: "script.let_me_in", Action: "script.run"}]; ok {
		t.Error("script.run with a code variable was granted")
	}
	if _, _, err := Check(g, Request{EntityID: "light.office", Action: "light.turn_on"}, nil, localOff); err == nil {
		t.Error("local extra rule passed while local is off")
	}
}

// An empty link or template is valid. It grants nothing, warns, and every
// request on it is refused.
func TestResolveEmpty(t *testing.T) {
	empty := model.Template{ID: "empty", Name: "Empty"}
	for _, tc := range []struct {
		name string
		link model.Link
		tpls []model.Template
		inv  model.Inventory
		warn Warning
	}{
		{"empty link, empty house", model.Link{}, nil, model.Inventory{}, Warning{Rule: -1, Message: "This link grants nothing yet. Add a template or a rule."}},
		{"empty link", model.Link{}, []model.Template{empty}, house(), Warning{Rule: -1, Message: "This link grants nothing yet. Add a template or a rule."}},
		{"template with no rules", model.Link{TemplateIDs: []string{"empty"}}, []model.Template{empty}, house(), Warning{Template: "empty", Rule: -1, Message: "This template has no rules yet, so it grants nothing."}},
		{"template with empty rules", model.Link{TemplateIDs: []string{"empty"}}, []model.Template{{ID: "empty", Name: "E", Rules: []model.Rule{}}}, house(), Warning{Template: "empty", Rule: -1, Message: "This template has no rules yet, so it grants nothing."}},
		{"party template with no rules", model.Link{TemplateIDs: []string{"party"}}, []model.Template{{ID: "party", Name: "P", Party: true}}, house(), Warning{Template: "party", Rule: -1, Message: "This template has no rules, so it only shows the party link."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, w := Resolve(tc.link, tc.tpls, tc.inv)
			if len(g) != 0 || !reflect.DeepEqual(w, []Warning{tc.warn}) {
				t.Fatalf("Resolve = %v, %v", g, w)
			}
			if _, _, err := Check(g, Request{EntityID: "light.office", Action: "light.turn_on"}, nil, allowAll); err == nil {
				t.Error("a request passed on an empty link")
			}
		})
	}
}
