// Package links stores guest links in links.yaml of the data set. Every
// change runs under one lock and is saved before the method returns.
// Lookups by slug go through a map of slug hashes, so a miss never decodes
// the file.
package links

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
	"github.com/igrybkov/ha-guest-access/internal/store"
)

var (
	// ErrNotFound means no link (or password) has that id.
	ErrNotFound = errors.New("not found")
	// ErrInactive means the link is revoked, not started or expired.
	ErrInactive = errors.New("link is not active")
	// ErrUsed means a one-time link already used this action on this
	// entity.
	ErrUsed = errors.New("action already used")
	// ErrSlugTaken means another link has the slug.
	ErrSlugTaken = errors.New("slug is used by another link")
)

// Options configure a Store. Zero fields get the defaults.
type Options struct {
	// NewSlug returns a random slug. Default access.NewSlug.
	NewSlug func() string
	// ValidCustomSlug checks a slug the admin typed. Default
	// access.ValidCustomSlug. Uniqueness is checked by the Store.
	ValidCustomSlug func(string) error
	// Now is the clock for CreatedAt fields. Default time.Now.
	Now    func() time.Time
	Logger *slog.Logger
}

// Store serves links.yaml. Safe for concurrent use.
//
// Lock order: Store.mu, then the set's lock. Nothing that holds the set's
// lock may call into Store.
type Store struct {
	file *store.File[model.Links]
	opts Options
	log  *slog.Logger

	mu     sync.Mutex
	bySlug map[string]string // access.HashSlug(slug) -> link id
	rev    uint64            // set revision bySlug was built at
	built  bool
}

// New returns the link store of a data set (see migrate.OpenData).
func New(set *store.Set, o Options) *Store {
	if o.NewSlug == nil {
		o.NewSlug = access.NewSlug
	}
	if o.ValidCustomSlug == nil {
		o.ValidCustomSlug = access.ValidCustomSlug
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Store{
		file: store.In(set, migrate.LinksFile, func() model.Links {
			return model.Links{Version: migrate.DataCurrent, Links: []model.Link{}}
		}),
		opts: o,
		log:  log.With("component", "links"),
	}
}

// List returns every link, or the last good ones with the error.
func (s *Store) List() (model.Links, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ls, err := s.file.Load()
	if ls.Links == nil {
		ls.Links = []model.Link{}
	}
	return ls, err
}

// Get returns the link with id.
func (s *Store) Get(id string) (model.Link, error) {
	ls, err := s.List()
	if l, ok := ls.Find(id); ok {
		return l, nil
	}
	if err != nil {
		return model.Link{}, err
	}
	return model.Link{}, ErrNotFound
}

// FindBySlug returns the link whose slug is slug. It does not check the
// link's window; see model.Link.Active. A miss costs a hash and a map
// lookup, never a file decode.
func (s *Store) FindBySlug(slug string) (model.Link, bool) {
	h := access.HashSlug(slug)
	s.mu.Lock()
	defer s.mu.Unlock()
	if rev := s.file.Rev(); !s.built || rev != s.rev {
		ls, err := s.file.Load()
		if err != nil {
			s.log.Warn("load links", "err", err)
		}
		s.index(ls, rev)
	}
	id, ok := s.bySlug[h]
	if !ok {
		return model.Link{}, false
	}
	ls, err := s.file.Load()
	if err != nil {
		s.log.Warn("load links", "err", err)
	}
	l, ok := ls.Find(id)
	if !ok || l.Slug != slug {
		// The file changed after Rev. The next call rebuilds the index.
		s.built = false
		return model.Link{}, false
	}
	return l, true
}

// index rebuilds bySlug. It requires s.mu.
func (s *Store) index(ls model.Links, rev uint64) {
	s.bySlug = make(map[string]string, len(ls.Links))
	for _, l := range ls.Links {
		s.bySlug[access.HashSlug(l.Slug)] = l.ID
	}
	s.rev, s.built = rev, true
}

// update runs fn on the links under both locks and saves. When validate is
// set, every link must pass model.Links.Validate before the save; that
// also checks ids and slugs are unique. Methods that change one link
// validate it first, so its problems have paths relative to the link. It
// requires s.mu.
func (s *Store) update(validate bool, fn func(*model.Links) error) error {
	err := s.file.Update(func(ls *model.Links) error {
		if err := fn(ls); err != nil {
			return err
		}
		if validate {
			return ls.Validate()
		}
		return nil
	})
	s.built = false
	return err
}

// at returns the link with id in ls.
func at(ls *model.Links, id string) (*model.Link, error) {
	for i := range ls.Links {
		if ls.Links[i].ID == id {
			return &ls.Links[i], nil
		}
	}
	return nil, ErrNotFound
}

// setEditable copies the fields the admin may edit from src to dst. It is
// an allowlist on purpose: the slug, passwords, uses and any field added
// later stay as the server set them.
func setEditable(dst *model.Link, src model.Link) {
	dst.Name = src.Name
	dst.Note = src.Note
	dst.ValidFrom = src.ValidFrom
	dst.ValidUntil = src.ValidUntil
	dst.TemplateIDs = slices.Clone(src.TemplateIDs)
	dst.Extra = slices.Clone(src.Extra)
	dst.OneTime = src.OneTime
}

// Create adds a link with the editable fields of draft (name, note,
// window, templates, extra rules, one time). It sets the id, the slug and
// CreatedAt. An empty customSlug gets a random slug; otherwise customSlug
// must pass ValidCustomSlug and be unused.
func (s *Store) Create(draft model.Link, customSlug string) (model.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out model.Link
	err := s.update(true, func(ls *model.Links) error {
		l := model.Link{ID: newID(func(id string) bool { _, ok := ls.Find(id); return ok }), CreatedAt: s.now()}
		setEditable(&l, draft)
		slug, err := s.pickSlug(ls, customSlug)
		if err != nil {
			return err
		}
		l.Slug = slug
		if err := l.Validate(); err != nil {
			return err
		}
		ls.Links = append(ls.Links, l)
		out = l
		return nil
	})
	if err != nil {
		return model.Link{}, err
	}
	return out, nil
}

// Update replaces the editable fields of link id with those of edit. Other
// fields of edit are ignored.
func (s *Store) Update(id string, edit model.Link) (model.Link, error) {
	return s.change(id, func(l *model.Link) error {
		setEditable(l, edit)
		return nil
	})
}

// Revoke ends link id at once. Its slug stops working.
func (s *Store) Revoke(id string) error {
	_, err := s.change(id, func(l *model.Link) error {
		l.Revoked = true
		return nil
	})
	return err
}

// RotateSlug gives link id a new slug: customSlug, or a random one when it
// is empty. The old slug stops working.
func (s *Store) RotateSlug(id, customSlug string) (model.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out model.Link
	err := s.update(true, func(ls *model.Links) error {
		l, err := at(ls, id)
		if err != nil {
			return err
		}
		slug, err := s.pickSlug(ls, customSlug)
		if err != nil {
			return err
		}
		l.Slug = slug
		out = *l
		return nil
	})
	if err != nil {
		return model.Link{}, err
	}
	return out, nil
}

// Delete removes link id.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.update(true, func(ls *model.Links) error {
		if _, err := at(ls, id); err != nil {
			return err
		}
		ls.Links = slices.DeleteFunc(ls.Links, func(l model.Link) bool { return l.ID == id })
		return nil
	})
}

// AddPassword adds a password to link id. hash is the argon2id hash of
// the password (see access); the store never sees the password itself.
func (s *Store) AddPassword(linkID, label, hash string) (model.LinkPassword, error) {
	var out model.LinkPassword
	_, err := s.change(linkID, func(l *model.Link) error {
		out = model.LinkPassword{
			ID:        newID(func(id string) bool { _, ok := l.Password(id); return ok }),
			Label:     label,
			Hash:      hash,
			CreatedAt: s.now(),
		}
		l.Passwords = append(l.Passwords, out)
		return nil
	})
	if err != nil {
		return model.LinkPassword{}, err
	}
	return out, nil
}

// RemovePassword removes password pwID from link id. Its sessions end,
// since a session names its password.
func (s *Store) RemovePassword(linkID, pwID string) error {
	_, err := s.change(linkID, func(l *model.Link) error {
		if _, ok := l.Password(pwID); !ok {
			return fmt.Errorf("password: %w", ErrNotFound)
		}
		l.Passwords = slices.DeleteFunc(l.Passwords, func(p model.LinkPassword) bool { return p.ID == pwID })
		return nil
	})
	return err
}

// change applies fn to link id, validates and saves.
func (s *Store) change(id string, fn func(*model.Link) error) (model.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out model.Link
	err := s.update(true, func(ls *model.Links) error {
		l, err := at(ls, id)
		if err != nil {
			return err
		}
		if err := fn(l); err != nil {
			return err
		}
		if err := l.Validate(); err != nil {
			return err
		}
		out = *l
		return nil
	})
	if err != nil {
		return model.Link{}, err
	}
	return out, nil
}

// Consume records one use of action on entityID by link linkID, and saves
// it before it returns. Call it after every other check passed and before
// the HA call.
//
//   - The link must be active at now, checked again under the lock, so a
//     revoke that lands first always wins.
//   - On a one-time link, a pair used before gives ErrUsed. Of N parallel
//     calls for the same pair, exactly one succeeds.
//   - Every link counts the use in UseCount and LastUsedAt.
//
// If the HA call fails, call refund. It takes the use back: the one-time
// pair can be used again, and the counters are restored. refund is safe to
// call more than once and only removes this call's use.
func (s *Store) Consume(linkID, entityID, action string, now time.Time) (refund func(), err error) {
	now = now.Round(0) // drop the monotonic reading; it does not survive a save
	s.mu.Lock()
	defer s.mu.Unlock()
	var oneTime bool
	var prevLast *time.Time
	err = s.update(false, func(ls *model.Links) error {
		l, err := at(ls, linkID)
		if err != nil {
			return err
		}
		if !l.Active(now) {
			return ErrInactive
		}
		if oneTime = l.OneTime; oneTime {
			if _, used := l.UsedAt(entityID, action); used {
				return ErrUsed
			}
			l.Used = append(l.Used, model.Use{EntityID: entityID, Action: action, At: now})
		}
		prevLast = l.LastUsedAt
		l.UseCount++
		l.LastUsedAt = &now
		return nil
	})
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { s.refund(linkID, entityID, action, now, oneTime, prevLast) })
	}, nil
}

func (s *Store) refund(linkID, entityID, action string, usedAt time.Time, oneTime bool, prevLast *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.update(false, func(ls *model.Links) error {
		l, err := at(ls, linkID)
		if err != nil {
			return err
		}
		if oneTime {
			l.Used = slices.DeleteFunc(l.Used, func(u model.Use) bool {
				return u.EntityID == entityID && u.Action == action && u.At.Equal(usedAt)
			})
		}
		if l.UseCount > 0 {
			l.UseCount--
		}
		if l.LastUsedAt != nil && l.LastUsedAt.Equal(usedAt) {
			l.LastUsedAt = prevLast
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		s.log.Error("refund a use", "link", linkID, "entity_id", entityID, "action", action, "err", err)
	}
}

// pickSlug returns customSlug, checked, or a new random slug. It requires
// both locks (it runs inside update).
func (s *Store) pickSlug(ls *model.Links, customSlug string) (string, error) {
	taken := func(slug string) bool {
		return slices.ContainsFunc(ls.Links, func(l model.Link) bool { return l.Slug == slug })
	}
	if customSlug != "" {
		if err := s.opts.ValidCustomSlug(customSlug); err != nil {
			return "", err
		}
		if taken(customSlug) {
			return "", ErrSlugTaken
		}
		return customSlug, nil
	}
	for range 5 {
		if slug := s.opts.NewSlug(); !taken(slug) {
			return slug, nil
		}
	}
	return "", errors.New("could not pick a free random slug")
}

func (s *Store) now() time.Time { return s.opts.Now().UTC().Round(0) }

// newID returns a random 16-character hex id for which taken is false.
func newID(taken func(string) bool) string {
	for {
		b := make([]byte, 8)
		_, _ = rand.Read(b) // crypto/rand.Read never fails
		if id := hex.EncodeToString(b); !taken(id) {
			return id
		}
	}
}
