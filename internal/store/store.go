// Package store keeps YAML files with a schema version, migrated in memory on
// read, with atomic writes (temp file, fsync, rename) under a mutex and
// reload when a file changes.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"go.yaml.in/yaml/v3"
)

// File is one file of a Set, holding one value of type T.
//
// Load decodes the migrated content on every call, so each caller gets its
// own value and can change it freely. See Set for reloads, migrations and
// broken files.
type File[T any] struct {
	set  *Set
	name string
	def  func() T // value when the file does not exist
}

// Open returns a File for path in a Set of its own, at version 1 with no
// migrations. def builds the value used while the file is missing.
func Open[T any](path string, def func() T) *File[T] {
	set := OpenSet(filepath.Dir(path), SetOptions{Files: []string{filepath.Base(path)}})
	return In(set, filepath.Base(path), def)
}

// In returns the File name of set. name must be one of the set's files.
func In[T any](set *Set, name string, def func() T) *File[T] {
	if !slices.Contains(set.names, name) {
		panic("store: " + name + " is not a file of the set")
	}
	return &File[T]{set: set, name: name, def: def}
}

// Path returns the file path.
func (f *File[T]) Path() string { return filepath.Join(f.set.dir, f.name) }

// Rev returns the revision of the file's set. See Set.Rev.
func (f *File[T]) Rev() uint64 { return f.set.Rev() }

// Load returns the current value, re-reading the set if a file changed.
//
// A missing file gives the default value and is not created: reads never
// write, so the HA config repo gets no files nobody chose to save. The file
// appears on the first Save or Update. If a file of the set does not
// decode, Load returns the last good value (or the default) together with
// the error, so callers can keep working and report the problem.
func (f *File[T]) Load() (T, error) {
	f.set.mu.Lock()
	defer f.set.mu.Unlock()
	return f.load()
}

// Save writes v atomically. It refuses while a file of the set is broken.
func (f *File[T]) Save(v T) error {
	f.set.mu.Lock()
	defer f.set.mu.Unlock()
	return f.save(v)
}

// Update loads, applies fn and saves under one lock. It refuses to run when
// a file of the set does not decode, so a broken hand edit is never
// overwritten. If fn returns an error, nothing is written.
func (f *File[T]) Update(fn func(*T) error) error {
	f.set.mu.Lock()
	defer f.set.mu.Unlock()
	v, err := f.load()
	if err != nil {
		return err
	}
	if err := fn(&v); err != nil {
		return err
	}
	return f.save(v)
}

// load requires f.set.mu.
func (f *File[T]) load() (T, error) {
	node, err := f.set.get(f.name)
	if node == nil {
		return f.def(), err
	}
	var v T
	if derr := node.Decode(&v); derr != nil {
		return f.def(), errors.Join(err, fmt.Errorf("decode %s: %w", f.name, derr))
	}
	return v, err
}

// save requires f.set.mu.
func (f *File[T]) save(v T) error {
	var node yaml.Node
	if err := node.Encode(v); err != nil {
		return fmt.Errorf("encode %s: %w", f.name, err)
	}
	return f.set.put(f.name, &node)
}

// WriteFileAtomic writes data to a temp file in the same dir, fsyncs and
// renames. The file gets mode 0644: these files are shared with HA and its
// git repo.
func WriteFileAtomic(path string, data []byte) error {
	return writeFileAtomic(path, data, 0o644)
}

// writeFileAtomic is WriteFileAtomic with a file mode. The temp file is
// created 0600 by os.CreateTemp, so the data is never readable by others
// before the chmod.
func writeFileAtomic(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir makes the rename durable. Some filesystems do not support fsync on
// a directory; that is not worth failing a save for.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
