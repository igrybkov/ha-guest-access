package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// The test schema:
//
//	v1: a.yaml {version, names: [...]} and old.yaml, a bare list of names
//	v2: old.yaml merged into a.yaml's names, old.yaml gone
//	v3: a.yaml names renamed to items
func testSteps() []Step {
	return []Step{
		func(t *Tree) error { // 1 → 2
			old, _, ok := t.Get("old.yaml")
			if !ok {
				return nil
			}
			a, _, ok := t.Get("a.yaml")
			if !ok {
				a = &yaml.Node{Kind: yaml.MappingNode}
				MapSet(a, "names", &yaml.Node{Kind: yaml.SequenceNode})
				t.Put("a.yaml", a)
			}
			names := MapGet(a, "names")
			names.Content = append(names.Content, old.Content...)
			t.Delete("old.yaml")
			return nil
		},
		func(t *Tree) error { // 2 → 3
			a, _, ok := t.Get("a.yaml")
			if !ok {
				return nil
			}
			for i := 0; i+1 < len(a.Content); i += 2 {
				if a.Content[i].Value == "names" {
					a.Content[i].Value = "items"
				}
			}
			return nil
		},
	}
}

type v3doc struct {
	Version int      `yaml:"version"`
	Items   []string `yaml:"items"`
}

func newTestSet(dir string, current int) *Set {
	return OpenSet(dir, SetOptions{Current: current, Files: []string{"a.yaml", "old.yaml", "b.yaml"}, Steps: testSteps()[:current-1]})
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Some filesystems keep mtime at a coarse grain; move it so a change shows.
	future := time.Now().Add(time.Duration(len(content)) * time.Second)
	_ = os.Chtimes(filepath.Join(dir, name), future, future)
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMigrateFromEachVersionOnRead(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
	}{
		{"from v1", map[string]string{"a.yaml": "version: 1\nnames: [x]\n", "old.yaml": "[y]\n", "b.yaml": "version: 1\nk: v\n"}},
		{"from v2", map[string]string{"a.yaml": "version: 2\nnames: [x, y]\n", "b.yaml": "version: 2\nk: v\n"}},
		{"mixed", map[string]string{"a.yaml": "version: 2\nnames: [x, y]\n", "b.yaml": "k: v\n"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for n, c := range tt.files {
				write(t, dir, n, c)
			}
			set := newTestSet(dir, 3)
			v, err := In(set, "a.yaml", func() v3doc { return v3doc{} }).Load()
			if err != nil {
				t.Fatal(err)
			}
			if v.Version != 3 || strings.Join(v.Items, ",") != "x,y" {
				t.Fatalf("got %+v", v)
			}
			// Reading wrote nothing.
			for n, c := range tt.files {
				if got := read(t, dir, n); got != c {
					t.Errorf("%s changed on read: %q", n, got)
				}
			}
		})
	}
}

func TestFirstWriteFlushesSetAndDropsObsolete(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "version: 1\n# kept comment\nnames: [x]\n")
	write(t, dir, "old.yaml", "[y]\n")
	write(t, dir, "b.yaml", "k: v\n")
	set := newTestSet(dir, 3)
	b := In(set, "b.yaml", func() map[string]any { return nil })
	if err := b.Update(func(m *map[string]any) error { (*m)["k"] = "w"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.yaml")); !os.IsNotExist(err) {
		t.Fatalf("obsolete file kept: %v", err)
	}
	a := read(t, dir, "a.yaml")
	if !strings.HasPrefix(a, "version: 3\n") || !strings.Contains(a, "items:") || !strings.Contains(a, "# kept comment") {
		t.Fatalf("a.yaml = %q", a)
	}
	if got := read(t, dir, "b.yaml"); !strings.Contains(got, "version: 3") || !strings.Contains(got, "k: w") {
		t.Fatalf("b.yaml = %q", got)
	}

	// Later writes touch one file.
	before, _ := os.Stat(filepath.Join(dir, "a.yaml"))
	if err := b.Update(func(m *map[string]any) error { (*m)["k"] = "z"; return nil }); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(dir, "a.yaml"))
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a.yaml rewritten by a b.yaml change")
	}
}

func TestNewerFileMakesSetReadOnly(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "version: 9\nitems: [x]\n")
	set := newTestSet(dir, 3)
	b := In(set, "b.yaml", func() map[string]any { return map[string]any{} })
	if _, err := b.Load(); !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("load: %v", err)
	}
	if err := b.Save(map[string]any{"k": "v"}); !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.yaml")); !os.IsNotExist(err) {
		t.Fatal("wrote to a read-only set")
	}
}

func TestBrokenFileBlocksWritesToOtherFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "version: [oops\n")
	set := newTestSet(dir, 3)
	b := In(set, "b.yaml", func() map[string]any { return map[string]any{} })
	if err := b.Save(map[string]any{"k": "v"}); err == nil {
		t.Fatal("saved while a.yaml is broken")
	}
	// Fixing the file unblocks it.
	write(t, dir, "a.yaml", "version: 3\nitems: []\n")
	if err := b.Save(map[string]any{"k": "v"}); err != nil {
		t.Fatal(err)
	}
}

func TestObsoleteFileAppearingIsReadAgain(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "version: 2\nnames: [x]\n")
	set := newTestSet(dir, 3)
	a := In(set, "a.yaml", func() v3doc { return v3doc{} })
	if v, _ := a.Load(); len(v.Items) != 1 {
		t.Fatalf("got %+v", v)
	}
	// A git pull brings back a v1 file: it is migrated in.
	write(t, dir, "a.yaml", "version: 1\nnames: [x]\n")
	write(t, dir, "old.yaml", "[y, z]\n")
	if v, err := a.Load(); err != nil || len(v.Items) != 3 {
		t.Fatalf("got %+v, %v", v, err)
	}
}

func TestStepCountMustMatchVersion(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want panic")
		}
	}()
	OpenSet(t.TempDir(), SetOptions{Current: 3, Files: []string{"a.yaml"}})
}

func mode(t *testing.T, dir, name string) os.FileMode {
	t.Helper()
	st, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// FileMode sets the mode of every file the set writes: a new file, an
// existing one with a looser mode, and the files of a migration flush.
func TestFileMode(t *testing.T) {
	type m = map[string]any
	t.Run("default is 0644", func(t *testing.T) {
		dir := t.TempDir()
		f := In(OpenSet(dir, SetOptions{Files: []string{"a.yaml"}}), "a.yaml", func() m { return m{} })
		if err := f.Save(m{"k": "v"}); err != nil {
			t.Fatal(err)
		}
		if got := mode(t, dir, "a.yaml"); got != 0o644 {
			t.Errorf("mode = %o, want 644", got)
		}
	})
	t.Run("new file", func(t *testing.T) {
		dir := t.TempDir()
		f := In(OpenSet(dir, SetOptions{Files: []string{"a.yaml"}, FileMode: 0o600}), "a.yaml", func() m { return m{} })
		if err := f.Save(m{"k": "v"}); err != nil {
			t.Fatal(err)
		}
		if got := mode(t, dir, "a.yaml"); got != 0o600 {
			t.Errorf("mode = %o, want 600", got)
		}
	})
	t.Run("existing 0644 file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "a.yaml", "version: 1\nk: v\n")
		f := In(OpenSet(dir, SetOptions{Files: []string{"a.yaml"}, FileMode: 0o600}), "a.yaml", func() m { return m{} })
		if err := f.Update(func(v *m) error { (*v)["k"] = "w"; return nil }); err != nil {
			t.Fatal(err)
		}
		if got := mode(t, dir, "a.yaml"); got != 0o600 {
			t.Errorf("mode = %o, want 600", got)
		}
	})
	t.Run("migration flush", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "a.yaml", "version: 1\nnames: [x]\n")
		write(t, dir, "b.yaml", "k: v\n")
		set := OpenSet(dir, SetOptions{Current: 3, Files: []string{"a.yaml", "old.yaml", "b.yaml"}, Steps: testSteps(), FileMode: 0o600})
		b := In(set, "b.yaml", func() m { return m{} })
		if err := b.Update(func(v *m) error { (*v)["k"] = "w"; return nil }); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a.yaml", "b.yaml"} {
			if got := mode(t, dir, name); got != 0o600 {
				t.Errorf("%s mode = %o, want 600", name, got)
			}
		}
	})
}

// Rev changes on a write and on an outside change, and only then.
func TestRev(t *testing.T) {
	dir := t.TempDir()
	set := OpenSet(dir, SetOptions{Files: []string{"a.yaml", "b.yaml"}})
	a := In(set, "a.yaml", func() map[string]any { return map[string]any{} })
	r0 := set.Rev()
	if set.Rev() != r0 {
		t.Fatal("Rev moved with no change")
	}
	if err := a.Save(map[string]any{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	r1 := a.Rev()
	if r1 == r0 {
		t.Fatal("Rev did not move on a write")
	}
	if set.Rev() != r1 {
		t.Fatal("Rev moved on reading our own write")
	}
	write(t, dir, "b.yaml", "k: outside\n")
	r2 := set.Rev()
	if r2 == r1 {
		t.Fatal("Rev did not move on an outside change")
	}
	write(t, dir, "b.yaml", "k: [\n")
	if set.Rev() != r2 {
		t.Error("Rev moved on a broken file; the content did not change")
	}
}

// A file that holds only comments or blank space reads like a missing
// one. It must not panic.
func TestCommentOnlyFile(t *testing.T) {
	for _, content := range []string{"#00", "# a note\n\n# another\n", "   \n"} {
		dir := t.TempDir()
		write(t, dir, "a.yaml", content)
		got, err := In(newTestSet(dir, 3), "a.yaml", func() v3doc { return v3doc{Items: []string{"default"}} }).Load()
		if err != nil || len(got.Items) != 1 || got.Items[0] != "default" {
			t.Errorf("%q: %+v, %v", content, got, err)
		}
	}
}

func TestMapDelete(t *testing.T) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	MapSet(m, "a", &yaml.Node{Kind: yaml.ScalarNode, Value: "1"})
	MapSet(m, "b", &yaml.Node{Kind: yaml.ScalarNode, Value: "2"})
	// A duplicate key goes too.
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "a"}, &yaml.Node{Kind: yaml.ScalarNode, Value: "3"})
	MapDelete(m, "a")
	if len(m.Content) != 2 || m.Content[0].Value != "b" || MapGet(m, "a") != nil {
		t.Errorf("after delete: %v", m.Content)
	}
	MapDelete(m, "missing")
	if len(m.Content) != 2 {
		t.Error("deleting a missing key changed the map")
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "a"}}}
	MapDelete(seq, "a")
	MapDelete(nil, "a")
	if len(seq.Content) != 1 {
		t.Error("a sequence changed")
	}
}
