package model

import (
	"slices"
	"strings"
)

// EntityText is the admin's own text for one entity. Guests see Name in
// place of the HA name, and Description as a short line under it. It
// applies app-wide: every template and link shows the same text. Both
// are plain text.
type EntityText struct {
	// Name replaces the HA name. Empty means the HA name.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Description may hold line breaks; the guest page shows them.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// MaxEntityNameLen and MaxEntityDescriptionLen cap EntityText, in bytes.
// They are shape limits, not options: a lower cap must never make a
// saved file fail.
const (
	MaxEntityNameLen        = 120
	MaxEntityDescriptionLen = 300
)

// Empty reports whether t has no text but spaces.
func (t EntityText) Empty() bool {
	return strings.TrimSpace(t.Name) == "" && strings.TrimSpace(t.Description) == ""
}

// Trimmed returns t without leading and trailing spaces.
func (t EntityText) Trimmed() EntityText {
	return EntityText{Name: strings.TrimSpace(t.Name), Description: strings.TrimSpace(t.Description)}
}

// ValidEntityID reports whether id looks like an HA entity id, such as
// "light.kitchen". It never checks that the entity exists.
func ValidEntityID(id string) bool { return entityIDRe.MatchString(id) }

// Validate checks the caps and the characters.
func (t EntityText) Validate() error {
	var p problems
	t.validate(&p, "")
	return p.err()
}

func (t EntityText) validate(p *problems, path string) {
	if len(t.Name) > MaxEntityNameLen || hasControl(t.Name) {
		p.add(join(path, "name"), "must be one line of at most %d bytes with no control characters", MaxEntityNameLen)
	}
	d := strings.NewReplacer("\r\n", "", "\n", "").Replace(t.Description)
	if len(t.Description) > MaxEntityDescriptionLen || hasControl(d) {
		p.add(join(path, "description"), "must be at most %d bytes with no control characters but line breaks", MaxEntityDescriptionLen)
	}
}

// entityTextPath is the problem path of one entry. Entity ids hold dots,
// so the id goes in brackets: entities[light.kitchen].name.
func entityTextPath(id string) string { return "entities[" + id + "]" }

// validateEntityTexts checks every entry of Templates.Entities. An entry
// for an entity HA does not know is fine: it is kept and does nothing.
func validateEntityTexts(p *problems, m map[string]EntityText) {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		at := entityTextPath(id)
		if !ValidEntityID(id) {
			p.add(at, "%q is not an entity id like light.kitchen", id)
		}
		m[id].validate(p, at)
	}
}

// EntityText returns the text of id when it validates and is not empty.
// A bad entry (a hand edit) is skipped, so guests never see it.
func (ts Templates) EntityText(id string) (EntityText, bool) {
	t, ok := ts.Entities[id]
	if !ok || !ValidEntityID(id) || t.Validate() != nil || t.Empty() {
		return EntityText{}, false
	}
	return t.Trimmed(), true
}
