package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

var quietLog = slog.New(slog.DiscardHandler)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Move mtime so the store sees a change on coarse-grained filesystems.
	future := time.Now().Add(time.Duration(len(content)) * time.Second)
	_ = os.Chtimes(path, future, future)
}

func TestSettingsMissingFileIsDefaults(t *testing.T) {
	s := NewSettingsStore(migrate.OpenConfig(t.TempDir(), quietLog))
	got, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got.RateLimits != model.DefaultRateLimits() || len(got.LocalAccess.Networks) != 4 || got.TrustedProxies == nil {
		t.Errorf("got %+v", got)
	}
}

// A hand-written file with only some keys gets the rest from the defaults.
func TestSettingsFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, migrate.SettingsFile, "trusted_proxies: [172.30.32.1]\nrate_limits:\n  guest_per_min: 5\n")
	got, err := NewSettingsStore(migrate.OpenConfig(dir, quietLog)).Settings()
	if err != nil {
		t.Fatal(err)
	}
	want := model.DefaultRateLimits()
	want.GuestPerMin = 5
	if got.RateLimits != want {
		t.Errorf("rate limits = %+v", got.RateLimits)
	}
	if len(got.LocalAccess.Networks) != 4 || got.TrustedProxies[0] != "172.30.32.1" || got.Version != 1 {
		t.Errorf("got %+v", got)
	}

	// An explicit empty network list stays empty.
	writeFile(t, dir, migrate.SettingsFile, "local_access:\n  networks: []\n")
	got, err = NewSettingsStore(migrate.OpenConfig(dir, quietLog)).Settings()
	if err != nil || got.LocalAccess.Networks == nil || len(got.LocalAccess.Networks) != 0 {
		t.Errorf("empty networks = %#v, %v", got.LocalAccess.Networks, err)
	}
}

// A file that does not validate gives the defaults and the error, so a
// typo never half applies.
func TestSettingsInvalidFileFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, migrate.SettingsFile, "trusted_proxies: [npm]\nrate_limits: {guest_per_min: -1}\n")
	got, err := NewSettingsStore(migrate.OpenConfig(dir, quietLog)).Settings()
	var ve *model.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	if len(got.TrustedProxies) != 0 || got.RateLimits != model.DefaultRateLimits() {
		t.Errorf("got %+v, want defaults", got)
	}
}

func TestSettingsSaveValidates(t *testing.T) {
	dir := t.TempDir()
	s := NewSettingsStore(migrate.OpenConfig(dir, quietLog))
	bad := model.DefaultSettings()
	bad.Guest.AccentColor = "red"
	if err := s.Save(bad); err == nil {
		t.Fatal("saved a bad color")
	}
	if _, err := os.Stat(filepath.Join(dir, migrate.SettingsFile)); !os.IsNotExist(err) {
		t.Fatalf("a refused save wrote the file: %v", err)
	}
	if err := s.Update(func(v *model.Settings) error { v.Guest.Title = "Hi"; v.RateLimits.AdminPerMin = 0; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings()
	if err != nil || got.Guest.Title != "Hi" || got.RateLimits.AdminPerMin != 600 {
		t.Fatalf("got %+v, %v", got, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, migrate.SettingsFile))
	if !strings.HasPrefix(string(raw), "version: 1\n") {
		t.Errorf("file = %q", raw)
	}
}

// A file that does not parse is never overwritten.
func TestSettingsBrokenFileBlocksSave(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, migrate.SettingsFile, "guest: [\n")
	s := NewSettingsStore(migrate.OpenConfig(dir, quietLog))
	if _, err := s.Settings(); err == nil {
		t.Error("no load error")
	}
	if err := s.Save(model.DefaultSettings()); err == nil {
		t.Error("saved over a broken file")
	}
}

func TestTemplatesStore(t *testing.T) {
	dir := t.TempDir()
	s := NewTemplatesStore(migrate.OpenConfig(dir, quietLog))
	got, err := s.Templates()
	if err != nil || got.Templates == nil || len(got.Templates) != 0 {
		t.Fatalf("missing file = %+v, %v", got, err)
	}
	tpl := model.Template{ID: "guest_room", Name: "Guest room", Rules: []model.Rule{{
		Select: model.Selector{Areas: []string{"guest_room"}}, Actions: []string{"light.turn_on"},
	}}}
	if err := s.Save(model.Templates{Templates: []model.Template{tpl}}); err != nil {
		t.Fatal(err)
	}
	bad := tpl
	bad.Rules = []model.Rule{{Select: model.Selector{Entities: []string{"*"}}, Actions: []string{"light.turn_on"}}}
	if err := s.Update(func(ts *model.Templates) error { ts.Templates = append(ts.Templates, bad); return nil }); err == nil {
		t.Fatal("saved a wildcard-only select")
	}
	got, err = s.Templates()
	if err != nil || len(got.Templates) != 1 || got.Templates[0].ID != "guest_room" {
		t.Fatalf("got %+v, %v", got, err)
	}

	// A hand edit that does not validate is returned with the error.
	writeFile(t, dir, migrate.TemplatesFile, "version: 1\ntemplates:\n  - {id: x, name: X, rules: []}\n")
	got, err = s.Templates()
	if err == nil || len(got.Templates) != 1 {
		t.Fatalf("invalid file = %+v, %v", got, err)
	}
}

func TestSecretsStore(t *testing.T) {
	dir := t.TempDir()
	s := NewSecretsStore(migrate.OpenData(dir, quietLog))
	got, err := s.Secrets()
	if err != nil || got.MusicAssistantToken != "" {
		t.Fatalf("missing file = %+v, %v", got, err)
	}
	if err := s.Update(func(v *model.Secrets) error { v.MusicAssistantToken = "tok"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Secrets(); err != nil || got.MusicAssistantToken != "tok" {
		t.Fatalf("got %+v, %v", got, err)
	}
	st, err := os.Stat(filepath.Join(dir, migrate.SecretsFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets mode: %v, %v", st, err)
	}
}
