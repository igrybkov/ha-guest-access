package config

import (
	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
	"github.com/igrybkov/ha-guest-access/internal/store"
)

// SettingsStore serves settings.yaml of the config set. A missing file
// gives model.DefaultSettings.
type SettingsStore struct {
	file *store.File[model.Settings]
}

// NewSettingsStore returns the settings store of a config set (see
// migrate.OpenConfig).
func NewSettingsStore(set *store.Set) *SettingsStore {
	return &SettingsStore{file: store.In(set, migrate.SettingsFile, model.DefaultSettings)}
}

// Settings returns the current settings with defaults filled in.
//
// On an error it still returns settings that are safe to use: the last
// good file, or model.DefaultSettings when the file does not validate. So
// a bad hand edit (an unknown trusted proxy, a zero rate limit) falls back
// to the defaults instead of half applying. Report the error in the admin
// UI and the log.
func (s *SettingsStore) Settings() (model.Settings, error) {
	v, err := s.file.Load()
	fillSettings(&v)
	if verr := v.Validate(); verr != nil {
		return model.DefaultSettings(), verr
	}
	return v, err
}

// Save validates v and writes it. Like Update, it refuses to overwrite a
// settings.yaml that does not parse, so a broken hand edit is never lost.
func (s *SettingsStore) Save(v model.Settings) error {
	return s.Update(func(cur *model.Settings) error {
		*cur = v
		return nil
	})
}

// Update applies fn to the current settings and saves the result, with
// defaults filled and validated, under the set lock.
func (s *SettingsStore) Update(fn func(*model.Settings) error) error {
	return s.file.Update(func(v *model.Settings) error {
		fillSettings(v)
		if err := fn(v); err != nil {
			return err
		}
		fillSettings(v)
		return v.Validate()
	})
}

// fillSettings fills what a hand-written file left out. A missing
// local_access.networks gets the default networks; an empty list stays
// empty. Zero rate limits get their defaults.
func fillSettings(v *model.Settings) {
	if v.Version == 0 {
		v.Version = migrate.ConfigCurrent
	}
	if v.LocalAccess.Networks == nil {
		v.LocalAccess.Networks = model.DefaultSettings().LocalAccess.Networks
	}
	if v.TrustedProxies == nil {
		v.TrustedProxies = []string{}
	}
	v.RateLimits = v.RateLimits.WithDefaults()
}

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

// SecretsStore serves secrets.yaml of the data set. A missing file has no
// secrets. Never log or return its values over the API.
type SecretsStore struct {
	file *store.File[model.Secrets]
}

// NewSecretsStore returns the secrets store of a data set (see
// migrate.OpenData).
func NewSecretsStore(set *store.Set) *SecretsStore {
	return &SecretsStore{file: store.In(set, migrate.SecretsFile, func() model.Secrets {
		return model.Secrets{Version: migrate.DataCurrent}
	})}
}

// Secrets returns the current secrets, or the last good ones with the
// error.
func (s *SecretsStore) Secrets() (model.Secrets, error) { return s.file.Load() }

// Update applies fn to the current secrets and saves them under the set
// lock.
func (s *SecretsStore) Update(fn func(*model.Secrets) error) error { return s.file.Update(fn) }
