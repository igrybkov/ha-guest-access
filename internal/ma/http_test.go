package ma

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const token = "ma-token-123"

// server answers like MA's POST /api, after checking the request shape.
func server(t *testing.T, reply func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return
		}
		var msg struct {
			MessageID string         `json:"message_id"`
			Command   string         `json:"command"`
			Args      map[string]any `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil || msg.Command != "party/url" || msg.MessageID == "" || msg.Args == nil {
			http.Error(w, "bad command", http.StatusBadRequest)
			return
		}
		reply(w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jsonReply(v string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(v))
	}
}

func client(t *testing.T, base, tok string) *HTTPClient {
	t.Helper()
	c, err := New(base, tok)
	if err != nil || c == nil {
		t.Fatalf("New(%q) = %v, %v", base, c, err)
	}
	return c.(*HTTPClient)
}

func TestPartyURL(t *testing.T) {
	srv := server(t, jsonReply(`"https://ma.example/guest?join=ABC123"`))
	for _, base := range []string{srv.URL, srv.URL + "/"} {
		got, err := client(t, base, token).PartyURL(context.Background())
		if err != nil || got != "https://ma.example/guest?join=ABC123" {
			t.Errorf("%s: %q, %v", base, got, err)
		}
	}
}

func TestPartyURLFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reply  func(http.ResponseWriter)
		tok    string
		unauth bool
	}{
		{"guest access off", jsonReply(`null`), token, false},
		{"empty string", jsonReply(`""`), token, false},
		{"non-JSON", jsonReply(`<html>oops</html>`), token, false},
		{"object", jsonReply(`{"url":"https://x"}`), token, false},
		{"number", jsonReply(`42`), token, false},
		{"javascript URL", jsonReply(`"javascript:alert(1)"`), token, false},
		{"JAVASCRIPT URL", jsonReply(`"JavaScript:alert(1)"`), token, false},
		{"data URL", jsonReply(`"data:text/html,hi"`), token, false},
		{"relative URL", jsonReply(`"/guest?join=x"`), token, false},
		{"no host", jsonReply(`"https:///x"`), token, false},
		{"credentials", jsonReply(`"https://u:p@ma.example/x"`), token, false},
		{"quote in URL", jsonReply(`"https://ma.example/\"onload=x"`), token, false},
		{"huge body", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`"https://ma.example/` + strings.Repeat("a", maxResponse) + `"`))
		}, token, false},
		{"500", func(w http.ResponseWriter) { http.Error(w, "Internal server error", 500) }, token, false},
		{"redirect", func(w http.ResponseWriter) {
			w.Header().Set("Location", "https://evil.example/api")
			w.WriteHeader(http.StatusTemporaryRedirect)
		}, token, false},
		{"401", jsonReply(`"https://x"`), "wrong", true},
	} {
		srv := server(t, tc.reply)
		got, err := client(t, srv.URL, tc.tok).PartyURL(context.Background())
		if !errors.Is(err, ErrUnavailable) || got != "" {
			t.Errorf("%s: %q, %v; want ErrUnavailable", tc.name, got, err)
		}
		if errors.Is(err, ErrUnauthorized) != tc.unauth {
			t.Errorf("%s: ErrUnauthorized = %v", tc.name, !tc.unauth)
		}
		if err != nil && strings.Contains(err.Error(), token) {
			t.Errorf("%s: error has the token: %v", tc.name, err)
		}
	}
}

// A redirect is never followed, so the token never reaches another host.
func TestNoRedirectFollow(t *testing.T) {
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer other.Close()
	srv := server(t, func(w http.ResponseWriter) {
		w.Header().Set("Location", other.URL+"/api")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	if _, err := client(t, srv.URL, token).PartyURL(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
	if hit {
		t.Error("redirect followed")
	}
}

func TestTimeout(t *testing.T) {
	if Timeout != 2*time.Second {
		t.Errorf("Timeout = %v", Timeout)
	}
	release := make(chan struct{})
	srv := server(t, func(w http.ResponseWriter) { <-release })
	defer close(release)
	c := client(t, srv.URL, token)
	c.timeout = 50 * time.Millisecond
	start := time.Now()
	if _, err := c.PartyURL(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v", d)
	}
}

func TestNewWithTimeout(t *testing.T) {
	for _, tc := range []struct {
		in, want time.Duration
	}{{0, Timeout}, {-time.Second, Timeout}, {300 * time.Millisecond, 300 * time.Millisecond}} {
		c, err := NewWithTimeout("http://ma:8095", token, tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.(*HTTPClient).timeout; got != tc.want {
			t.Errorf("timeout %v: got %v, want %v", tc.in, got, tc.want)
		}
	}
	if c, err := NewWithTimeout("", token, time.Second); c != nil || err != nil {
		t.Errorf("no url: %v, %v", c, err)
	}
}

func TestNewEmptyOrBadConfig(t *testing.T) {
	for _, tc := range [][2]string{{"", ""}, {"http://ma:8095", ""}, {"", token}} {
		c, err := New(tc[0], tc[1])
		if c != nil || err != nil {
			t.Errorf("New(%q, %q) = %v, %v; want nil, nil", tc[0], tc[1], c, err)
		}
	}
	for _, base := range []string{"ma:8095", "ftp://ma", "javascript:alert(1)", "http://", "http://u:p@ma", "://x"} {
		if c, err := New(base, token); err == nil || c != nil {
			t.Errorf("New(%q) = %v, %v; want an error", base, c, err)
		}
	}
}

func FuzzParsePartyURL(f *testing.F) {
	for _, s := range []string{`"https://ma/x?join=1"`, `null`, `"javascript:x"`, `""`, `{}`, `"http://a b"`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		s, err := parsePartyURL(raw)
		if err != nil {
			if !errors.Is(err, ErrUnavailable) || s != "" {
				t.Fatalf("%q: %q, %v", raw, s, err)
			}
			return
		}
		u, perr := url.Parse(s)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			t.Fatalf("%q gave %q, not an http(s) URL", raw, s)
		}
	})
}
