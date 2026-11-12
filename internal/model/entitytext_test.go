package model

import (
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestEntityTextValidate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  EntityText
		paths []string
	}{
		{"empty", EntityText{}, nil},
		{"both", EntityText{Name: "Reading lamp", Description: "By the bed"}, nil},
		{"unicode", EntityText{Name: "Лампа для читання", Description: "Біля ліжка"}, nil},
		{"name at cap", EntityText{Name: strings.Repeat("a", MaxEntityNameLen)}, nil},
		{"name over cap", EntityText{Name: strings.Repeat("a", MaxEntityNameLen+1)}, []string{"name"}},
		{"name over cap in bytes", EntityText{Name: strings.Repeat("é", MaxEntityNameLen/2+1)}, []string{"name"}},
		{"name line break", EntityText{Name: "a\nb"}, []string{"name"}},
		{"name tab", EntityText{Name: "a\tb"}, []string{"name"}},
		{"name control", EntityText{Name: "a\x00b"}, []string{"name"}},
		{"description lines", EntityText{Description: "line one\nline two\r\nline three"}, nil},
		{"description at cap", EntityText{Description: strings.Repeat("a", MaxEntityDescriptionLen)}, nil},
		{"description over cap", EntityText{Description: strings.Repeat("a", MaxEntityDescriptionLen+1)}, []string{"description"}},
		{"description lone CR", EntityText{Description: "a\rb"}, []string{"description"}},
		{"description escape", EntityText{Description: "a\x1b[31mb"}, []string{"description"}},
		{"description NEL", EntityText{Description: "a\u0085b"}, []string{"description"}},
		{"both bad", EntityText{Name: "a\nb", Description: "\x07"}, []string{"name", "description"}},
	} {
		wantProblems(t, tc.name, tc.text.Validate(), tc.paths...)
	}
}

func TestValidEntityID(t *testing.T) {
	for id, want := range map[string]bool{
		"light.guest_room": true, "sensor.2nd_floor": true, "light": false, "light.": false, ".x": false,
		"Light.x": false, "light.x y": false, "light.x/../y": false, "light.x.y": false, "": false,
	} {
		if got := ValidEntityID(id); got != want {
			t.Errorf("ValidEntityID(%q) = %v, want %v", id, got, want)
		}
	}
}

// Templates.Validate checks every entry. Paths put the id in brackets,
// since it holds a dot.
func TestTemplatesValidateEntities(t *testing.T) {
	ok := Templates{Version: 1, Entities: map[string]EntityText{
		"light.guest_room": {Name: "Reading lamp"},
		// An entity HA does not know is fine.
		"light.gone": {Description: "Old lamp"},
	}}
	wantProblems(t, "ok", ok.Validate())
	bad := Templates{Version: 1, Entities: map[string]EntityText{
		"Light.Bad":        {Name: "x"},
		"light.guest_room": {Name: "a\nb"},
	}}
	wantProblems(t, "bad", bad.Validate(), "entities[Light.Bad]", "entities[light.guest_room].name")
}

// EntityText gives only good, non-empty entries, trimmed.
func TestTemplatesEntityText(t *testing.T) {
	ts := Templates{Entities: map[string]EntityText{
		"light.a":   {Name: "  Lamp  ", Description: " By the bed\n"},
		"light.b":   {Name: "a\x00b", Description: "fine"},
		"light.c":   {Name: "   "},
		"Light.Bad": {Name: "x"},
	}}
	if got, ok := ts.EntityText("light.a"); !ok || got != (EntityText{Name: "Lamp", Description: "By the bed"}) {
		t.Errorf("light.a = %+v, %v", got, ok)
	}
	for _, id := range []string{"light.b", "light.c", "Light.Bad", "light.none"} {
		if got, ok := ts.EntityText(id); ok {
			t.Errorf("%s = %+v, want none", id, got)
		}
	}
	if _, ok := (Templates{}).EntityText("light.a"); ok {
		t.Error("nil map gave text")
	}
}

// entities is left out when empty, and survives YAML and JSON.
func TestEntityTextRoundTrip(t *testing.T) {
	b, err := yaml.Marshal(Templates{Version: 1, Templates: []Template{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "entities") {
		t.Errorf("empty entities written: %s", b)
	}
	ts := Templates{Version: 1, Templates: []Template{}, Entities: map[string]EntityText{
		"light.guest_room": {Name: "Reading lamp", Description: "By the bed\nDimmable"},
		"switch.fan":       {Description: "Ceiling fan"},
	}}
	for _, tc := range []struct {
		name      string
		marshal   func(any) ([]byte, error)
		unmarshal func([]byte, any) error
	}{
		{"yaml", yaml.Marshal, yaml.Unmarshal},
		{"json", json.Marshal, json.Unmarshal},
	} {
		b, err := tc.marshal(ts)
		if err != nil {
			t.Fatal(err)
		}
		var got Templates
		if err := tc.unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Entities) != 2 || got.Entities["light.guest_room"] != ts.Entities["light.guest_room"] ||
			got.Entities["switch.fan"] != ts.Entities["switch.fan"] {
			t.Errorf("%s: %+v from %s", tc.name, got.Entities, b)
		}
		if strings.Contains(string(b), `name: ""`) || strings.Contains(string(b), `"name":""`) {
			t.Errorf("%s: empty name written: %s", tc.name, b)
		}
	}
}

// A file written before entities existed loads with none, and a file
// with them loads them.
func TestEntityTextYAMLShape(t *testing.T) {
	var old Templates
	if err := yaml.Unmarshal([]byte("version: 1\ntemplates: []\n"), &old); err != nil || old.Entities != nil {
		t.Errorf("old file: %+v, %v", old, err)
	}
	raw := `version: 1
templates: []
entities:
  light.guest_room:
    name: Reading lamp
    description: |-
      By the bed
      Dimmable
`
	var ts Templates
	if err := yaml.Unmarshal([]byte(raw), &ts); err != nil {
		t.Fatal(err)
	}
	if got := ts.Entities["light.guest_room"]; got.Name != "Reading lamp" || got.Description != "By the bed\nDimmable" {
		t.Errorf("entities = %+v", ts.Entities)
	}
	wantProblems(t, "shape", ts.Validate())
}
