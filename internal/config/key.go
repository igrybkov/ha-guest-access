package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/igrybkov/ha-guest-access/internal/migrate"
)

// KeySize is the length of the HMAC key in bytes.
const KeySize = 32

// LoadOrCreateKey returns the HMAC key in <dataDir>/hmac.key. If there is
// none, it makes KeySize random bytes and saves them with mode 0600.
//
// The new key is written to a temp file, synced, then hard-linked to its
// name. A link never replaces a file, so two processes that start at once
// both end up with the key of whichever linked first. A key file of the
// wrong length is an error. It is never overwritten: that would end every
// signed cookie, and it may be a sign of a bad restore.
func LoadOrCreateKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, migrate.KeyFile)
	key, err := readKey(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}

	key = make([]byte, KeySize)
	_, _ = rand.Read(key) // crypto/rand.Read never fails
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dataDir, "."+migrate.KeyFile+".tmp-*") // mode 0600
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(key); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	switch err := os.Link(tmp.Name(), path); {
	case errors.Is(err, fs.ErrExist):
		return readKey(path) // another process won the race
	case err != nil:
		return nil, fmt.Errorf("save %s: %w", path, err)
	}
	syncDir(dataDir)
	return key, nil
}

func readKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%s has %d bytes, want %d; fix or remove it (removing it ends every signed cookie)", path, len(key), KeySize)
	}
	return key, nil
}

// syncDir makes the link durable. Some filesystems do not support fsync on
// a directory; that is not worth failing for.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
