package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// A hand-written templates.yaml with nested aliases ("billion laughs") in a
// field that decodes into data (script variables) is read with a bounded
// cost and either loads or errors. It does not hang the app or eat memory.
func TestSecYAMLAliasBombIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("version: 1\ntemplates:\n- id: bomb\n  name: Bomb\n  rules:\n  - select: {entities: [script.x]}\n    actions: [script.run]\n    variables:\n")
	b.WriteString("      a0: &a0 [lol,lol,lol,lol,lol,lol,lol,lol,lol]\n")
	for i := 1; i <= 9; i++ {
		refs := make([]string, 9)
		for j := range refs {
			refs[j] = fmt.Sprintf("*a%d", i-1)
		}
		fmt.Fprintf(&b, "      a%d: &a%d [%s]\n", i, i, strings.Join(refs, ","))
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "templates.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	f := Open(path, func() model.Templates { return model.Templates{Version: 1} })
	done := make(chan error, 1)
	go func() { _, err := f.Load(); done <- err }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("alias bomb took over 10 s to load")
	}
}
