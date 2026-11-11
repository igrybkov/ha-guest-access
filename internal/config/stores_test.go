package config

import (
	"log/slog"
	"os"
	"path/filepath"
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
