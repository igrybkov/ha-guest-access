package migrate

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/model"
	"github.com/igrybkov/ha-guest-access/internal/store"
)

var quietLog = slog.New(slog.DiscardHandler)

// Every version of each set has fixtures, and each loads at the current
// version into valid app types. A new version without fixtures fails here.
func TestFixturesOfEveryVersion(t *testing.T) {
	for v := 1; v <= ConfigCurrent; v++ {
		dir := fixtures(t, "config", v)
		set := OpenConfig(copyDir(t, dir), quietLog)
		tpl, err := store.In(set, TemplatesFile, func() model.Templates { return model.Templates{} }).Load()
		if err != nil {
			t.Fatalf("config v%d %s: %v", v, TemplatesFile, err)
		}
		if err := tpl.Validate(); err != nil || len(tpl.Templates) == 0 {
			t.Errorf("config v%d templates: %d, %v", v, len(tpl.Templates), err)
		}
		st, err := store.In(set, SettingsFile, model.DefaultSettings).Load()
		if err != nil {
			t.Fatalf("config v%d %s: %v", v, SettingsFile, err)
		}
		if err := st.Validate(); err != nil {
			t.Errorf("config v%d settings: %v", v, err)
		}
	}
	for v := 1; v <= DataCurrent; v++ {
		dir := fixtures(t, "data", v)
		set := OpenData(copyDir(t, dir), quietLog)
		links, err := store.In(set, LinksFile, func() model.Links { return model.Links{} }).Load()
		if err != nil {
			t.Fatalf("data v%d %s: %v", v, LinksFile, err)
		}
		if err := links.Validate(); err != nil || len(links.Links) == 0 {
			t.Errorf("data v%d links: %d, %v", v, len(links.Links), err)
		}
		if _, err := store.In(set, SecretsFile, func() model.Secrets { return model.Secrets{} }).Load(); err != nil {
			t.Errorf("data v%d %s: %v", v, SecretsFile, err)
		}
	}
}

// A save writes the current version, and the file reads back the same.
func TestRoundTrip(t *testing.T) {
	set := OpenConfig(copyDir(t, fixtures(t, "config", ConfigCurrent)), quietLog)
	f := store.In(set, TemplatesFile, func() model.Templates { return model.Templates{} })
	before, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Update(func(*model.Templates) error { return nil }); err != nil {
		t.Fatal(err)
	}
	after, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != ConfigCurrent || len(after.Templates) != len(before.Templates) ||
		after.Templates[0].Rules[1].Variables["source"] != "guest_access" {
		t.Fatalf("round trip changed templates: %+v", after)
	}
}

func fixtures(t *testing.T, set string, v int) string {
	t.Helper()
	dir := filepath.Join("testdata", set, fmt.Sprintf("v%d", v))
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("%s version %d has no fixtures in %s: %v", set, v, dir, err)
	}
	return dir
}

// copyDir copies fixtures to a temp dir, so a test that writes can't
// change them.
func copyDir(t *testing.T, dir string) string {
	t.Helper()
	out := t.TempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, e.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// The data set holds slugs, password hashes and the MA token, so its files
// are 0600. The config set stays 0644 for HA's git repo.
func TestFileModes(t *testing.T) {
	data := t.TempDir()
	if err := store.In(OpenData(data, quietLog), SecretsFile, func() model.Secrets { return model.Secrets{} }).Save(model.Secrets{MusicAssistantToken: "t"}); err != nil {
		t.Fatal(err)
	}
	config := t.TempDir()
	if err := store.In(OpenConfig(config, quietLog), SettingsFile, model.DefaultSettings).Save(model.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want os.FileMode
	}{{filepath.Join(data, SecretsFile), 0o600}, {filepath.Join(config, SettingsFile), 0o644}} {
		st, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != tc.want {
			t.Errorf("%s mode = %o, want %o", tc.path, got, tc.want)
		}
	}
}
