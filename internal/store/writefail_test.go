package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

var errDiskFull = errors.New("disk full")

// failWrites makes every write of a file named in names fail until the
// returned func is called.
func failWrites(set *Set, names ...string) (restore func()) {
	set.mu.Lock()
	defer set.mu.Unlock()
	set.writeFile = func(path string, data []byte, mode os.FileMode) error {
		if slices.Contains(names, filepath.Base(path)) {
			return errDiskFull
		}
		return writeFileAtomic(path, data, mode)
	}
	return func() {
		set.mu.Lock()
		defer set.mu.Unlock()
		set.writeFile = writeFileAtomic
	}
}

// A failed write leaves memory as disk has it. The change never shows in
// Load or Rev, and the next good write does not carry it.
func TestFailedWriteRollsBack(t *testing.T) {
	dir := t.TempDir()
	set := OpenSet(dir, SetOptions{Files: []string{"a.yaml"}})
	f := In(set, "a.yaml", defDoc)
	if err := f.Save(doc{Names: []string{"good"}}); err != nil {
		t.Fatal(err)
	}
	rev := set.Rev()

	restore := failWrites(set, "a.yaml")
	err := f.Update(func(d *doc) error { d.Names = append(d.Names, "lost"); return nil })
	if !errors.Is(err, errDiskFull) {
		t.Fatalf("Update = %v, want the write error", err)
	}
	if got, err := f.Load(); err != nil || !slices.Equal(got.Names, []string{"good"}) {
		t.Fatalf("after a failed write Load = %v, %v", got.Names, err)
	}
	if set.Rev() == rev {
		t.Error("Rev did not move; an index built during the failed write would stay")
	}
	restore()

	if err := f.Update(func(d *doc) error { d.Names = append(d.Names, "next"); return nil }); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "a.yaml"); strings.Contains(got, "lost") || !strings.Contains(got, "next") {
		t.Fatalf("a.yaml = %q", got)
	}
}

// A write that fails for a file that did not exist yet leaves no trace.
func TestFailedFirstWriteRollsBack(t *testing.T) {
	dir := t.TempDir()
	set := OpenSet(dir, SetOptions{Files: []string{"a.yaml"}})
	f := In(set, "a.yaml", defDoc)
	failWrites(set, "a.yaml")
	if err := f.Save(doc{Names: []string{"lost"}}); err == nil {
		t.Fatal("Save succeeded")
	}
	if got, err := f.Load(); err != nil || len(got.Names) != 0 {
		t.Fatalf("Load = %v, %v; want the default", got.Names, err)
	}
}

// A migration flush that fails partway never writes the changed file, so
// disk and memory agree, and the obsolete file is kept for the next try.
func TestFailedFlushRollsBack(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "version: 1\nnames: [x]\n")
	write(t, dir, "old.yaml", "[y]\n")
	set := newTestSet(dir, 3)
	b := In(set, "b.yaml", func() map[string]any { return map[string]any{} })

	restore := failWrites(set, "a.yaml")
	err := b.Update(func(m *map[string]any) error { (*m)["k"] = "lost"; return nil })
	if !errors.Is(err, errDiskFull) {
		t.Fatalf("Update = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.yaml")); !os.IsNotExist(err) {
		t.Fatalf("b.yaml written before the flush finished: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.yaml")); err != nil {
		t.Fatalf("old.yaml removed by a failed flush: %v", err)
	}
	if got, err := b.Load(); err != nil || got["k"] != nil {
		t.Fatalf("b after a failed flush = %v, %v", got, err)
	}
	restore()

	if err := b.Update(func(m *map[string]any) error { (*m)["k"] = "kept"; return nil }); err != nil {
		t.Fatal(err)
	}
	a := In(set, "a.yaml", func() v3doc { return v3doc{} })
	got, err := a.Load()
	if err != nil || !slices.Equal(got.Items, []string{"x", "y"}) {
		t.Fatalf("a after the retried flush = %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.yaml")); !os.IsNotExist(err) {
		t.Fatalf("obsolete file kept: %v", err)
	}
}

// The real thing: a directory that refuses writes.
func TestReadOnlyDirRollsBack(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not stop this user")
	}
	dir := t.TempDir()
	f := In(OpenSet(dir, SetOptions{Files: []string{"a.yaml"}}), "a.yaml", defDoc)
	if err := f.Save(doc{Names: []string{"good"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := f.Update(func(d *doc) error { d.Names = []string{"lost"}; return nil }); err == nil {
		t.Fatal("write to a read-only dir succeeded")
	}
	if got, err := f.Load(); err != nil || !slices.Equal(got.Names, []string{"good"}) {
		t.Fatalf("Load = %v, %v", got.Names, err)
	}
}
