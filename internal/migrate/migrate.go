// Package migrate owns the schema versions of the app's two file sets and
// the steps that bring older files up to them:
//
//   - config: templates.yaml in /homeassistant/guest_access, tracked in the
//     HA git repo. No secrets here.
//   - data: links.yaml in /data. Never in git.
//
// Settings are not files: they come from the app options (see package
// config).
//
// Every file holds the version it was written at. Reading migrates all of
// them in memory, one step per version; nothing is written until the user
// changes something (see store.Set). To change a set's schema:
//
//  1. Add a step that takes version Current to Current+1. It works on YAML
//     nodes only: the app types will move on, the step must not.
//  2. Bump the Current constant, and add any new file name to the Files
//     list. Never remove a name: a file of an old version must still be
//     found.
//  3. Add testdata/<set>/v<new Current>/ with files at the new version.
package migrate

import (
	"log/slog"

	"github.com/igrybkov/ha-guest-access/internal/store"
)

// File names. Use these, never string literals.
const (
	TemplatesFile = "templates.yaml" // config set, model.Templates
	LinksFile     = "links.yaml"     // data set, model.Links
	// KeyFile holds the HMAC key for the location cookie, as raw bytes in
	// the data dir. It is not part of the data set: it has no schema.
	KeyFile = "hmac.key"
)

// ConfigCurrent is the schema version of the config set this build reads
// and writes.
const ConfigCurrent = 1

// ConfigFiles are the names of every file in the config set, of every
// version.
var ConfigFiles = []string{TemplatesFile}

// ConfigSteps takes the config set from version i+1 to i+2 at index i.
var ConfigSteps = []store.Step{}

// DataCurrent is the schema version of the data set.
const DataCurrent = 1

// DataFiles are the names of every file in the data set, of every version.
var DataFiles = []string{LinksFile}

// DataSteps takes the data set from version i+1 to i+2 at index i.
var DataSteps = []store.Step{}

// OpenConfig returns the config set in dir.
func OpenConfig(dir string, log *slog.Logger) *store.Set {
	return store.OpenSet(dir, store.SetOptions{Current: ConfigCurrent, Files: ConfigFiles, Steps: ConfigSteps, Logger: log})
}

// DataFileMode is the mode of the data set's files. Links hold slugs and
// password hashes: only the app reads them.
const DataFileMode = 0o600

// OpenData returns the data set in dir. Its files are written 0600.
func OpenData(dir string, log *slog.Logger) *store.Set {
	return store.OpenSet(dir, store.SetOptions{Current: DataCurrent, Files: DataFiles, Steps: DataSteps, FileMode: DataFileMode, Logger: log})
}
