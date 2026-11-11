package main

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminHealthz(t *testing.T) {
	srv := httptest.NewServer(adminHandler())
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err := checkHealth(":" + port); err != nil {
		t.Fatalf("checkHealth: %v", err)
	}
}

// The guest port must not serve admin routes, the health check included.
func TestGuestHasNoAdminRoutes(t *testing.T) {
	var buf bytes.Buffer
	h := guestHandler(slog.New(slog.NewTextHandler(&buf, nil)))
	for _, path := range []string{"/", "/healthz", "/api/links"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s = %d, want 503", path, rec.Code)
		}
	}
}

func TestGuestLogsForwardHeaders(t *testing.T) {
	var buf bytes.Buffer
	h := guestHandler(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("CF-Connecting-IP", "203.0.113.7")
	h.ServeHTTP(httptest.NewRecorder(), req)
	for _, want := range []string{"peer=", "X-Forwarded-For=203.0.113.7", "Cf-Connecting-Ip=203.0.113.7"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log %q has no %q", buf.String(), want)
		}
	}
}

// At info the guest port logs nothing per request.
func TestGuestQuietAtInfo(t *testing.T) {
	var buf bytes.Buffer
	h := guestHandler(slog.New(slog.NewTextHandler(&buf, nil)))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if buf.Len() != 0 {
		t.Errorf("logged at info: %q", buf.String())
	}
}

func TestLogLevel(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "options.json"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("LOG_LEVEL", "")
	if l, err := logLevel(dir); err != nil || l != slog.LevelInfo {
		t.Errorf("no file: %v, %v; want info", l, err)
	}
	write(`{"log_level":"debug"}`)
	if l, err := logLevel(dir); err != nil || l != slog.LevelDebug {
		t.Errorf("options debug: %v, %v", l, err)
	}
	t.Setenv("LOG_LEVEL", "WARN")
	if l, err := logLevel(dir); err != nil || l != slog.LevelWarn {
		t.Errorf("env wins: %v, %v", l, err)
	}
	t.Setenv("LOG_LEVEL", "loud")
	if _, err := logLevel(dir); err == nil {
		t.Error("bad level: no error")
	}
	t.Setenv("LOG_LEVEL", "")
	write(`{`)
	if _, err := logLevel(dir); err == nil {
		t.Error("bad json: no error")
	}
}
