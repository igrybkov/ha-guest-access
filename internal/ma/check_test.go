package ma

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// recordingMA is an MA that records each command and answers auth/me
// with a user record, or with status when it is set.
type recordingMA struct {
	mu       sync.Mutex
	commands []string
	status   int
}

func (m *recordingMA) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			Command string `json:"command"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		m.mu.Lock()
		m.commands = append(m.commands, msg.Command)
		status := m.status
		m.mu.Unlock()
		if status != 0 {
			http.Error(w, "refused", status)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user_id":"u1","username":"guest-access","role":"user"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (m *recordingMA) sent() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.commands...)
}

// CheckToken sends the read-only auth/me, never party/url, and reports
// a refused token as ErrUnauthorized.
func TestCheckToken(t *testing.T) {
	m := &recordingMA{}
	base := m.serve(t)
	if err := client(t, base, token).CheckToken(context.Background()); err != nil {
		t.Fatalf("good token: %v", err)
	}
	if err := client(t, base, "wrong").CheckToken(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("bad token: %v", err)
	}
	m.mu.Lock()
	m.status = http.StatusForbidden
	m.mu.Unlock()
	if err := client(t, base, token).CheckToken(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("403: %v", err)
	}
	m.mu.Lock()
	m.status = http.StatusServiceUnavailable
	m.mu.Unlock()
	if err := client(t, base, token).CheckToken(context.Background()); errors.Is(err, ErrUnauthorized) || !errors.Is(err, ErrUnavailable) {
		t.Errorf("503: %v", err)
	}
	for _, c := range m.sent() {
		if c != tokenCheckCommand {
			t.Errorf("sent %q, want only %q", c, tokenCheckCommand)
		}
	}
	if tokenCheckCommand != "auth/me" {
		t.Errorf("token check command = %q", tokenCheckCommand)
	}
}
