package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// wantProblems checks that err lists exactly the given paths (in any
// order), or is nil when none are given.
func wantProblems(t *testing.T, name string, err error, paths ...string) {
	t.Helper()
	if len(paths) == 0 {
		if err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
		}
		return
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("%s: want problems at %v, got %v", name, paths, err)
		return
	}
	got := map[string]bool{}
	for _, p := range ve.Problems {
		got[p.Path] = true
	}
	for _, p := range paths {
		if !got[p] {
			t.Errorf("%s: no problem at %s; got %v", name, p, ve)
		}
	}
	if len(ve.Problems) != len(paths) {
		t.Errorf("%s: %d problems, want %d: %v", name, len(ve.Problems), len(paths), ve)
	}
}

func TestAccessValidate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		a     Access
		paths []string
	}{
		{"zero", Access{}, nil},
		{"any", Access{Network: NetworkAny, NearHomeM: 300}, nil},
		{"local", Access{Network: NetworkLocal}, nil},
		{"max radius", Access{NearHomeM: MaxNearHomeM}, nil},
		{"unknown network", Access{Network: "LAN"}, []string{"network"}},
		{"case matters", Access{Network: "Local"}, []string{"network"}},
		{"negative radius", Access{NearHomeM: -1}, []string{"near_home_m"}},
		{"huge radius", Access{NearHomeM: MaxNearHomeM + 1}, []string{"near_home_m"}},
	} {
		wantProblems(t, tc.name, tc.a.Validate(), tc.paths...)
	}
}

func TestWildcardOnly(t *testing.T) {
	for pat, want := range map[string]bool{
		"*": true, "**": true, "*.*": true, ".": true, "*.": true, ".*": true,
		"light.*": false, "*.guest_room_*": false, "guest_*": false, "a": false, "*a*": false,
	} {
		if got := WildcardOnly(pat); got != want {
			t.Errorf("WildcardOnly(%q) = %v, want %v", pat, got, want)
		}
	}
}

func TestRuleValidate(t *testing.T) {
	ok := Rule{Select: Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock"}}
	with := func(fn func(*Rule)) Rule {
		r := ok
		r.Select.Entities = append([]string(nil), ok.Select.Entities...)
		r.Actions = append([]string(nil), ok.Actions...)
		fn(&r)
		return r
	}
	for _, tc := range []struct {
		name  string
		r     Rule
		paths []string
	}{
		{"ok", ok, nil},
		{"every key with patterns", with(func(r *Rule) {
			r.Select = Selector{Entities: []string{"light.guest_*"}, Areas: []string{"guest_room"}, Labels: []string{"guest_*"}, Devices: []string{"Sonos*"}}
		}), nil},
		{"empty select", with(func(r *Rule) { r.Select = Selector{} }), []string{"select"}},
		{"only exclude", with(func(r *Rule) {
			r.Select = Selector{}
			r.Exclude = Selector{Entities: []string{"lock.back"}}
		}), []string{"select"}},
		{"select star", with(func(r *Rule) { r.Select.Entities = []string{"*"} }), []string{"select.entities[0]"}},
		{"select star dot star", with(func(r *Rule) { r.Select.Entities = []string{"lock.a", "*.*"} }), []string{"select.entities[1]"}},
		{"select area star", with(func(r *Rule) { r.Select.Areas = []string{"*"} }), []string{"select.areas[0]"}},
		{"select label star", with(func(r *Rule) { r.Select.Labels = []string{"**"} }), []string{"select.labels[0]"}},
		{"select device star", with(func(r *Rule) { r.Select.Devices = []string{"*"} }), []string{"select.devices[0]"}},
		{"exclude may use star", with(func(r *Rule) { r.Exclude = Selector{Entities: []string{"*"}, Devices: []string{"*.*"}} }), nil},
		{"empty pattern", with(func(r *Rule) { r.Select.Entities = []string{""} }), []string{"select.entities[0]"}},
		{"empty exclude pattern", with(func(r *Rule) { r.Exclude.Labels = []string{""} }), []string{"exclude.labels[0]"}},
		{"padded pattern", with(func(r *Rule) { r.Select.Entities = []string{" lock.front_door"} }), []string{"select.entities[0]"}},
		{"no actions", with(func(r *Rule) { r.Actions = nil }), []string{"actions"}},
		{"bad action", with(func(r *Rule) { r.Actions = []string{"unlock"} }), []string{"actions[0]"}},
		{"upper case action", with(func(r *Rule) { r.Actions = []string{"lock.Unlock"} }), []string{"actions[0]"}},
		{"action wildcard", with(func(r *Rule) { r.Actions = []string{"lock.*"} }), []string{"actions[0]"}},
		{"three part action", with(func(r *Rule) { r.Actions = []string{"a.b.c"} }), []string{"actions[0]"}},
		{"duplicate action", with(func(r *Rule) { r.Actions = []string{"lock.lock", "lock.lock"} }), []string{"actions[1]"}},
		{"bad access", with(func(r *Rule) { r.Access = &Access{Network: "wifi"} }), []string{"access.network"}},
		{"script variables", with(func(r *Rule) {
			r.Select.Entities = []string{"script.let_me_in"}
			r.Actions = []string{ActionScriptRun, ActionScriptCancel}
			r.Variables = map[string]any{"source": "guest_access"}
		}), nil},
		{"variables without script.run", with(func(r *Rule) { r.Variables = map[string]any{"a": 1} }), []string{"variables"}},
		{"variables with cancel only", with(func(r *Rule) {
			r.Actions = []string{ActionScriptCancel}
			r.Variables = map[string]any{"a": 1}
		}), []string{"variables"}},
		{"code variable", with(func(r *Rule) {
			r.Actions = []string{ActionScriptRun}
			r.Variables = map[string]any{"code": "1234"}
		}), []string{"variables"}},
		{"CODE variable", with(func(r *Rule) {
			r.Actions = []string{ActionScriptRun}
			r.Variables = map[string]any{"Code": "1234"}
		}), []string{"variables"}},
		{"nested code variable", with(func(r *Rule) {
			r.Actions = []string{ActionScriptRun}
			r.Variables = map[string]any{"alarm": map[string]any{"opts": []any{map[string]any{"code": 1}}}}
		}), []string{"variables"}},
		{"code from yaml any-map", with(func(r *Rule) {
			r.Actions = []string{ActionScriptRun}
			r.Variables = map[string]any{"alarm": map[any]any{"code": 1}}
		}), []string{"variables"}},
	} {
		wantProblems(t, tc.name, tc.r.Validate(), tc.paths...)
	}
}

func TestTemplateValidate(t *testing.T) {
	rule := Rule{Select: Selector{Areas: []string{"guest_room"}}, Actions: []string{"light.turn_on"}}
	for _, tc := range []struct {
		name  string
		t     Template
		paths []string
	}{
		{"ok", Template{ID: "guest_room", Name: "Guest room", Rules: []Rule{rule}}, nil},
		{"party and access", Template{ID: "music-1", Name: "Music", Party: true, Access: &Access{Network: NetworkLocal}, Rules: []Rule{rule}}, nil},
		{"no id", Template{Name: "x", Rules: []Rule{rule}}, []string{"id"}},
		{"bad id", Template{ID: "Guest Room", Name: "x", Rules: []Rule{rule}}, []string{"id"}},
		{"id starts with dash", Template{ID: "-x", Name: "x", Rules: []Rule{rule}}, []string{"id"}},
		{"id too long", Template{ID: strings.Repeat("a", 65), Name: "x", Rules: []Rule{rule}}, []string{"id"}},
		{"blank name", Template{ID: "x", Name: "  ", Rules: []Rule{rule}}, []string{"name"}},
		{"no rules", Template{ID: "x", Name: "x"}, []string{"rules"}},
		{"bad access", Template{ID: "x", Name: "x", Access: &Access{NearHomeM: -5}, Rules: []Rule{rule}}, []string{"access.near_home_m"}},
		{"bad rule path", Template{ID: "x", Name: "x", Rules: []Rule{rule, {Select: Selector{Entities: []string{"*"}}, Actions: []string{"light.turn_on"}}}},
			[]string{"rules[1].select.entities[0]"}},
	} {
		wantProblems(t, tc.name, tc.t.Validate(), tc.paths...)
	}
}

func TestTemplatesValidate(t *testing.T) {
	tpl := Template{ID: "a", Name: "A", Rules: []Rule{{Select: Selector{Entities: []string{"lock.x"}}, Actions: []string{"lock.lock"}}}}
	wantProblems(t, "unique", Templates{Templates: []Template{tpl}}.Validate())
	wantProblems(t, "duplicate id", Templates{Templates: []Template{tpl, tpl}}.Validate(), "templates[1].id")
	bad := tpl
	bad.Name = ""
	wantProblems(t, "nested path", Templates{Templates: []Template{tpl, bad}}.Validate(), "templates[1].name", "templates[1].id")

	ts := Templates{Templates: []Template{tpl}}
	if got, ok := ts.Find("a"); !ok || got.Name != "A" {
		t.Errorf("Find(a) = %+v, %v", got, ok)
	}
	if _, ok := ts.Find("A"); ok {
		t.Error("Find is case-insensitive")
	}
}

func TestRuleAccessReplacesTemplateAccess(t *testing.T) {
	r := Rule{Select: Selector{Entities: []string{"lock.x"}}, Actions: []string{"lock.lock"}}
	own := r
	own.Access = &Access{NearHomeM: 50}
	tpl := Template{Access: &Access{Network: NetworkLocal, NearHomeM: 300}, Rules: []Rule{r, own}}
	if got := tpl.RuleAccess(0); got != (Access{Network: NetworkLocal, NearHomeM: 300}) {
		t.Errorf("rule without access = %+v, want the template's", got)
	}
	// Replaced as a whole: the template's network does not leak in.
	if got := tpl.RuleAccess(1); got != (Access{NearHomeM: 50}) {
		t.Errorf("rule with access = %+v", got)
	}
	if !tpl.UsesLocalNetwork() {
		t.Error("UsesLocalNetwork = false")
	}
	tpl.Access = nil
	if got := tpl.RuleAccess(0); got != (Access{}) {
		t.Errorf("no access anywhere = %+v", got)
	}
	if tpl.UsesLocalNetwork() {
		t.Error("UsesLocalNetwork = true")
	}
}

func TestLinkValidate(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	ok := Link{ID: "l1", Name: "Anna", Slug: "k3Xq9vTzR0aW1bN2cD4eFg", CreatedAt: now, ValidUntil: now.Add(time.Hour), TemplateIDs: []string{"house_entry"}}
	with := func(fn func(*Link)) Link { l := ok; fn(&l); return l }
	rule := Rule{Select: Selector{Entities: []string{"switch.heater"}}, Actions: []string{"switch.turn_on"}}
	for _, tc := range []struct {
		name  string
		l     Link
		paths []string
	}{
		{"ok", ok, nil},
		{"extra only", with(func(l *Link) { l.TemplateIDs = nil; l.Extra = []Rule{rule} }), nil},
		{"window", with(func(l *Link) { l.ValidFrom = now }), nil},
		{"no id", with(func(l *Link) { l.ID = "" }), []string{"id"}},
		{"blank name", with(func(l *Link) { l.Name = "" }), []string{"name"}},
		{"short slug", with(func(l *Link) { l.Slug = "short" }), []string{"slug"}},
		{"slug with slash", with(func(l *Link) { l.Slug = "abcdefghijkl/x" }), []string{"slug"}},
		{"slug with space", with(func(l *Link) { l.Slug = "abcdefghijkl x" }), []string{"slug"}},
		{"no end", with(func(l *Link) { l.ValidUntil = time.Time{} }), []string{"valid_until"}},
		{"ends before start", with(func(l *Link) { l.ValidFrom = now.Add(2 * time.Hour) }), []string{"valid_until"}},
		{"ends at start", with(func(l *Link) { l.ValidFrom = l.ValidUntil }), []string{"valid_until"}},
		{"grants nothing", with(func(l *Link) { l.TemplateIDs = nil }), []string{"templates"}},
		{"empty template id", with(func(l *Link) { l.TemplateIDs = []string{""} }), []string{"templates[0]"}},
		{"duplicate template", with(func(l *Link) { l.TemplateIDs = []string{"a", "a"} }), []string{"templates[1]"}},
		{"bad extra rule", with(func(l *Link) {
			l.Extra = []Rule{{Select: Selector{Entities: []string{"*.*"}}, Actions: []string{"switch.turn_on"}}}
		}),
			[]string{"extra[0].select.entities[0]"}},
		{"negative uses", with(func(l *Link) { l.UseCount = -1 }), []string{"use_count"}},
		{"passwords", with(func(l *Link) { l.Passwords = []LinkPassword{pw("p1"), pw("p2")} }), nil},
		{"duplicate password id", with(func(l *Link) { l.Passwords = []LinkPassword{pw("p1"), pw("p1")} }), []string{"passwords[1].id"}},
		{"empty password id", with(func(l *Link) { l.Passwords = []LinkPassword{pw("")} }), []string{"passwords[0].id"}},
		{"blank password label", with(func(l *Link) { p := pw("p1"); p.Label = " "; l.Passwords = []LinkPassword{p} }), []string{"passwords[0].label"}},
		{"no hash", with(func(l *Link) { p := pw("p1"); p.Hash = ""; l.Passwords = []LinkPassword{p} }), []string{"passwords[0].hash"}},
		{"plain text hash", with(func(l *Link) { p := pw("p1"); p.Hash = "5551234567"; l.Passwords = []LinkPassword{p} }), []string{"passwords[0].hash"}},
		{"bcrypt hash", with(func(l *Link) { p := pw("p1"); p.Hash = "$2a$10$abc"; l.Passwords = []LinkPassword{p} }), []string{"passwords[0].hash"}},
		{"devices", with(func(l *Link) {
			l.Devices = []LinkDevice{dev("d1", "admin"), dev("d2", "device:d1"), dev("d3", "claim"), dev("d4", "ha")}
		}), nil},
		{"approver gone", with(func(l *Link) { l.Devices = []LinkDevice{dev("d2", "device:d1")} }), nil},
		// The cap is an app option, checked on add, not here.
		{"many devices", with(func(l *Link) {
			for i := range 20 {
				l.Devices = append(l.Devices, dev(fmt.Sprintf("d%d", i), "admin"))
			}
		}), nil},
		{"duplicate device id", with(func(l *Link) { l.Devices = []LinkDevice{dev("d1", "admin"), dev("d1", "ha")} }), []string{"devices[1].id"}},
		{"bad device id", with(func(l *Link) { l.Devices = []LinkDevice{dev("D 1", "admin")} }), []string{"devices[0].id"}},
		{"unknown approver", with(func(l *Link) { l.Devices = []LinkDevice{dev("d1", "root")} }), []string{"devices[0].approved_by"}},
		{"empty device approver", with(func(l *Link) { l.Devices = []LinkDevice{dev("d1", "device:")} }), []string{"devices[0].approved_by"}},
		{"no approval time", with(func(l *Link) { d := dev("d1", "admin"); d.ApprovedAt = time.Time{}; l.Devices = []LinkDevice{d} }), []string{"devices[0].approved_at"}},
		{"control in device", with(func(l *Link) { d := dev("d1", "admin"); d.Device = "iPhone\x1b[2J"; l.Devices = []LinkDevice{d} }), []string{"devices[0].device"}},
		{"long label", with(func(l *Link) {
			d := dev("d1", "admin")
			d.Label = strings.Repeat("x", MaxDeviceTextLen+1)
			l.Devices = []LinkDevice{d}
		}), []string{"devices[0].label"}},
	} {
		wantProblems(t, tc.name, tc.l.Validate(), tc.paths...)
	}
}

// dev returns a valid device with id, approved by by.
func dev(id, by string) LinkDevice {
	return LinkDevice{ID: id, Device: "iPhone, Safari", ApprovedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), ApprovedBy: by}
}

func TestApprovalModes(t *testing.T) {
	for _, tc := range []struct {
		a         Approval
		on, claim bool
	}{
		{Approval{}, false, false},
		{Approval{Manual: true}, true, false},
		{Approval{SelfService: true}, true, true},
		{Approval{Manual: true, SelfService: true}, true, false},
	} {
		if tc.a.On() != tc.on || tc.a.Claim() != tc.claim {
			t.Errorf("%+v: On %v Claim %v, want %v %v", tc.a, tc.a.On(), tc.a.Claim(), tc.on, tc.claim)
		}
	}
	l := Link{Devices: []LinkDevice{dev("d1", "admin")}}
	if d, ok := l.Device("d1"); !ok || d.ID != "d1" {
		t.Errorf("Device(d1) = %+v, %v", d, ok)
	}
	if _, ok := l.Device("d2"); ok {
		t.Error("Device(d2) found")
	}
}

// With approval off and no devices, links.yaml stays as it was.
func TestApprovalOmittedWhenOff(t *testing.T) {
	raw, err := yaml.Marshal(Link{ID: "l1"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(raw); strings.Contains(s, "approval") || strings.Contains(s, "devices") {
		t.Errorf("off approval written:\n%s", s)
	}
	raw, _ = yaml.Marshal(Link{ID: "l1", Approval: Approval{SelfService: true}, Devices: []LinkDevice{dev("d1", "claim")}})
	if s := string(raw); !strings.Contains(s, "self_service: true") || strings.Contains(s, "manual") || strings.Contains(s, "last_seen_at") {
		t.Errorf("approval YAML:\n%s", s)
	}
}

// pw returns a valid password with id.
func pw(id string) LinkPassword {
	const h = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2g"
	return LinkPassword{ID: id, Label: "Anna", Hash: h}
}

func TestLinkPasswordLookupAndJSON(t *testing.T) {
	l := Link{Passwords: []LinkPassword{pw("p1")}}
	if got, ok := l.Password("p1"); !ok || got.Label != "Anna" {
		t.Errorf("Password(p1) = %+v, %v", got, ok)
	}
	if _, ok := l.Password("p2"); ok {
		t.Error("Password(p2) found")
	}
	raw, err := json.Marshal(l)
	if err != nil || strings.Contains(string(raw), "argon2id") {
		t.Fatalf("hashes in JSON: %s, %v", raw, err)
	}
}

func TestLinkValidateHidesSlug(t *testing.T) {
	l := Link{ID: "l1", Name: "x", Slug: "secret-but-bad/", ValidUntil: time.Now(), TemplateIDs: []string{"a"}}
	if err := l.Validate(); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaks the slug or is nil: %v", err)
	}
}

func TestLinksValidate(t *testing.T) {
	until := time.Now().Add(time.Hour)
	a := Link{ID: "a", Name: "A", Slug: "aaaaaaaaaaaa", ValidUntil: until, TemplateIDs: []string{"t"}}
	b := Link{ID: "b", Name: "B", Slug: "bbbbbbbbbbbb", ValidUntil: until, TemplateIDs: []string{"t"}}
	wantProblems(t, "ok", Links{Links: []Link{a, b}}.Validate())
	dupID := b
	dupID.ID = "a"
	wantProblems(t, "duplicate id", Links{Links: []Link{a, dupID}}.Validate(), "links[1].id")
	dupSlug := b
	dupSlug.Slug = a.Slug
	wantProblems(t, "duplicate slug", Links{Links: []Link{a, dupSlug}}.Validate(), "links[1].slug")
	if got, ok := (Links{Links: []Link{a, b}}).Find("b"); !ok || got.Name != "B" {
		t.Errorf("Find(b) = %+v, %v", got, ok)
	}
}

func TestLinkStatus(t *testing.T) {
	from := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	until := from.Add(48 * time.Hour)
	l := Link{ValidFrom: from, ValidUntil: until}
	for _, tc := range []struct {
		name string
		l    Link
		now  time.Time
		want string
	}{
		{"before", l, from.Add(-time.Second), StatusScheduled},
		{"at start", l, from, StatusActive},
		{"inside", l, from.Add(time.Hour), StatusActive},
		{"at end", l, until, StatusExpired},
		{"after", l, until.Add(time.Hour), StatusExpired},
		{"no start", Link{ValidUntil: until}, from.Add(-24 * time.Hour), StatusActive},
		{"revoked inside", Link{ValidFrom: from, ValidUntil: until, Revoked: true}, from.Add(time.Hour), StatusRevoked},
		{"no end is expired", Link{}, from, StatusExpired},
	} {
		if got := tc.l.Status(tc.now); got != tc.want {
			t.Errorf("%s: Status = %s, want %s", tc.name, got, tc.want)
		}
		if got := tc.l.Active(tc.now); got != (tc.want == StatusActive) {
			t.Errorf("%s: Active = %v", tc.name, got)
		}
	}
}

func TestLinkUsedAtAndLocal(t *testing.T) {
	at := time.Date(2026, 11, 11, 8, 5, 0, 0, time.UTC)
	l := Link{Used: []Use{{EntityID: "lock.front_door", Action: "lock.unlock", At: at}}}
	if got, ok := l.UsedAt("lock.front_door", "lock.unlock"); !ok || !got.Equal(at) {
		t.Errorf("UsedAt = %v, %v", got, ok)
	}
	for _, k := range [][2]string{{"lock.front_door", "lock.lock"}, {"lock.back_door", "lock.unlock"}, {"LOCK.FRONT_DOOR", "lock.unlock"}} {
		if _, ok := l.UsedAt(k[0], k[1]); ok {
			t.Errorf("UsedAt(%v) = true", k)
		}
	}
	if l.UsesLocalNetwork() {
		t.Error("UsesLocalNetwork with no extra rules")
	}
	l.Extra = []Rule{{Access: &Access{Network: NetworkLocal}}}
	if !l.UsesLocalNetwork() {
		t.Error("UsesLocalNetwork = false")
	}
}

func TestSettingsValidate(t *testing.T) {
	wantProblems(t, "defaults", DefaultSettings().Validate())
	for _, tc := range []struct {
		name  string
		fn    func(*Settings)
		paths []string
	}{
		{"proxies as IP and CIDR", func(s *Settings) { s.TrustedProxies = []string{"172.30.32.1", "172.30.33.0/24", "::1"} }, nil},
		{"MA https", func(s *Settings) { s.MusicAssistant.URL = "https://ma.example:8095" }, nil},
		{"lower case color", func(s *Settings) { s.Guest.AccentColor = "#1e88e5" }, nil},
		{"network is an IP", func(s *Settings) { s.LocalAccess.Networks = []string{"192.168.1.1"} }, []string{"local_access.networks[0]"}},
		{"network garbage", func(s *Settings) { s.LocalAccess.Networks = []string{"10.0.0.0/8", "lan"} }, []string{"local_access.networks[1]"}},
		{"bad proxy", func(s *Settings) { s.TrustedProxies = []string{"npm"} }, []string{"trusted_proxies[0]"}},
		{"negative radius", func(s *Settings) { s.HomeRadiusDefaultM = -1 }, []string{"home_radius_default_m"}},
		{"MA no scheme", func(s *Settings) { s.MusicAssistant.URL = "ma:8095" }, []string{"music_assistant.url"}},
		{"MA ftp", func(s *Settings) { s.MusicAssistant.URL = "ftp://ma" }, []string{"music_assistant.url"}},
		{"color name", func(s *Settings) { s.Guest.AccentColor = "red" }, []string{"guest.accent_color"}},
		{"zero guest rate", func(s *Settings) { s.RateLimits.GuestPerMin = 0 }, []string{"rate_limits.guest_per_min"}},
		{"negative lockout", func(s *Settings) { s.RateLimits.PasswordLockoutMin = -1 }, []string{"rate_limits.password_lockout_min"}},
		{"all rates zero", func(s *Settings) { s.RateLimits = RateLimits{} }, []string{
			"rate_limits.guest_per_min", "rate_limits.guest_burst", "rate_limits.actions_per_min",
			"rate_limits.actions_burst", "rate_limits.max_streams_per_link",
			"rate_limits.misses_per_hour_per_ip", "rate_limits.misses_per_hour_global",
			"rate_limits.password_fails_per_ip", "rate_limits.password_fails_per_link",
			"rate_limits.password_lockout_min", "rate_limits.admin_per_min",
			"rate_limits.continuous_per_min", "rate_limits.continuous_burst",
			"rate_limits.assets_per_min", "rate_limits.assets_burst", "rate_limits.password_link_delay_s"}},
		{"zero link delay", func(s *Settings) { s.RateLimits.PasswordLinkDelayS = 0 }, []string{"rate_limits.password_link_delay_s"}},
		{"negative assets rate", func(s *Settings) { s.RateLimits.AssetsPerMin = -1 }, []string{"rate_limits.assets_per_min"}},
		{"slider interval too short", func(s *Settings) { s.Guest.SliderIntervalMS = 99 }, []string{"guest.slider_interval_ms"}},
		{"color css injection", func(s *Settings) { s.Guest.AccentColor = "#fff;}body{" }, []string{"guest.accent_color"}},
	} {
		s := DefaultSettings()
		tc.fn(&s)
		wantProblems(t, tc.name, s.Validate(), tc.paths...)
	}
}

func TestDefaultSettings(t *testing.T) {
	s := DefaultSettings()
	if s.LocalAccess.Enabled {
		t.Error("local access is on by default")
	}
	if s.RateLimits != (RateLimits{GuestPerMin: 20, GuestBurst: 15, ActionsPerMin: 120, ActionsBurst: 40, ContinuousPerMin: 600, ContinuousBurst: 120, MaxStreamsPerLink: 10, MissesPerHourPerIP: 10, MissesPerHourGlobal: 100,
		PasswordFailsPerIP: 5, PasswordFailsPerLink: 30, PasswordLockoutMin: 60, PasswordLinkDelayS: 10, AdminPerMin: 600,
		AssetsPerMin: 300, AssetsBurst: 100}) {
		t.Errorf("default rate limits = %+v", s.RateLimits)
	}
	if len(s.TrustedProxies) != 0 {
		t.Error("trusted proxies are not empty by default")
	}
	nets, err := ParsePrefixes(s.LocalAccess.Networks)
	if err != nil || len(nets) != 4 {
		t.Fatalf("default networks: %v, %v", nets, err)
	}
	// Changing one copy does not change the defaults.
	s.LocalAccess.Networks[0] = "1.2.3.0/24"
	if DefaultSettings().LocalAccess.Networks[0] != "10.0.0.0/8" {
		t.Error("DefaultSettings shares its slice")
	}
}

func TestParsePrefix(t *testing.T) {
	for in, want := range map[string]string{
		"172.30.32.1":    "172.30.32.1/32",
		"10.1.2.3/8":     "10.0.0.0/8",
		"fc00::/7":       "fc00::/7",
		"::1":            "::1/128",
		" 192.168.0.1 ":  "192.168.0.1/32",
		"192.168.0.0/16": "192.168.0.0/16",
	} {
		p, err := ParsePrefix(in)
		if err != nil || p.String() != want {
			t.Errorf("ParsePrefix(%q) = %v, %v; want %s", in, p, err, want)
		}
	}
	for _, in := range []string{"", "npm", "10.0.0.0/33", "1.2.3"} {
		if _, err := ParsePrefix(in); err == nil {
			t.Errorf("ParsePrefix(%q) accepted", in)
		}
	}
	if _, err := ParsePrefixes([]string{"10.0.0.0/8", "bad"}); err == nil {
		t.Error("ParsePrefixes accepted a bad entry")
	}
}

func TestYAMLShape(t *testing.T) {
	src := `
version: 1
templates:
  - id: house_entry
    name: House entry
    access: {network: any, near_home_m: 300}
    rules:
      - select: {entities: [lock.front_door], devices: ["Front*"]}
        exclude: {labels: [private]}
        actions: [lock.unlock]
        access: {network: local}
      - select: {entities: [script.let_me_in]}
        actions: [script.run]
        variables: {source: guest_access}
    party: true
`
	var ts Templates
	if err := yaml.Unmarshal([]byte(src), &ts); err != nil {
		t.Fatal(err)
	}
	if err := ts.Validate(); err != nil {
		t.Fatal(err)
	}
	tpl := ts.Templates[0]
	if !tpl.Party || tpl.Access.NearHomeM != 300 || tpl.Rules[0].Access.Network != NetworkLocal ||
		tpl.Rules[0].Select.Devices[0] != "Front*" || tpl.Rules[0].Exclude.Labels[0] != "private" ||
		tpl.Rules[1].Variables["source"] != "guest_access" {
		t.Fatalf("decoded %+v", tpl)
	}
}

func TestInventoryHelpers(t *testing.T) {
	if d := (Entity{ID: "light.guest_room"}).Domain(); d != "light" {
		t.Errorf("Domain = %q", d)
	}
	if d := Domain("nodot"); d != "" {
		t.Errorf("Domain(nodot) = %q", d)
	}
	if n := (Device{Name: "Sonos One", NameByUser: "Office speaker"}).DisplayName(); n != "Office speaker" {
		t.Errorf("DisplayName = %q", n)
	}
	if n := (Device{Name: "Sonos One"}).DisplayName(); n != "Sonos One" {
		t.Errorf("DisplayName = %q", n)
	}
}

func TestRateLimitsWithDefaults(t *testing.T) {
	if got := (RateLimits{}).WithDefaults(); got != DefaultRateLimits() {
		t.Errorf("zero WithDefaults = %+v", got)
	}
	got := RateLimits{GuestPerMin: 99, PasswordLockoutMin: -1}.WithDefaults()
	if got.GuestPerMin != 99 || got.PasswordLockoutMin != -1 || got.AdminPerMin != 600 {
		t.Errorf("WithDefaults = %+v; set values must stay, negatives are left for Validate", got)
	}
}
