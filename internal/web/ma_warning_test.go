package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func (m *switchMA) CheckToken(ctx context.Context) error {
	_, err := m.PartyURL(ctx)
	return err
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

// countMA counts token checks and party calls, and answers with
// switchMA.
type countMA struct {
	switchMA
	n     atomic.Int32 // token checks
	party atomic.Int32 // PartyURL calls
}

func (m *countMA) CheckToken(ctx context.Context) error {
	m.n.Add(1)
	return m.switchMA.CheckToken(ctx)
}

func (m *countMA) PartyURL(ctx context.Context) (string, error) {
	m.party.Add(1)
	return m.switchMA.PartyURL(ctx)
}

// The token check runs at start and on its interval, and its result sets
// and clears the refused-token warning with no guest call.
func TestMATokenCheck(t *testing.T) {
	stub := &countMA{}
	stub.set(ma.ErrUnauthorized)
	h := newHarness(t, withMA(stub), func(o *config.Options, _ *Deps) { o.MACheckInterval = 20 * time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.srv.RunMACheck(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitUntil(t, func() bool { return h.srv.maRefused.Load() })
	stub.set(nil)
	waitUntil(t, func() bool { return !h.srv.maRefused.Load() })
	// MA down is not a token problem.
	stub.set(ma.ErrUnavailable)
	n := stub.n.Load()
	waitUntil(t, func() bool { return stub.n.Load() > n+2 })
	if h.srv.maRefused.Load() {
		t.Error("MA down set the refused warning")
	}
}

// With the interval at zero the check runs once, at start.
func TestMATokenCheckOnce(t *testing.T) {
	stub := &countMA{}
	h := newHarness(t, withMA(stub), func(o *config.Options, _ *Deps) { o.MACheckInterval = 0 })
	done := make(chan struct{})
	go func() { h.srv.RunMACheck(t.Context()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunMACheck with no interval did not return")
	}
	if n := stub.n.Load(); n != 1 {
		t.Errorf("%d checks, want 1", n)
	}
	if n := stub.party.Load(); n != 0 {
		t.Errorf("the token check called party/url %d times", n)
	}
}

// Without Music Assistant set up there is no check and no call.
func TestMATokenCheckOff(t *testing.T) {
	stub := &countMA{}
	h := newHarness(t, func(_ *config.Options, d *Deps) {
		d.NewMA = func(string, string, time.Duration) (ma.Client, error) { return stub, nil }
	})
	h.srv.RunMACheck(t.Context())
	if n := stub.n.Load(); n != 0 {
		t.Errorf("%d calls with MA off", n)
	}
}

// The token check, through the real MA client against an httptest MA,
// sends only auth/me. A 401 marks the token refused and a later success
// clears the mark. party/url is never sent.
func TestMATokenCheckOverHTTP(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	status := http.StatusUnauthorized
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			Command string `json:"command"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		mu.Lock()
		sent = append(sent, msg.Command)
		code := status
		mu.Unlock()
		if code != http.StatusOK {
			http.Error(w, "Authentication required", code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user_id":"u1"}`))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, func(o *config.Options, d *Deps) {
		o.Settings.MusicAssistant.URL = srv.URL
		o.MusicAssistantToken = "tok"
		o.MACheckInterval = 20 * time.Millisecond
		d.NewMA = nil // the real HTTP client
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.srv.RunMACheck(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitUntil(t, func() bool { return h.srv.maRefused.Load() })
	mu.Lock()
	status = http.StatusOK
	mu.Unlock()
	waitUntil(t, func() bool { return !h.srv.maRefused.Load() })
	mu.Lock()
	defer mu.Unlock()
	for _, c := range sent {
		if c != "auth/me" {
			t.Errorf("the token check sent %q", c)
		}
	}
}
