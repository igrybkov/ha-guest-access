package main

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
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
	h := guestHandler(slog.New(slog.NewTextHandler(&buf, nil)))
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
