package config

import (
	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
	"github.com/igrybkov/ha-guest-access/internal/store"
)

// TemplatesStore serves templates.yaml of the config set. A missing file
// has no templates.
type TemplatesStore struct {
	file *store.File[model.Templates]
}

// NewTemplatesStore returns the templates store of a config set.
func NewTemplatesStore(set *store.Set) *TemplatesStore {
	return &TemplatesStore{file: store.In(set, migrate.TemplatesFile, emptyTemplates)}
}

func emptyTemplates() model.Templates {
	return model.Templates{Version: migrate.ConfigCurrent, Templates: []model.Template{}}
}

// Templates returns the current templates and any load or validation
// error. Templates are returned even when they do not validate, so the
// admin UI can show and fix them. Callers that grant access must check
// each template with Template.Validate and skip the ones that fail.
func (s *TemplatesStore) Templates() (model.Templates, error) {
	v, err := s.file.Load()
	if v.Templates == nil {
		v.Templates = []model.Template{}
	}
	if err != nil {
		return v, err
	}
	return v, v.Validate()
}

// Save validates ts and writes it.
func (s *TemplatesStore) Save(ts model.Templates) error {
	return s.Update(func(cur *model.Templates) error {
		*cur = ts
		return nil
	})
}

// Update applies fn to the current templates and saves the result,
// validated, under the set lock.
func (s *TemplatesStore) Update(fn func(*model.Templates) error) error {
	return s.file.Update(func(v *model.Templates) error {
		if err := fn(v); err != nil {
			return err
		}
		if v.Templates == nil {
			v.Templates = []model.Template{}
		}
		return v.Validate()
	})
}
