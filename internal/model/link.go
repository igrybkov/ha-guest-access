package model

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Link is one guest link in links.yaml. It grants the union of its
// templates' rules and its extra rules, inside its validity window.
type Link struct {
	// ID is a random internal id. It never changes and never appears in
	// the guest URL.
	ID   string `yaml:"id" json:"id"`
	Name string `yaml:"name" json:"name"`
	Note string `yaml:"note,omitempty" json:"note,omitempty"`
	// Slug is the secret part of the guest URL. It is kept so the admin UI
	// can show the URL again. Lookups go by its hash (access.HashSlug).
	Slug      string    `yaml:"slug" json:"slug"`
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
	// ValidFrom is when the link starts to work. Zero means at once.
	ValidFrom time.Time `yaml:"valid_from,omitempty" json:"valid_from,omitzero"`
	// ValidUntil is when the link stops working. Every link must end.
	ValidUntil  time.Time `yaml:"valid_until" json:"valid_until"`
	TemplateIDs []string  `yaml:"templates,omitempty" json:"templates,omitempty"`
	// Extra are rules for this link only. Each carries its own access.
	Extra []Rule `yaml:"extra,omitempty" json:"extra,omitempty"`
	// OneTime makes every (entity, action) pair of the link usable once.
	OneTime bool `yaml:"one_time,omitempty" json:"one_time,omitempty"`
	// Passwords lock the link. With none, the slug alone is enough. With
	// any, the guest must enter one of them first.
	Passwords []LinkPassword `yaml:"passwords,omitempty" json:"passwords,omitempty"`
	// Used lists the pairs a one-time link has used up.
	Used    []Use `yaml:"used,omitempty" json:"used,omitempty"`
	Revoked bool  `yaml:"revoked,omitempty" json:"revoked,omitempty"`
	// UseCount counts successful actions, for the admin list.
	UseCount   int        `yaml:"use_count,omitempty" json:"use_count,omitempty"`
	LastUsedAt *time.Time `yaml:"last_used_at,omitempty" json:"last_used_at,omitempty"`
}

// Use is one used-up (entity, action) pair of a one-time link.
type Use struct {
	EntityID string    `yaml:"entity_id" json:"entity_id"`
	Action   string    `yaml:"action" json:"action"`
	At       time.Time `yaml:"at" json:"at"`
}

// LinkPassword is one password of a link, often a guest's phone number.
// Only the hash is stored, never the plain value, nor any part of it. The
// admin API never returns Hash.
type LinkPassword struct {
	ID string `yaml:"id" json:"id"`
	// Label names the guest. It shows in logs and in the
	// guest_access_action event.
	Label string `yaml:"label" json:"label"`
	// Hash is the argon2id PHC string of the exact bytes the admin typed
	// (access.HashPassword). Nothing is trimmed or normalized.
	Hash      string    `yaml:"hash" json:"-"`
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
}

// argon2Prefix starts every argon2id PHC string. A hash without it is
// refused, so a plain value can't slip in as a hash.
const argon2Prefix = "$argon2id$"

// Password returns the password with id.
func (l Link) Password(id string) (LinkPassword, bool) {
	for _, p := range l.Passwords {
		if p.ID == id {
			return p, true
		}
	}
	return LinkPassword{}, false
}

// UsedAt reports whether the pair was used, and when.
func (l Link) UsedAt(entityID, action string) (time.Time, bool) {
	for _, u := range l.Used {
		if u.EntityID == entityID && u.Action == action {
			return u.At, true
		}
	}
	return time.Time{}, false
}

// Link statuses, as Status returns them.
const (
	StatusActive    = "active"
	StatusScheduled = "scheduled" // before ValidFrom
	StatusExpired   = "expired"   // at or after ValidUntil
	StatusRevoked   = "revoked"
)

// Status returns the link's status at now. Revoked wins over the window.
func (l Link) Status(now time.Time) string {
	switch {
	case l.Revoked:
		return StatusRevoked
	case !l.ValidFrom.IsZero() && now.Before(l.ValidFrom):
		return StatusScheduled
	case !now.Before(l.ValidUntil):
		return StatusExpired
	}
	return StatusActive
}

// Active reports whether the link works at now.
func (l Link) Active(now time.Time) bool { return l.Status(now) == StatusActive }

// UsesLocalNetwork reports whether any extra rule needs the local network.
// Template rules are checked on the templates.
func (l Link) UsesLocalNetwork() bool {
	for _, r := range l.Extra {
		if r.UsesLocalNetwork() {
			return true
		}
	}
	return false
}

// slugRe is what a stored slug may hold: random slugs are base64url,
// custom ones are a subset. access.ValidCustomSlug is stricter.
var slugRe = regexp.MustCompile(`^[A-Za-z0-9_-]{12,128}$`)

// Validate checks the link's shape. It does not check that the templates
// exist.
func (l Link) Validate() error {
	var p problems
	l.validate(&p, "")
	return p.err()
}

func (l Link) validate(p *problems, path string) {
	if !idRe.MatchString(l.ID) {
		p.add(join(path, "id"), "must be 1-64 of a-z, 0-9, _ and -")
	}
	if strings.TrimSpace(l.Name) == "" {
		p.add(join(path, "name"), "is empty")
	}
	if !slugRe.MatchString(l.Slug) {
		p.add(join(path, "slug"), "must be 12-128 of A-Z, a-z, 0-9, _ and -")
	}
	if l.ValidUntil.IsZero() {
		p.add(join(path, "valid_until"), "is required")
	} else if !l.ValidFrom.IsZero() && !l.ValidUntil.After(l.ValidFrom) {
		p.add(join(path, "valid_until"), "must be after valid_from")
	}
	if len(l.TemplateIDs) == 0 && len(l.Extra) == 0 {
		p.add(join(path, "templates"), "pick a template or add a rule")
	}
	seen := map[string]bool{}
	for i, id := range l.TemplateIDs {
		at := fmt.Sprintf("%s[%d]", join(path, "templates"), i)
		switch {
		case id == "":
			p.add(at, "is empty")
		case seen[id]:
			p.add(at, "%q is listed twice", id)
		}
		seen[id] = true
	}
	for i, r := range l.Extra {
		r.validate(p, fmt.Sprintf("%s[%d]", join(path, "extra"), i))
	}
	pwIDs := map[string]bool{}
	for i, pw := range l.Passwords {
		at := fmt.Sprintf("%s[%d]", join(path, "passwords"), i)
		switch {
		case !idRe.MatchString(pw.ID):
			p.add(join(at, "id"), "must be 1-64 of a-z, 0-9, _ and -")
		case pwIDs[pw.ID]:
			p.add(join(at, "id"), "%q is used twice", pw.ID)
		}
		pwIDs[pw.ID] = true
		if strings.TrimSpace(pw.Label) == "" {
			p.add(join(at, "label"), "is empty")
		}
		if !strings.HasPrefix(pw.Hash, argon2Prefix) {
			p.add(join(at, "hash"), "must be an argon2id hash")
		}
	}
	if l.UseCount < 0 {
		p.add(join(path, "use_count"), "is negative")
	}
}

// Links is links.yaml.
type Links struct {
	Version int    `yaml:"version" json:"version"`
	Links   []Link `yaml:"links" json:"links"`
}

// Find returns the link with id.
func (ls Links) Find(id string) (Link, bool) {
	for _, l := range ls.Links {
		if l.ID == id {
			return l, true
		}
	}
	return Link{}, false
}

// Validate checks every link and that ids and slugs are unique.
func (ls Links) Validate() error {
	var p problems
	ids, slugs := map[string]bool{}, map[string]bool{}
	for i, l := range ls.Links {
		at := fmt.Sprintf("links[%d]", i)
		l.validate(&p, at)
		if l.ID != "" && ids[l.ID] {
			p.add(join(at, "id"), "%q is used twice", l.ID)
		}
		if l.Slug != "" && slugs[l.Slug] {
			p.add(join(at, "slug"), "is used by another link")
		}
		ids[l.ID], slugs[l.Slug] = true, true
	}
	return p.err()
}
