package config

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/migrate"
)

func TestLoadOrCreateKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	key, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != KeySize || bytes.Equal(key, make([]byte, KeySize)) {
		t.Fatalf("key = %x", key)
	}
	st, err := os.Stat(filepath.Join(dir, migrate.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode().Perm())
	}
	again, err := LoadOrCreateKey(dir)
	if err != nil || !bytes.Equal(again, key) {
		t.Fatalf("second load = %x, %v; want the same key", again, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left: %v", entries)
	}
	other, err := LoadOrCreateKey(t.TempDir())
	if err != nil || bytes.Equal(other, key) {
		t.Errorf("a new dir got the same key or failed: %v", err)
	}
}

// A key of the wrong length is an error, and the file stays as it was.
func TestLoadOrCreateKeyWrongLength(t *testing.T) {
	for name, content := range map[string][]byte{
		"empty": {},
		"short": bytes.Repeat([]byte{1}, KeySize-1),
		"long":  bytes.Repeat([]byte{1}, KeySize+1),
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, migrate.KeyFile)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if key, err := LoadOrCreateKey(dir); err == nil {
			t.Errorf("%s: no error, key %x", name, key)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, content) {
			t.Errorf("%s: file changed to %x (%v)", name, got, err)
		}
	}
}

// Parallel first starts all get the same key.
func TestLoadOrCreateKeyConcurrent(t *testing.T) {
	dir := t.TempDir()
	const n = 16
	keys := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { keys[i], errs[i] = LoadOrCreateKey(dir) })
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || !bytes.Equal(keys[i], keys[0]) {
			t.Fatalf("call %d: %x, %v; want %x", i, keys[i], errs[i], keys[0])
		}
	}
}
