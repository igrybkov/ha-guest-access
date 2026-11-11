package model

import (
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Any prefix ParsePrefix accepts parses again from its own String.
func FuzzParsePrefix(f *testing.F) {
	for _, s := range []string{"10.0.0.0/8", "172.30.32.1", "::1", "fc00::/7", " 1.2.3.4 ", "1.2.3.4/33", "", "::ffff:1.2.3.4"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParsePrefix(s)
		if err != nil {
			return
		}
		q, err := ParsePrefix(p.String())
		if err != nil || q != p {
			t.Fatalf("%q -> %v does not re-parse: %v, %v", s, p, q, err)
		}
	})
}

// Decoding and validating any templates file never panics.
func FuzzTemplatesYAML(f *testing.F) {
	if raw, err := os.ReadFile("../migrate/testdata/config/v1/templates.yaml"); err == nil {
		f.Add(raw)
	}
	f.Add([]byte("templates: [{id: a, name: A, rules: [{select: {entities: ['*']}, actions: [script.run], variables: {x: [{code: 1}]}}]}]"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var ts Templates
		if yaml.Unmarshal(raw, &ts) != nil {
			return
		}
		_ = ts.Validate()
	})
}

// Decoding and validating any links file never panics.
func FuzzLinksYAML(f *testing.F) {
	if raw, err := os.ReadFile("../migrate/testdata/data/v1/links.yaml"); err == nil {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var ls Links
		if yaml.Unmarshal(raw, &ls) != nil {
			return
		}
		_ = ls.Validate()
	})
}
