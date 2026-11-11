package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"go.yaml.in/yaml/v3"
)

// ErrNewerVersion means a file was written by a newer version of the app.
// The set is read-only until that app version runs again.
var ErrNewerVersion = errors.New("file is from a newer app version")

// Step migrates a Tree from one schema version to the next: steps[i] of a
// Set takes the files at version i+1 to i+2. A step works on the YAML
// nodes only, never on app types, so it keeps compiling as the types move
// on. Files at another version are left alone; after the step, every file
// that was at its version, and every file it created, is at the next one.
type Step func(t *Tree) error

// Set is a directory of YAML files that share one schema version. Each
// file holds its version in a top-level "version" key (a file without one,
// such as a bare list, is version 1). Reading migrates the files in memory
// to the current version and never writes. The first write after that
// writes every file of the set at the current version and deletes the
// files the migration made obsolete; later writes touch one file.
//
// Files are re-read when any of them changes, appears or goes away, so an
// edit made outside the process (a git pull on HA) is picked up on the
// next Load. While a file does not parse, or is newer than the set knows,
// reads get the last good content (or defaults) with the error, and every
// write is refused, so a broken hand edit is never overwritten.
type Set struct {
	dir     string
	current int
	names   []string // every file name the set has ever used, obsolete ones too
	steps   []Step
	mode    os.FileMode
	log     *slog.Logger

	mu     sync.Mutex
	stats  map[string]fs.FileInfo // files present at the last read
	read   bool                   // stats are valid
	tree   *Tree                  // migrated content, last good
	onDisk map[string]int         // version of each file on disk, at the last read
	err    error                  // why the files on disk are not usable as they are
	rev    uint64                 // bumped when tree changes

	// writeFile writes one file. It is writeFileAtomic, swapped in tests
	// to make a write fail.
	writeFile func(path string, data []byte, mode os.FileMode) error
}

// SetOptions configure a Set.
type SetOptions struct {
	// Current is the schema version this build writes. 0 means 1.
	Current int
	// Files are the file names of every version, current and obsolete.
	Files []string
	// Steps has Current-1 entries; see Step.
	Steps []Step
	// FileMode is the mode of the files the set writes. 0 means 0644,
	// for files shared with HA and its git repo. Use 0600 for files only
	// this app may read.
	FileMode os.FileMode
	Logger   *slog.Logger
}

// OpenSet returns a Set for dir. It reads nothing until the first Load.
func OpenSet(dir string, o SetOptions) *Set {
	if o.Current == 0 {
		o.Current = 1
	}
	if len(o.Steps) != o.Current-1 {
		panic(fmt.Sprintf("store: %d migration steps for version %d", len(o.Steps), o.Current))
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	if o.FileMode == 0 {
		o.FileMode = 0o644
	}
	return &Set{dir: dir, current: o.Current, names: slices.Clone(o.Files), steps: o.Steps, mode: o.FileMode, log: log, writeFile: writeFileAtomic}
}

// Dir returns the directory of the set.
func (s *Set) Dir() string { return s.dir }

// Rev re-reads the set if a file changed, like Load, and returns a number
// that changes whenever the content does: on a reload from disk and on
// every write. Callers use it to keep a derived index (such as a map by
// slug) without decoding the files on every call.
func (s *Set) Rev() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.rev
}

// load re-reads and migrates the files if any changed. It requires s.mu.
func (s *Set) load() {
	stats := map[string]fs.FileInfo{}
	var statErr error
	for _, name := range s.names {
		st, err := os.Stat(filepath.Join(s.dir, name))
		switch {
		case err == nil:
			stats[name] = st
		case !errors.Is(err, fs.ErrNotExist):
			statErr = errors.Join(statErr, fmt.Errorf("stat %s: %w", name, err))
		}
	}
	if statErr != nil {
		s.err = statErr
		return
	}
	if s.read && sameStats(s.stats, stats) {
		return
	}
	s.stats, s.read = stats, true

	raw := &Tree{docs: map[string]*yaml.Node{}, versions: map[string]int{}}
	var errs error
	for name := range stats {
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("read %s: %w", name, err))
			continue
		}
		if len(bytes.TrimSpace(data)) == 0 {
			continue // an empty file is a missing one
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			errs = errors.Join(errs, fmt.Errorf("parse %s: %w", name, err))
			continue
		}
		v := versionOf(doc.Content[0])
		if v > s.current {
			errs = errors.Join(errs, fmt.Errorf("%s has version %d, this app knows %d: %w", name, v, s.current, ErrNewerVersion))
			continue
		}
		raw.docs[name], raw.versions[name] = doc.Content[0], v
	}
	if errs != nil {
		s.err = errs
		return
	}
	onDisk := maps(raw.versions)
	if err := s.migrate(raw); err != nil {
		s.err = err
		return
	}
	s.tree, s.onDisk, s.err = raw, onDisk, nil
	s.rev++
}

// migrate runs the steps from the oldest file version up. It requires s.mu.
func (s *Set) migrate(t *Tree) error {
	from := s.current
	for _, v := range t.versions {
		from = min(from, v)
	}
	for v := from; v < s.current; v++ {
		t.at = v
		if err := s.steps[v-1](t); err != nil {
			return fmt.Errorf("migrate %s from version %d: %w", s.dir, v, err)
		}
		for name, ver := range t.versions {
			if ver == v {
				t.versions[name] = v + 1
				setVersion(t.docs[name], v+1)
			}
		}
		s.log.Info("migrated config in memory", "dir", s.dir, "from", v, "to", v+1)
	}
	return nil
}

// needsFlush reports whether disk lags the migrated tree: a file at an
// older version, or a file the migration made obsolete. It requires s.mu.
func (s *Set) needsFlush() bool {
	for name, v := range s.onDisk {
		if _, kept := s.tree.docs[name]; !kept || v < s.current {
			return true
		}
	}
	return false
}

// get returns the migrated node of a file, nil when it has none. It
// requires s.mu.
func (s *Set) get(name string) (*yaml.Node, error) {
	s.load()
	if s.tree == nil {
		return nil, s.err
	}
	return s.tree.docs[name], s.err
}

// put stores node as the content of a file and writes it. When disk lags
// the migration, it writes every file of the set first, then deletes the
// obsolete ones. The named file is written last, so a failed flush never
// leaves the new content on disk.
//
// On any error, memory goes back to what disk holds: the old content is
// restored if the named file was not written, and the next load re-reads
// every file. So a failed save is never visible to a later Load. It
// requires s.mu.
func (s *Set) put(name string, node *yaml.Node) (err error) {
	s.load()
	if s.err != nil {
		return fmt.Errorf("not saved: %w", s.err)
	}
	if s.tree == nil {
		s.tree = &Tree{docs: map[string]*yaml.Node{}, versions: map[string]int{}}
	}
	prevNode, hadPrev := s.tree.docs[name]
	prevVer := s.tree.versions[name]
	wroteName := false
	defer func() {
		if err == nil {
			return
		}
		if !wroteName {
			if hadPrev {
				s.tree.docs[name], s.tree.versions[name] = prevNode, prevVer
			} else {
				delete(s.tree.docs, name)
				delete(s.tree.versions, name)
			}
		}
		s.read = false // disk is the truth; read it again on the next load
		s.rev++
	}()

	setVersion(node, s.current)
	s.tree.docs[name], s.tree.versions[name] = node, s.current
	s.rev++
	write := []string{name}
	if s.needsFlush() {
		write = slices.DeleteFunc(s.tree.Names(), func(n string) bool { return n == name })
		write = append(write, name)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	for _, n := range write {
		data, err := encodeNode(s.tree.docs[n])
		if err != nil {
			return fmt.Errorf("encode %s: %w", n, err)
		}
		if err := s.writeFile(filepath.Join(s.dir, n), data, s.mode); err != nil {
			return err
		}
		s.onDisk[n] = s.current
		wroteName = n == name
	}
	if len(write) > 1 {
		for n := range s.onDisk {
			if _, kept := s.tree.docs[n]; kept {
				continue
			}
			if err := os.Remove(filepath.Join(s.dir, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove obsolete %s: %w", n, err)
			}
			delete(s.onDisk, n)
		}
		s.log.Info("wrote migrated config", "dir", s.dir, "version", s.current, "files", write)
	}
	s.read = false // pick up our own stats on the next load
	return nil
}

// Tree is the content of a Set's files during a migration: one YAML node
// (the document's root) and one version per file.
type Tree struct {
	docs     map[string]*yaml.Node
	versions map[string]int
	at       int // version the running step migrates from
}

// Names lists the files in the tree, sorted.
func (t *Tree) Names() []string {
	out := make([]string, 0, len(t.docs))
	for n := range t.docs {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Get returns a file's root node and version, if the tree has the file.
func (t *Tree) Get(name string) (*yaml.Node, int, bool) {
	n, ok := t.docs[name]
	return n, t.versions[name], ok
}

// Put adds or replaces a file. In a step it lands at the step's next version.
func (t *Tree) Put(name string, node *yaml.Node) {
	t.docs[name], t.versions[name] = node, t.at
}

// Delete drops a file. The first write after the migration deletes it on disk.
func (t *Tree) Delete(name string) {
	delete(t.docs, name)
	delete(t.versions, name)
}

// versionOf reads the top-level "version" of a mapping; anything else is 1.
func versionOf(n *yaml.Node) int {
	if v := MapGet(n, "version"); v != nil {
		var ver int
		if v.Decode(&ver) == nil && ver > 0 {
			return ver
		}
	}
	return 1
}

// setVersion sets the top-level "version" of a mapping. A bare list can't
// hold one; it stays version 1 until a step wraps it.
func setVersion(n *yaml.Node, v int) {
	if n.Kind == yaml.MappingNode {
		MapSet(n, "version", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(v)})
	}
}

// MapGet returns the value node of key in a mapping node, or nil.
func MapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// MapSet sets key in a mapping node. A new key goes first when it is
// "version", else last.
func MapSet(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	if key == "version" {
		m.Content = append([]*yaml.Node{k, v}, m.Content...)
		return
	}
	m.Content = append(m.Content, k, v)
}

func encodeNode(n *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sameStats(a, b map[string]fs.FileInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for name, x := range a {
		y, ok := b[name]
		if !ok || !os.SameFile(x, y) || !x.ModTime().Equal(y.ModTime()) || x.Size() != y.Size() {
			return false
		}
	}
	return true
}

func maps(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
