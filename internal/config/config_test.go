package config

import (
	"errors"
	"io/fs"
	"log/slog"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func files(m map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if v, ok := m[p]; ok {
			return []byte(v), nil
		}
		return nil, fs.ErrNotExist
	}
}

func TestLoadSupervisor(t *testing.T) {
	c, err := load(
		env(map[string]string{"SUPERVISOR_TOKEN": "tok", "HA_URL": "https://ignored", "HA_TOKEN": "ignored", "DEV_ADMIN_ANY_PEER": "1"}),
		files(map[string]string{"/data/options.json": `{"log_level":"debug"}`}),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		LogLevel: "debug", DataDir: "/data", ConfigDir: "/homeassistant/guest_access",
		Listen: ":8099", GuestListen: ":8888",
		HAURL: "ws://supervisor/core/websocket", HAToken: "tok", Supervisor: true,
	}
	if c != want {
		t.Fatalf("got %+v\nwant %+v", c, want)
	}
}

func TestLoadDevMode(t *testing.T) {
	c, err := load(env(map[string]string{
		"HA_URL": "https://hass.example/", "HA_TOKEN": "t",
		"DATA_DIR": "/tmp/d", "CONFIG_DIR": "/tmp/c", "LISTEN": "127.0.0.1:9000", "GUEST_LISTEN": "127.0.0.1:9001",
		"DEV_ADMIN_ANY_PEER": "1",
	}), files(map[string]string{"/tmp/d/options.json": `{"log_level":"warn"}`}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		LogLevel: "warn", DataDir: "/tmp/d", ConfigDir: "/tmp/c",
		Listen: "127.0.0.1:9000", GuestListen: "127.0.0.1:9001",
		HAURL: "wss://hass.example/api/websocket", HAToken: "t", DevAdminAnyPeer: true,
	}
	if c != want {
		t.Fatalf("got %+v\nwant %+v", c, want)
	}
}

func TestLoadErrors(t *testing.T) {
	sup := map[string]string{"SUPERVISOR_TOKEN": "tok"}
	for _, tc := range []struct {
		name  string
		env   map[string]string
		files map[string]string
	}{
		{"no HA outside Supervisor", map[string]string{}, nil},
		{"no token outside Supervisor", map[string]string{"HA_URL": "https://h"}, nil},
		{"bad HA_URL", map[string]string{"HA_URL": "ftp://h", "HA_TOKEN": "t"}, nil},
		{"bad options JSON", sup, map[string]string{"/data/options.json": `{`}},
		{"bad level in options", sup, map[string]string{"/data/options.json": `{"log_level":"loud"}`}},
		{"bad level in env", map[string]string{"SUPERVISOR_TOKEN": "t", "LOG_LEVEL": "loud"}, nil},
	} {
		if _, err := load(env(tc.env), files(tc.files)); err == nil {
			t.Errorf("%s: no error", tc.name)
		}
	}
	readErr := func(string) ([]byte, error) { return nil, errors.New("disk on fire") }
	if _, err := load(env(sup), readErr); err == nil {
		t.Error("unreadable options.json: no error")
	}
}

// The same cases as the skeleton's logLevel in main, so main can switch over.
func TestLogLevel(t *testing.T) {
	const opts = "/data/options.json"
	for _, tc := range []struct {
		name  string
		env   string
		file  string
		want  slog.Level
		level string
	}{
		{"no file", "", "", slog.LevelInfo, "info"},
		{"options debug", "", `{"log_level":"debug"}`, slog.LevelDebug, "debug"},
		{"env wins, any case", "WARN", `{"log_level":"debug"}`, slog.LevelWarn, "warn"},
		{"error", "error", "", slog.LevelError, "error"},
		{"empty in options", "", `{"log_level":""}`, slog.LevelInfo, "info"},
	} {
		fs := map[string]string{}
		if tc.file != "" {
			fs[opts] = tc.file
		}
		c, err := load(env(map[string]string{"SUPERVISOR_TOKEN": "t", "LOG_LEVEL": tc.env}), files(fs))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if c.LogLevel != tc.level || c.SlogLevel() != tc.want {
			t.Errorf("%s: %q / %v, want %q / %v", tc.name, c.LogLevel, c.SlogLevel(), tc.level, tc.want)
		}
	}
}
