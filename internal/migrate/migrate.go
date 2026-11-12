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

	"go.yaml.in/yaml/v3"

	"github.com/igrybkov/ha-guest-access/internal/store"
)

// File names. Use these, never string literals.
const (
	TemplatesFile = "templates.yaml" // config set, model.Templates
	LinksFile     = "links.yaml"     // data set, model.Links
	// KeyFile holds the HMAC key for the location cookie, as raw bytes in
	// the data dir. It is not part of the data set: it has no schema.
	KeyFile = "hmac.key"
	// SelfSignedCertFile and SelfSignedKeyFile hold the self-signed
	// certificate of the guest port, as PEM in the data dir. The app makes
	// them when ssl is on and the files in /ssl are missing or broken. They
	// have no schema.
	SelfSignedCertFile = "guest_tls.crt"
	SelfSignedKeyFile  = "guest_tls.key"
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
//
//   - v1: first version.
//   - v2: a link has no "revoked" key. A revoked link is inactive and
//     suggests a new URL.
const DataCurrent = 2

// DataFiles are the names of every file in the data set, of every version.
var DataFiles = []string{LinksFile}

// DataSteps takes the data set from version i+1 to i+2 at index i.
var DataSteps = []store.Step{revokedToInactive}

// cleanFalse reports whether n is a YAML false or null. Quoted text such
// as "no" is not.
func cleanFalse(n *yaml.Node) bool {
	if n.Kind != yaml.ScalarNode {
		return false
	}
	switch n.ShortTag() {
	case "!!null":
		return true
	case "!!bool":
		var b bool
		return n.Decode(&b) == nil && !b
	}
	return false
}

func yamlTrue() *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"} }

// revokedToInactive takes the data set from v1 to v2. Each link with a
// "revoked" key loses it. A link that was revoked gets "inactive: true",
// so it stays off, and "suggest_new_url: true", because its old URL was
// meant to be dead. A revoked value that is not a clean false counts as
// true: a revoked link must never come back on by itself.
func revokedToInactive(t *store.Tree) error {
	doc, _, ok := t.Get(LinksFile)
	if !ok {
		return nil
	}
	list := store.MapGet(doc, "links")
	if list == nil || list.Kind != yaml.SequenceNode {
		return nil
	}
	for _, l := range list.Content {
		if l.Kind != yaml.MappingNode {
			continue
		}
		found, off := false, false
		for i := 0; i+1 < len(l.Content); i += 2 {
			if l.Content[i].Value != "revoked" {
				continue
			}
			found = true
			if !cleanFalse(l.Content[i+1]) {
				off = true
			}
		}
		if off {
			store.MapSet(l, "inactive", yamlTrue())
			store.MapSet(l, "suggest_new_url", yamlTrue())
		}
		if found {
			store.MapDelete(l, "revoked")
		}
	}
	return nil
}

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
