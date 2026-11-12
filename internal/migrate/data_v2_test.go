package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/igrybkov/ha-guest-access/internal/model"
	"github.com/igrybkov/ha-guest-access/internal/store"
)

// A v1 link that was revoked loads as inactive, stays off and suggests a
// new URL. The old key is gone after the next save.
func TestDataV1RevokedBecomesInactive(t *testing.T) {
	dir := copyDir(t, fixtures(t, "data", 1))
	f := store.In(OpenData(dir, quietLog), LinksFile, func() model.Links { return model.Links{} })
	links, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	l, ok := links.Find("l-plumber")
	if !ok || !l.Inactive || !l.SuggestNewURL || l.Status(l.ValidUntil.Add(-time.Hour)) != model.StatusInactive {
		t.Fatalf("l-plumber = %+v", l)
	}
	if anna, _ := links.Find("l-anna"); anna.Inactive || anna.SuggestNewURL {
		t.Errorf("l-anna changed: %+v", anna)
	}
	if err := f.Update(func(*model.Links) error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, LinksFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "revoked") || !strings.Contains(string(raw), "version: 2") {
		t.Errorf("saved file:\n%s", raw)
	}
}

// The v1 to v2 step on raw links. A revoked value that is not a clean
// false keeps the link off.
func TestRevokedToInactive(t *testing.T) {
	const off = "inactive: true, suggest_new_url: true"
	for _, tc := range []struct {
		name, in, want string // links before and after the step
	}{
		{"true", `[{id: a, revoked: true}]`, `[{id: a, ` + off + `}]`},
		{"false", `[{id: a, revoked: false}]`, `[{id: a}]`},
		{"true over inactive false", `[{id: a, inactive: false, revoked: true}]`, `[{id: a, ` + off + `}]`},
		{"false keeps inactive", `[{id: a, inactive: true, revoked: false}]`, `[{id: a, inactive: true}]`},
		{"not a bool", `[{id: a, revoked: [1]}]`, `[{id: a, ` + off + `}]`},
		{"a string", `[{id: a, revoked: "no"}]`, `[{id: a, ` + off + `}]`},
		{"null", `[{id: a, revoked: null}]`, `[{id: a}]`},
		{"no key", `[{id: a}, {id: b, inactive: true}]`, `[{id: a}, {id: b, inactive: true}]`},
		{"not a mapping", `[a, [b], {id: c, revoked: true}]`, `[a, [b], {id: c, ` + off + `}]`},
		{"links a mapping", `{a: 1}`, `{a: 1}`},
		{"links a scalar", `x`, `x`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, LinksFile), []byte("version: 1\nlinks: "+tc.in+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got := migratedLinks(t, dir)
			var want yaml.Node
			if err := yaml.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !sameYAML(got, want.Content[0]) {
				out, _ := yaml.Marshal(got)
				t.Errorf("links = %s, want %s", out, tc.want)
			}
		})
	}
}

// A data set with no links file loads empty.
func TestRevokedToInactiveNoFile(t *testing.T) {
	links, err := store.In(OpenData(t.TempDir(), quietLog), LinksFile, func() model.Links { return model.Links{} }).Load()
	if err != nil || len(links.Links) != 0 {
		t.Fatalf("links = %+v, %v", links, err)
	}
}

// migratedLinks reads links.yaml in dir through the data set and returns
// its "links" node. The file must load at the current version.
func migratedLinks(t *testing.T, dir string) *yaml.Node {
	t.Helper()
	doc, err := store.In(OpenData(dir, quietLog), LinksFile, func() yaml.Node { return yaml.Node{} }).Load()
	if err != nil {
		t.Fatal(err)
	}
	root := rootOf(&doc)
	if v := store.MapGet(root, "version"); v == nil || v.Value != fmt.Sprint(DataCurrent) {
		t.Fatalf("version = %v", v)
	}
	l := store.MapGet(root, "links")
	if l == nil {
		t.Fatal("no links")
	}
	return l
}

func rootOf(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

// sameYAML compares two nodes by kind, value and children. Style is
// ignored.
func sameYAML(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || len(a.Content) != len(b.Content) {
		return false
	}
	if a.Kind == yaml.ScalarNode {
		return a.Value == b.Value
	}
	for i := range a.Content {
		if !sameYAML(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

// The data set never panics on a v1 file, and a link that loads after
// the step has no revoked key.
func FuzzRevokedToInactive(f *testing.F) {
	for _, s := range []string{
		"version: 1\nlinks: [{id: a, revoked: true}]\n",
		"version: 1\nlinks: [{id: a, revoked: false, inactive: true}]\n",
		"version: 1\nlinks: [a, {revoked: [1]}, {revoked: &x 1, b: *x}]\n",
		"links: x\n",
		"[]\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, LinksFile), []byte(in), 0o600); err != nil {
			t.Fatal(err)
		}
		doc, err := store.In(OpenData(dir, quietLog), LinksFile, func() yaml.Node { return yaml.Node{} }).Load()
		if err != nil {
			return // broken or newer: the set keeps the file as it is
		}
		root := rootOf(&doc)
		if v := store.MapGet(root, "version"); v == nil || v.Value != fmt.Sprint(DataCurrent) {
			return // not a mapping with a version the step took
		}
		list := store.MapGet(root, "links")
		if list == nil || list.Kind != yaml.SequenceNode {
			return
		}
		for _, l := range list.Content {
			if l.Kind == yaml.MappingNode && store.MapGet(l, "revoked") != nil {
				t.Fatalf("revoked kept in %q", in)
			}
		}
	})
}
