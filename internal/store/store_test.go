package store

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type doc struct {
	Version int      `yaml:"version"`
	Names   []string `yaml:"names"`
}

func defDoc() doc { return doc{Version: 1, Names: []string{}} }

func TestLoadMissingFileDoesNotWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	path := filepath.Join(dir, "doc.yaml")
	f := Open(path, defDoc)

	for range 2 {
		v, err := f.Load()
		if err != nil {
			t.Fatal(err)
		}
		if v.Version != 1 || v.Names == nil {
			t.Fatalf("got %+v, want default", v)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Load wrote to disk (stat err %v)", err)
	}

	// A file that shows up later (a seed copied in) is read.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\nnames: [seed]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := f.Load(); err != nil || v.Version != 1 || len(v.Names) != 1 {
		t.Fatalf("after the seed: %+v, %v", v, err)
	}
	// And a file removed again gives the default, not the stale content.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if v, err := f.Load(); err != nil || v.Version != 1 {
		t.Fatalf("after removal: %+v, %v", v, err)
	}
}

func TestUpdateCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "doc.yaml")
	f := Open(path, defDoc)
	if err := f.Update(func(d *doc) error { d.Names = append(d.Names, "a"); return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if !strings.Contains(string(raw), "version: 1") || !strings.Contains(string(raw), "- a") {
		t.Fatalf("unexpected content %q", raw)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", st.Mode().Perm())
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.yaml")
	f := Open(path, defDoc)
	// Save writes the set's version, whatever the value says.
	if err := f.Save(doc{Version: 7, Names: []string{"a", "Володимир"}}); err != nil {
		t.Fatal(err)
	}
	v, err := Open(path, defDoc).Load()
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || len(v.Names) != 2 || v.Names[1] != "Володимир" {
		t.Fatalf("got %+v", v)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want 1", len(entries))
	}
}

func TestLoadReturnsIndependentValues(t *testing.T) {
	f := Open(filepath.Join(t.TempDir(), "doc.yaml"), defDoc)
	if err := f.Save(doc{Version: 1, Names: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	v1, _ := f.Load()
	v1.Names[0] = "changed"
	v2, _ := f.Load()
	if v2.Names[0] != "a" {
		t.Fatalf("Load shares memory between callers: %v", v2.Names)
	}
}

func TestLoadPicksUpExternalChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.yaml")
	f := Open(path, defDoc)
	if err := f.Save(doc{Version: 1, Names: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Load(); err != nil {
		t.Fatal(err)
	}

	// Same size, written in place, mtime moved: like an editor save.
	if err := os.WriteFile(path, []byte("version: 1\nnames: [b]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	v, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Names) != 1 || v.Names[0] != "b" {
		t.Fatalf("got %+v, want reloaded content", v)
	}
}

func TestLoadPicksUpReplacedFile(t *testing.T) {
	// git replaces files with a new inode; mtime may be equal at 1s precision.
	path := filepath.Join(t.TempDir(), "doc.yaml")
	f := Open(path, defDoc)
	if err := f.Save(doc{Version: 1, Names: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if err := WriteFileAtomic(path, []byte("version: 1\nnames: [c]\n")); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, st.ModTime(), st.ModTime())
	v, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if v.Names[0] != "c" {
		t.Fatalf("got %+v, want reloaded content", v)
	}
}

func TestBrokenFileKeepsLastGoodAndBlocksUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.yaml")
	f := Open(path, defDoc)
	if err := f.Save(doc{Version: 1, Names: []string{"good"}}); err != nil {
		t.Fatal(err)
	}
	broken := []byte("version: [oops\n")
	if err := os.WriteFile(path, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(path, future, future)

	v, err := f.Load()
	if err == nil {
		t.Fatal("want parse error")
	}
	if len(v.Names) != 1 || v.Names[0] != "good" {
		t.Fatalf("got %+v, want last good value", v)
	}
	// Error is sticky until the file changes.
	if _, err := f.Load(); err == nil {
		t.Fatal("want parse error on second load")
	}

	err = f.Update(func(d *doc) error { d.Names = nil; return nil })
	if err == nil {
		t.Fatal("Update must refuse to overwrite a broken file")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != string(broken) {
		t.Fatal("broken file was overwritten")
	}
}

func TestUpdateAppliesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.yaml")
	f := Open(path, defDoc)
	err := f.Update(func(d *doc) error {
		d.Names = append(d.Names, "x")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := Open(path, defDoc).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Names) != 1 || v.Names[0] != "x" {
		t.Fatalf("got %+v", v)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	f := Open(filepath.Join(t.TempDir(), "doc.yaml"), defDoc)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.Update(func(d *doc) error {
				d.Names = append(d.Names, "n")
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	v, _ := f.Load()
	if len(v.Names) != 20 {
		t.Fatalf("got %d names, want 20 (lost update)", len(v.Names))
	}
}

func TestWriteFileAtomicFailureLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// Renaming a file over a non-empty directory fails.
	if err := os.WriteFile(filepath.Join(target, "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(target, []byte("data")); err == nil {
		t.Fatal("want error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %d entries", len(entries))
	}
}
