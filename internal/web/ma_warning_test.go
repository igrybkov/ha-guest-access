package web

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ma"
)

// switchMA is an ma.Client whose answer the test changes.
type switchMA struct {
	mu  sync.Mutex
	err error
}

func (m *switchMA) set(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *switchMA) PartyURL(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	return "https://ma.example/party?code=x", nil
}

// A token MA refuses shows as a warning in the settings and the
// diagnostics, and in the log once. A working call clears it. MA being
// down is not a token problem.
func TestMATokenRefusedWarning(t *testing.T) {
	log := &lockedBuffer{}
	stub := &switchMA{}
	h := newHarness(t, withMA(stub), withLog(log))
	l := h.link("music")
	party := func() int { return h.g(req{method: "GET", path: "/g/" + l.Slug + "/party"}).Code }
	refused := func() (bool, bool) {
		s := decode[settingsView](t, h.a(req{method: "GET", path: "/api/settings"}))
		d := decode[diagnosticsView](t, h.a(req{method: "GET", path: "/api/diagnostics"}))
		inDiag := false
		for _, w := range d.Warnings {
			inDiag = inDiag || w == maRefusedWarning
		}
		return s.MAToken.Refused, inDiag
	}

	if s, d := refused(); s || d {
		t.Fatalf("warning before any call: settings %v, diagnostics %v", s, d)
	}
	stub.set(ma.ErrUnavailable)
	party()
	if s, d := refused(); s || d {
		t.Errorf("MA down counts as a refused token: %v %v", s, d)
	}
	stub.set(ma.ErrUnauthorized)
	for range 3 {
		if c := party(); c != 503 {
			t.Fatalf("refused token: party %d", c)
		}
	}
	if s, d := refused(); !s || !d {
		t.Errorf("refused token: settings %v, diagnostics %v", s, d)
	}
	if n := strings.Count(log.String(), "refused the token"); n != 1 {
		t.Errorf("logged the refusal %d times, want 1", n)
	}
	stub.set(nil)
	if c := party(); c != 200 {
		t.Fatalf("working token: party %d", c)
	}
	if s, d := refused(); s || d {
		t.Errorf("warning stays after a working call: %v %v", s, d)
	}
}

// withLog sends the server's log to buf.
func withLog(buf *lockedBuffer) harnessOpt {
	return func(_ *config.Options, d *Deps) { d.Logger = slog.New(slog.NewTextHandler(buf, nil)) }
}
