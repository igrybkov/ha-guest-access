package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// appConfig reads the options and schema of guest_access/config.yaml.
func appConfig(t *testing.T) (options, schema map[string]any) {
	t.Helper()
	raw, err := os.ReadFile("../../guest_access/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Options map[string]any `yaml:"options"`
		Schema  map[string]any `yaml:"schema"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Options, doc.Schema
}

// The defaults in config.yaml load to DefaultOptions, so the app and
// Supervisor agree on every default.
func TestConfigYAMLDefaultsMatch(t *testing.T) {
	opts, _ := appConfig(t)
	raw, err := json.Marshal(opts)
	if err != nil {
		t.Fatal(err)
	}
	r, err := parseOptions(raw)
	if err != nil {
		t.Fatalf("config.yaml options do not parse: %v", err)
	}
	got, err := r.options()
	if err != nil {
		t.Fatalf("config.yaml options do not validate: %v", err)
	}
	if !reflect.DeepEqual(got, DefaultOptions()) {
		t.Errorf("config.yaml defaults:\n got %+v\nwant %+v", got, DefaultOptions())
	}
}

// Every key the app reads is in the schema, and every schema key is read.
func TestConfigYAMLSchemaMatchesOptions(t *testing.T) {
	_, schema := appConfig(t)
	raw, _ := json.Marshal(defaultRaw())
	var app map[string]any
	_ = json.Unmarshal(raw, &app)
	if got, want := keyPaths("", schema), keyPaths("", app); !slices.Equal(got, want) {
		t.Errorf("schema keys:\n got %v\nwant %v", got, want)
	}
	// Optional keys have no default, or Supervisor makes them required.
	opts, _ := appConfig(t)
	for _, p := range keyPaths("", schema) {
		if typ, ok := lookup(schema, p).(string); ok && strings.HasSuffix(typ, "?") && lookup(opts, p) != nil {
			t.Errorf("%s is optional but has a default", p)
		}
	}
}

func keyPaths(prefix string, m map[string]any) []string {
	var out []string
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out = append(out, keyPaths(prefix+k+".", sub)...)
			continue
		}
		out = append(out, prefix+k)
	}
	slices.Sort(out)
	return out
}

func lookup(m map[string]any, path string) any {
	var v any = m
	for _, k := range strings.Split(path, ".") {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[k]
	}
	return v
}

func TestParseOptionsFull(t *testing.T) {
	r, err := parseOptions([]byte(`{
		"log_level": "debug",
		"public_base_url": "https://guest.example.com",
		"local_access": {"enabled": true, "networks": ["192.168.1.0/24"]},
		"trusted_proxies": ["172.30.33.0/24"],
		"music_assistant": {"url": "http://ma:8095", "token": "secret", "timeout_ms": 500},
		"guest": {"title": "Hi", "welcome": "Make yourself at home", "accent_color": "#112233"},
		"location": {"ttl_min": 5, "max_accuracy_m": 200},
		"rate_limits": {"enabled": false, "guest_per_min": 7, "password_window_per_ip_min": 30},
		"links": {"custom_slug_min_len": 16, "custom_slug_max_len": 20},
		"approval": {"max_devices": 2}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	o, err := r.options()
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case o.PublicBaseURL != "https://guest.example.com",
		!o.Settings.LocalAccess.Enabled || o.Settings.LocalAccess.Networks[0] != "192.168.1.0/24",
		o.Settings.TrustedProxies[0] != "172.30.33.0/24",
		o.Settings.MusicAssistant.URL != "http://ma:8095", o.MusicAssistantToken != "secret",
		!o.MusicAssistantEnabled(), o.MATimeout != 500*time.Millisecond,
		o.Settings.Guest.AccentColor != "#112233",
		o.NearTTL != 5*time.Minute, o.MaxAccuracyM != 200,
		o.LimitsEnabled, o.Settings.RateLimits.GuestPerMin != 7, o.Settings.RateLimits.GuestBurst != 15,
		o.PasswordWindowPerIP != 30*time.Minute, o.PasswordWindowPerLink != time.Hour,
		o.CustomSlugMinLen != 16, o.CustomSlugMaxLen != 20,
		o.Approval.MaxDevices != 2, o.Approval.MaxPending != 3:
		t.Errorf("options = %+v", o)
	}
}

func TestOptionsErrorsNameEachKey(t *testing.T) {
	r, _ := parseOptions([]byte(`{"location":{"ttl_min":0},"links":{"custom_slug_min_len":30,"custom_slug_max_len":20},"guest":{"title":"x","accent_color":"red"}}`))
	_, err := r.options()
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"location.ttl_min", "links.custom_slug_max_len", "guest.accent_color"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestDefaultOptions(t *testing.T) {
	o := DefaultOptions()
	if !o.LimitsEnabled || o.MusicAssistantEnabled() || o.PublicBaseURL != "" ||
		o.MATimeout != 2*time.Second || o.NearTTL != 15*time.Minute || o.GuestBodyBytes != 16<<10 {
		t.Errorf("defaults = %+v", o)
	}
}

// Every option has a translation with a name, nested ones under
// "fields", as Supervisor's SCHEMA_TRANSLATION_CONFIGURATION wants.
func TestTranslationsCoverEveryOption(t *testing.T) {
	_, schema := appConfig(t)
	raw, err := os.ReadFile("../../guest_access/translations/en.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var tr struct {
		Configuration map[string]any `yaml:"configuration"`
	}
	if err := yaml.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	for _, p := range keyPaths("", schema) {
		var node any = tr.Configuration
		for i, k := range strings.Split(p, ".") {
			m, _ := node.(map[string]any)
			if i > 0 {
				m, _ = m["fields"].(map[string]any)
			}
			node = m[k]
		}
		m, _ := node.(map[string]any)
		if name, _ := m["name"].(string); name == "" {
			t.Errorf("%s has no translation name", p)
		}
	}
	for k, v := range tr.Configuration {
		if m, _ := v.(map[string]any); m["name"] == nil {
			t.Errorf("group %s has no name", k)
		}
	}
	enabled := lookup(tr.Configuration, "rate_limits.fields.enabled.description")
	if s, _ := enabled.(string); !strings.Contains(s, "Not recommended to turn off") {
		t.Errorf("rate_limits.enabled description = %q", s)
	}
}

// The HA timeouts, stream timings and audit options reach Options.
func TestParseOptionsTimeoutsAndAudit(t *testing.T) {
	r, err := parseOptions([]byte(`{
		"ha": {"call_timeout_s": 3, "event_timeout_s": 2},
		"streams": {"recheck_s": 7, "heartbeat_s": 40, "retry_after_s": 60},
		"audit": {"enabled": false, "max_file_kib": 128}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	o, err := r.options()
	if err != nil {
		t.Fatal(err)
	}
	if o.HACallTimeout != 3*time.Second || o.HAEventTimeout != 2*time.Second ||
		o.StreamCheck != 7*time.Second || o.StreamHeartbeat != 40*time.Second || o.StreamRetry != time.Minute ||
		o.AuditEnabled || o.AuditMaxFileBytes != 128<<10 {
		t.Errorf("options = %+v", o)
	}
	d := DefaultOptions()
	if d.HACallTimeout != 10*time.Second || d.HAEventTimeout != 5*time.Second || d.StreamCheck != 5*time.Second ||
		d.StreamHeartbeat != 25*time.Second || d.StreamRetry != 30*time.Second || !d.AuditEnabled || d.AuditMaxFileBytes != 2048<<10 {
		t.Errorf("defaults = %+v", d)
	}
	if _, err := (func() (Options, error) {
		r, _ := parseOptions([]byte(`{"streams": {"heartbeat_s": 1}}`))
		return r.options()
	})(); err == nil || !strings.Contains(err.Error(), "streams.heartbeat_s") {
		t.Errorf("short heartbeat: %v", err)
	}
}

// Every option row in DOCS.md names a real option and its default from
// config.yaml.
func TestDocsDefaultsMatch(t *testing.T) {
	opts, _ := appConfig(t)
	raw, err := os.ReadFile("../../guest_access/DOCS.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, line := range strings.Split(string(raw), "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 || !strings.HasPrefix(strings.TrimSpace(cells[1]), "`") {
			continue
		}
		key := strings.Trim(strings.TrimSpace(cells[1]), "`")
		def := strings.Trim(strings.TrimSpace(cells[2]), "`")
		var v any = opts
		for _, part := range strings.Split(key, ".") {
			m, ok := v.(map[string]any)
			if !ok {
				v = nil
				break
			}
			v = m[part]
		}
		if v == nil {
			t.Errorf("DOCS.md row %q: no such option in config.yaml", key)
			continue
		}
		if got := fmt.Sprint(v); got != def {
			t.Errorf("DOCS.md %s default %q, config.yaml %q", key, def, got)
		}
		rows++
	}
	if rows == 0 {
		t.Error("no option rows in DOCS.md")
	}
}
