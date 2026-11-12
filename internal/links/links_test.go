package links

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

var (
	quietLog = slog.New(slog.DiscardHandler)
	t0       = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
)

const hash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2g"

var errBadSlug = errors.New("bad slug")

// fakeCustom accepts lower-case slugs of 12 or more characters, like the
// real access.ValidCustomSlug will.
func fakeCustom(s string) error {
	if len(s) < 12 || strings.ToLower(s) != s {
		return errBadSlug
	}
	return nil
}

func newStore(t *testing.T, dir string) *Store {
	t.Helper()
	var n atomic.Int64
	return New(migrate.OpenData(dir, quietLog), Options{
		NewSlug:         func() string { return fmt.Sprintf("random-slug-%06d", n.Add(1)) },
		ValidCustomSlug: fakeCustom,
		Now:             func() time.Time { return t0 },
		Logger:          quietLog,
	})
}

func draft() model.Link {
	return model.Link{Name: "Anna", ValidUntil: t0.Add(48 * time.Hour), TemplateIDs: []string{"house_entry"}}
}

func mustCreate(t *testing.T, s *Store, d model.Link, slug string) model.Link {
	t.Helper()
	l, err := s.Create(d, slug)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestCreate(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	d := draft()
	// Server-owned fields in the draft are ignored.
	d.ID, d.Slug, d.UseCount, d.SuggestNewURL = "mine", "chosen-by-client", 99, true
	d.Passwords = []model.LinkPassword{{ID: "p", Label: "x", Hash: hash}}
	d.Used = []model.Use{{EntityID: "lock.a", Action: "lock.unlock", At: t0}}
	l := mustCreate(t, s, d, "")
	if l.ID == "mine" || len(l.ID) != 16 || l.Slug != "random-slug-000001" || !l.CreatedAt.Equal(t0) {
		t.Errorf("created %+v", l)
	}
	if l.UseCount != 0 || l.SuggestNewURL || len(l.Passwords) != 0 || len(l.Used) != 0 {
		t.Errorf("server-owned fields copied from the draft: %+v", l)
	}
	got, err := newStore(t, dir).Get(l.ID)
	if err != nil || got.Name != "Anna" || got.Slug != l.Slug {
		t.Fatalf("reload: %+v, %v", got, err)
	}
	st, err := os.Stat(filepath.Join(dir, migrate.LinksFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("links.yaml mode: %v, %v", st, err)
	}
}

func TestCreateErrors(t *testing.T) {
	s := newStore(t, t.TempDir())
	mustCreate(t, s, draft(), "custom-slug-one")
	noEnd := draft()
	noEnd.ValidUntil = time.Time{}
	for _, tc := range []struct {
		name  string
		d     model.Link
		slug  string
		check func(error) bool
	}{
		{"invalid custom slug", draft(), "Short", func(err error) bool { return errors.Is(err, errBadSlug) }},
		{"taken custom slug", draft(), "custom-slug-one", func(err error) bool { return errors.Is(err, ErrSlugTaken) }},
		{"invalid link", noEnd, "", func(err error) bool {
			var ve *model.ValidationError
			return errors.As(err, &ve) && ve.Problems[0].Path == "valid_until"
		}},
	} {
		if _, err := s.Create(tc.d, tc.slug); !tc.check(err) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	ls, _ := s.List()
	if len(ls.Links) != 1 {
		t.Errorf("%d links after refused creates", len(ls.Links))
	}
}

// A random slug that is already taken is drawn again.
func TestCreateRetriesTakenRandomSlug(t *testing.T) {
	slugs := []string{"random-slug-a", "random-slug-a", "random-slug-b"}
	var i int
	s := New(migrate.OpenData(t.TempDir(), quietLog), Options{
		NewSlug: func() string { i++; return slugs[i-1] }, ValidCustomSlug: fakeCustom, Logger: quietLog,
	})
	a := mustCreate(t, s, draft(), "")
	b := mustCreate(t, s, draft(), "")
	if a.Slug != "random-slug-a" || b.Slug != "random-slug-b" {
		t.Errorf("slugs %q, %q", a.Slug, b.Slug)
	}
}

func TestFindBySlug(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	a := mustCreate(t, s, draft(), "")
	b := mustCreate(t, s, draft(), "custom-slug-bbb")
	for slug, want := range map[string]string{a.Slug: a.ID, b.Slug: b.ID} {
		if l, ok := s.FindBySlug(slug); !ok || l.ID != want {
			t.Errorf("FindBySlug(%q) = %s, %v", slug, l.ID, ok)
		}
	}
	// Exact match only: no case folding, trimming or prefixes.
	for _, slug := range []string{"", "CUSTOM-SLUG-BBB", " custom-slug-bbb", "custom-slug-bbb ", "custom-slug-bb", a.Slug + "x"} {
		if _, ok := s.FindBySlug(slug); ok {
			t.Errorf("FindBySlug(%q) found a link", slug)
		}
	}

	// A rotated slug stops working at once; the new one works.
	rot, err := s.RotateSlug(b.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.FindBySlug(b.Slug); ok {
		t.Error("old slug still works after rotate")
	}
	if l, ok := s.FindBySlug(rot.Slug); !ok || l.ID != b.ID {
		t.Error("new slug does not work")
	}
	if _, err := s.RotateSlug(a.ID, rot.Slug); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("rotate to a taken slug: %v", err)
	}

	// A deleted link is gone.
	if err := s.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.FindBySlug(a.Slug); ok {
		t.Error("deleted link found")
	}
	if err := s.Delete(a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

// A change to links.yaml on disk (a restore) is picked up by the index.
func TestFindBySlugSeesFileChange(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	l := mustCreate(t, s, draft(), "")
	if _, ok := s.FindBySlug(l.Slug); !ok {
		t.Fatal("not found")
	}
	path := filepath.Join(dir, migrate.LinksFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), l.Slug, "edited-on-disk-slug", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	future := t0.Add(time.Hour)
	_ = os.Chtimes(path, future, future)
	if _, ok := s.FindBySlug(l.Slug); ok {
		t.Error("old slug found after the file changed")
	}
	if got, ok := s.FindBySlug("edited-on-disk-slug"); !ok || got.ID != l.ID {
		t.Error("new slug from disk not found")
	}
}

// A hand-written links.yaml with a link that has no templates and no
// rules loads with no error: such a link is valid and grants nothing.
func TestEmptyLinkLoadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	l := mustCreate(t, s, draft(), "")
	path := filepath.Join(dir, migrate.LinksFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for line := range strings.Lines(string(raw)) {
		if !strings.HasPrefix(strings.TrimSpace(line), "templates:") && !strings.HasPrefix(strings.TrimSpace(line), "- house_entry") {
			kept = append(kept, line)
		}
	}
	edited := strings.Join(kept, "")
	if edited == string(raw) {
		t.Fatalf("the edit changed nothing:\n%s", raw)
	}
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	future := t0.Add(time.Hour)
	_ = os.Chtimes(path, future, future)
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.FindBySlug(l.Slug)
	if !ok || len(got.TemplateIDs) != 0 || len(got.Extra) != 0 || got.Validate() != nil || len(all.Links) != 1 {
		t.Errorf("empty link from disk = %+v, %v (file:\n%s)", got, ok, edited)
	}
}

func TestUpdateKeepsServerFields(t *testing.T) {
	s := newStore(t, t.TempDir())
	l := mustCreate(t, s, draft(), "")
	if _, err := s.AddPassword(l.ID, "Anna", hash); err != nil {
		t.Fatal(err)
	}
	// An edit from the admin API: hashes are not in its JSON, and a
	// client might send anything for the server's fields.
	edit := draft()
	edit.Name, edit.Note, edit.OneTime = "Anna and Ben", "door code is in the script", true
	edit.ValidUntil = t0.Add(72 * time.Hour)
	edit.Slug, edit.SuggestNewURL, edit.UseCount = "client-slug-xxxx", true, 5
	edit.Passwords = []model.LinkPassword{{ID: "p", Label: "x"}}
	got, err := s.Update(l.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Anna and Ben" || !got.OneTime || !got.ValidUntil.Equal(t0.Add(72*time.Hour)) {
		t.Errorf("edit not applied: %+v", got)
	}
	if got.Slug != l.Slug || got.SuggestNewURL || got.UseCount != 0 || len(got.Passwords) != 1 || got.Passwords[0].Hash != hash {
		t.Errorf("server fields changed: %+v", got)
	}
	bad := edit
	bad.ValidUntil = time.Time{}
	if _, err := s.Update(l.ID, bad); err == nil {
		t.Error("saved a link with no end")
	}
	// A link with no templates and no rules is valid. It grants nothing.
	empty := edit
	empty.TemplateIDs, empty.Extra = nil, nil
	if got, err := s.Update(l.ID, empty); err != nil || len(got.TemplateIDs) != 0 || len(got.Extra) != 0 {
		t.Errorf("empty link = %+v, %v", got, err)
	}
	if _, err := s.Update("nope", edit); !errors.Is(err, ErrNotFound) {
		t.Errorf("update unknown: %v", err)
	}
}

// entity_order is an admin field: create and update keep it, and it
// reaches the file.
func TestEntityOrderIsEditable(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	d := draft()
	d.EntityOrder = []string{"switch.fan", "light.lamp"}
	l := mustCreate(t, s, d, "")
	if fmt.Sprint(l.EntityOrder) != "[switch.fan light.lamp]" {
		t.Errorf("create: %v", l.EntityOrder)
	}
	edit := draft()
	edit.EntityOrder = []string{"light.lamp"}
	got, err := s.Update(l.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got.EntityOrder) != "[light.lamp]" {
		t.Errorf("update: %v", got.EntityOrder)
	}
	if again, err := newStore(t, dir).Get(l.ID); err != nil || fmt.Sprint(again.EntityOrder) != "[light.lamp]" {
		t.Errorf("reload: %v %v", again.EntityOrder, err)
	}
}

// The guest title and welcome are admin fields: create and update keep
// them, and they reach the file.
func TestGuestTextIsEditable(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	d := draft()
	d.GuestTitle, d.GuestWelcome = "Anna's weekend", "Hi Anna"
	l := mustCreate(t, s, d, "")
	if l.GuestTitle != "Anna's weekend" || l.GuestWelcome != "Hi Anna" {
		t.Errorf("create: %q %q", l.GuestTitle, l.GuestWelcome)
	}
	edit := draft()
	edit.GuestTitle = "Welcome, Anna"
	got, err := s.Update(l.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	if got.GuestTitle != "Welcome, Anna" || got.GuestWelcome != "" {
		t.Errorf("update: %q %q", got.GuestTitle, got.GuestWelcome)
	}
	if again, err := newStore(t, dir).Get(l.ID); err != nil || again.GuestTitle != "Welcome, Anna" || again.GuestWelcome != "" {
		t.Errorf("reload: %+v %v", again, err)
	}
	bad := draft()
	bad.GuestTitle = "a\nb"
	if _, err := s.Update(l.ID, bad); err == nil {
		t.Error("saved a title with a line break")
	}
}

// An inactive link saves with no end, and every write path keeps it
// valid. It refuses uses and devices until the admin turns it on again.
func TestInactiveLink(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	d := draft()
	d.Inactive, d.ValidUntil = true, time.Time{}
	l := mustCreate(t, s, d, "")
	if !l.Inactive || !l.ValidUntil.IsZero() || l.Status(t0) != model.StatusInactive {
		t.Fatalf("create: %+v", l)
	}
	if _, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0); !errors.Is(err, ErrInactive) {
		t.Errorf("consume: %v", err)
	}
	if _, err := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev); !errors.Is(err, ErrInactive) {
		t.Errorf("add device: %v", err)
	}
	if _, err := s.AddPassword(l.ID, "Anna", hash); err != nil {
		t.Errorf("add password: %v", err)
	}
	if _, err := s.RotateSlug(l.ID, ""); err != nil {
		t.Errorf("rotate: %v", err)
	}
	got, _ := s.Get(l.ID)
	if got.UseCount != 0 || len(got.Used) != 0 || len(got.Devices) != 0 {
		t.Errorf("inactive link changed: %+v", got)
	}
	if again, err := newStore(t, dir).Get(l.ID); err != nil || !again.Inactive || !again.ValidUntil.IsZero() {
		t.Errorf("reload: %+v %v", again, err)
	}

	// An active link still needs an end.
	edit := draft()
	edit.ValidUntil = time.Time{}
	if _, err := s.Update(l.ID, edit); err == nil {
		t.Error("saved an active link with no end")
	}
	// Turning it on with a window makes it work.
	on, err := s.Update(l.ID, draft())
	if err != nil {
		t.Fatal(err)
	}
	if on.Inactive || on.Status(t0) != model.StatusActive {
		t.Errorf("activate: %+v", on)
	}
	if _, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0); err != nil {
		t.Errorf("consume after activate: %v", err)
	}
	// Turning it off again keeps the window.
	off := draft()
	off.Inactive = true
	got, err = s.Update(l.ID, off)
	if err != nil || !got.Inactive || !got.ValidUntil.Equal(off.ValidUntil) {
		t.Errorf("deactivate: %+v %v", got, err)
	}
	// Deactivate on an inactive link is a no-op.
	if got, err := s.Deactivate(l.ID); err != nil || got.Status(t0) != model.StatusInactive {
		t.Errorf("deactivate twice: %+v %v", got, err)
	}
}

// Deactivate turns a link off and keeps everything else. Activate turns
// it back on with the same state.
func TestDeactivateActivateKeepState(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	d := draft()
	d.OneTime = true
	l := mustCreate(t, s, d, "")
	pw, err := s.AddPassword(l.ID, "Anna", hash)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0); err != nil {
		t.Fatal(err)
	}
	off, err := s.Deactivate(l.ID)
	if err != nil || off.Status(t0) != model.StatusInactive {
		t.Fatalf("deactivate: %+v %v", off, err)
	}
	if _, err := s.Consume(l.ID, "lock.b", "lock.unlock", t0); !errors.Is(err, ErrInactive) {
		t.Errorf("consume on an inactive link: %v", err)
	}
	if _, err := s.AddDevice(l.ID, newDevice(model.ApprovedByAdmin), maxDev); !errors.Is(err, ErrInactive) {
		t.Errorf("add device on an inactive link: %v", err)
	}
	on, changed, err := s.Activate(l.ID, Activation{}, t0)
	if err != nil || on.Status(t0) != model.StatusActive || !slices.Equal(changed, []string{"inactive"}) {
		t.Fatalf("activate: %+v %v %v", on, changed, err)
	}
	got, _ := newStore(t, dir).Get(l.ID)
	if got.Slug != l.Slug || got.UseCount != 1 || len(got.Used) != 1 || len(got.Devices) != 1 || got.Devices[0].ID != dev.ID ||
		len(got.Passwords) != 1 || got.Passwords[0].ID != pw.ID || !got.ValidUntil.Equal(l.ValidUntil) {
		t.Errorf("state not kept: %+v", got)
	}
	if _, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0); !errors.Is(err, ErrUsed) {
		t.Errorf("used pair after activate: %v", err)
	}
}

func TestActivate(t *testing.T) {
	later := t0.Add(24 * time.Hour)
	for _, tc := range []struct {
		name    string
		link    func(*model.Link)
		a       Activation
		wantErr error // nil, ErrNoWindow, ErrSlugTaken, or errInvalid for a validation error
		changed []string
		check   func(t *testing.T, before, after model.Link)
	}{
		{name: "keep window", link: func(l *model.Link) { l.Inactive = true }, changed: []string{"inactive"}},
		{name: "already on, nothing to do", changed: nil},
		{name: "new until", link: func(l *model.Link) { l.Inactive = true },
			a: Activation{SetUntil: true, ValidUntil: later}, changed: []string{"valid_until", "inactive"},
			check: func(t *testing.T, _, after model.Link) {
				if !after.ValidUntil.Equal(later) {
					t.Errorf("until %v", after.ValidUntil)
				}
			}},
		{name: "same until is no change", a: Activation{SetUntil: true, ValidUntil: t0.Add(48 * time.Hour)}, changed: nil},
		{name: "set and clear from", link: func(l *model.Link) { l.Inactive = true; l.ValidFrom = t0.Add(-time.Hour) },
			a: Activation{SetFrom: true}, changed: []string{"valid_from", "inactive"},
			check: func(t *testing.T, _, after model.Link) {
				if !after.ValidFrom.IsZero() {
					t.Errorf("from %v", after.ValidFrom)
				}
			}},
		{name: "future from is scheduled", a: Activation{SetFrom: true, ValidFrom: t0.Add(time.Hour)}, changed: []string{"valid_from"},
			check: func(t *testing.T, _, after model.Link) {
				if after.Status(t0) != model.StatusScheduled {
					t.Errorf("status %s", after.Status(t0))
				}
			}},
		{name: "no end", link: func(l *model.Link) { l.Inactive = true; l.ValidUntil = time.Time{} }, wantErr: ErrNoWindow},
		{name: "end passed", link: func(l *model.Link) { l.Inactive = true; l.ValidUntil = t0.Add(-time.Hour) }, wantErr: ErrNoWindow},
		{name: "end now", a: Activation{SetUntil: true, ValidUntil: t0}, wantErr: ErrNoWindow},
		{name: "until cleared", a: Activation{SetUntil: true}, wantErr: ErrNoWindow},
		{name: "past end fixed", link: func(l *model.Link) { l.Inactive = true; l.ValidUntil = t0.Add(-time.Hour) },
			a: Activation{SetUntil: true, ValidUntil: later}, changed: []string{"valid_until", "inactive"}},
		{name: "until before from", a: Activation{SetFrom: true, ValidFrom: later.Add(time.Hour), SetUntil: true, ValidUntil: later}, wantErr: errInvalid},
		{name: "new random url", link: func(l *model.Link) { l.Inactive = true }, a: Activation{NewURL: true},
			changed: []string{"inactive", "url"},
			check: func(t *testing.T, before, after model.Link) {
				if after.Slug == before.Slug {
					t.Error("slug kept")
				}
			}},
		{name: "new custom url", a: Activation{NewURL: true, CustomSlug: "a-custom-slug-here"}, changed: []string{"url"},
			check: func(t *testing.T, _, after model.Link) {
				if after.Slug != "a-custom-slug-here" {
					t.Errorf("slug %s", after.Slug)
				}
			}},
		{name: "taken url", a: Activation{NewURL: true, CustomSlug: "taken-slug-other"}, wantErr: ErrSlugTaken},
		{name: "bad custom url", a: Activation{NewURL: true, CustomSlug: "BAD"}, wantErr: errBadSlug},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := newStore(t, dir)
			mustCreate(t, s, draft(), "taken-slug-other")
			d := draft()
			if tc.link != nil {
				tc.link(&d)
			}
			before := mustCreate(t, s, d, "")
			after, changed, err := s.Activate(before.ID, tc.a, t0)
			var ve *model.ValidationError
			switch {
			case tc.wantErr == errInvalid:
				if !errors.As(err, &ve) {
					t.Fatalf("err = %v, want a validation error", err)
				}
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			}
			stored, _ := newStore(t, dir).Get(before.ID)
			if tc.wantErr != nil {
				if !reflect.DeepEqual(stored, before) {
					t.Errorf("a refused activate changed the link:\n%+v\n%+v", stored, before)
				}
				return
			}
			if !slices.Equal(changed, tc.changed) {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
			if stored.Inactive || !reflect.DeepEqual(stored, after) {
				t.Errorf("stored %+v, returned %+v", stored, after)
			}
			if tc.check != nil {
				tc.check(t, before, after)
			}
		})
	}
	if _, _, err := newStore(t, t.TempDir()).Activate("nope", Activation{}, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown link: %v", err)
	}
}

// errInvalid marks a want of *model.ValidationError in table tests.
var errInvalid = errors.New("invalid")

// Activate and a new URL clear SuggestNewURL. Deactivate keeps it.
func TestSuggestNewURLCleared(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, migrate.LinksFile), []byte(`version: 2
links:
  - {id: a, name: A, slug: slug-aaaaaaaaaa, created_at: 2026-11-10T09:00:00Z, valid_until: 2026-11-20T09:00:00Z, inactive: true, suggest_new_url: true}
  - {id: b, name: B, slug: slug-bbbbbbbbbb, created_at: 2026-11-10T09:00:00Z, valid_until: 2026-11-20T09:00:00Z, inactive: true, suggest_new_url: true}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newStore(t, dir)
	for _, id := range []string{"a", "b"} {
		if l, _ := s.Get(id); !l.SuggestNewURL || l.Status(t0) != model.StatusInactive {
			t.Fatalf("%s on load: %+v", id, l)
		}
	}
	if l, err := s.Deactivate("a"); err != nil || !l.SuggestNewURL {
		t.Errorf("deactivate: %+v %v", l, err)
	}
	if l, _, err := s.Activate("a", Activation{}, t0); err != nil || l.SuggestNewURL || l.Slug != "slug-aaaaaaaaaa" {
		t.Errorf("activate: %+v %v", l, err)
	}
	if l, err := s.RotateSlug("b", ""); err != nil || l.SuggestNewURL || l.Status(t0) != model.StatusInactive {
		t.Errorf("rotate: %+v %v", l, err)
	}
	// An editor save that keeps the link off keeps the flag; one that
	// turns it on clears it.
	if err := os.WriteFile(filepath.Join(dir, migrate.LinksFile), []byte(`version: 2
links:
  - {id: c, name: C, slug: slug-cccccccccc, created_at: 2026-11-10T09:00:00Z, valid_until: 2026-11-20T09:00:00Z, inactive: true, suggest_new_url: true}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	s = newStore(t, dir)
	c, _ := s.Get("c")
	if l, err := s.Update("c", c); err != nil || !l.SuggestNewURL {
		t.Errorf("save while off: %+v %v", l, err)
	}
	c.Inactive = false
	if l, err := s.Update("c", c); err != nil || l.SuggestNewURL || l.Status(t0) != model.StatusActive {
		t.Errorf("save that turns it on: %+v %v", l, err)
	}
}

func TestPasswords(t *testing.T) {
	s := newStore(t, t.TempDir())
	l := mustCreate(t, s, draft(), "")
	p1, err := s.AddPassword(l.ID, "Anna", hash)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.AddPassword(l.ID, "Ben", hash)
	if err != nil {
		t.Fatal(err)
	}
	if p1.ID == p2.ID || !p1.CreatedAt.Equal(t0) {
		t.Errorf("passwords %+v %+v", p1, p2)
	}
	if _, err := s.AddPassword(l.ID, "Plain", "5551234567"); err == nil {
		t.Error("stored a password that is not a hash")
	}
	if _, err := s.AddPassword(l.ID, " ", hash); err == nil {
		t.Error("stored a blank label")
	}
	if err := s.RemovePassword(l.ID, p1.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePassword(l.ID, p1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove twice: %v", err)
	}
	got, _ := s.Get(l.ID)
	if len(got.Passwords) != 1 || got.Passwords[0].ID != p2.ID {
		t.Errorf("passwords = %+v", got.Passwords)
	}
}

func TestConsumeCountsUses(t *testing.T) {
	s := newStore(t, t.TempDir())
	l := mustCreate(t, s, draft(), "")
	for i := range 3 {
		refund, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0.Add(time.Duration(i)*time.Minute))
		if err != nil || refund == nil {
			t.Fatalf("use %d: %v", i, err)
		}
	}
	got, _ := s.Get(l.ID)
	if got.UseCount != 3 || !got.LastUsedAt.Equal(t0.Add(2*time.Minute)) || len(got.Used) != 0 {
		t.Errorf("after 3 uses: count %d, last %v, used %v", got.UseCount, got.LastUsedAt, got.Used)
	}
	for _, tc := range []struct {
		name string
		id   string
		now  time.Time
		want error
	}{
		{"unknown link", "nope", t0, ErrNotFound},
		{"expired", l.ID, l.ValidUntil, ErrInactive},
	} {
		if _, err := s.Consume(tc.id, "lock.a", "lock.unlock", tc.now); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

func oneTimeLink(t *testing.T, s *Store) model.Link {
	t.Helper()
	d := draft()
	d.OneTime = true
	return mustCreate(t, s, d, "")
}

func TestConsumeOneTime(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	l := oneTimeLink(t, s)
	if _, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0.Add(time.Second)); !errors.Is(err, ErrUsed) {
		t.Errorf("second use: %v", err)
	}
	// Another action or entity is its own pair. Ids are compared exactly.
	for _, k := range [][2]string{{"lock.a", "lock.lock"}, {"lock.b", "lock.unlock"}, {"LOCK.A", "lock.unlock"}} {
		if _, err := s.Consume(l.ID, k[0], k[1], t0); err != nil {
			t.Errorf("Consume(%v): %v", k, err)
		}
	}
	// The use survives a restart.
	if _, err := newStore(t, dir).Consume(l.ID, "lock.a", "lock.unlock", t0.Add(time.Hour)); !errors.Is(err, ErrUsed) {
		t.Errorf("after reload: %v", err)
	}
}

// Of N parallel uses of one one-time pair, exactly one wins.
func TestConsumeConcurrentExactlyOnce(t *testing.T) {
	s := newStore(t, t.TempDir())
	l := oneTimeLink(t, s)
	const n = 32
	var wins, used atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range n {
		wg.Go(func() {
			<-start
			_, err := s.Consume(l.ID, "lock.front_door", "lock.unlock", t0)
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrUsed):
				used.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || used.Load() != n-1 {
		t.Fatalf("wins %d, used %d; want 1 and %d", wins.Load(), used.Load(), n-1)
	}
	got, _ := s.Get(l.ID)
	if len(got.Used) != 1 || got.UseCount != 1 {
		t.Errorf("used %v, count %d", got.Used, got.UseCount)
	}
}

func TestRefund(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	l := oneTimeLink(t, s)
	prev := t0.Add(-time.Hour)
	if _, err := s.Consume(l.ID, "lock.a", "lock.lock", prev); err != nil {
		t.Fatal(err)
	}
	refund, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0)
	if err != nil {
		t.Fatal(err)
	}
	refund()
	refund() // a second call does nothing
	got, _ := newStore(t, dir).Get(l.ID)
	if len(got.Used) != 1 || got.Used[0].Action != "lock.lock" || got.UseCount != 1 || !got.LastUsedAt.Equal(prev) {
		t.Fatalf("after refund: used %v, count %d, last %v", got.Used, got.UseCount, got.LastUsedAt)
	}
	// The pair can be used again, and the refunded one does not come back.
	refund2, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("use after refund: %v", err)
	}
	refund() // late call of the old refund must not remove the new use
	if _, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0.Add(2*time.Minute)); !errors.Is(err, ErrUsed) {
		t.Errorf("old refund removed a later use: %v", err)
	}
	refund2()
	// A refund for a deleted link is a no-op.
	refund3, err := s.Consume(l.ID, "lock.b", "lock.unlock", t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(l.ID); err != nil {
		t.Fatal(err)
	}
	refund3()
}

// A link file that does not parse is never overwritten by a use.
func TestConsumeRefusesBrokenFile(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	l := oneTimeLink(t, s)
	path := filepath.Join(dir, migrate.LinksFile)
	if err := os.WriteFile(path, []byte("links: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := t0.Add(time.Hour)
	_ = os.Chtimes(path, future, future)
	if _, err := s.Consume(l.ID, "lock.a", "lock.unlock", t0); err == nil {
		t.Error("consumed with a broken file")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "links: [\n" {
		t.Errorf("file overwritten: %q", raw)
	}
	// The last good links still resolve, so valid slugs keep working.
	if _, ok := s.FindBySlug(l.Slug); !ok {
		t.Error("last good link not found")
	}
}
