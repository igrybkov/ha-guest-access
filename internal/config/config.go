// Package config loads the process config (env and /data/options.json),
// the HMAC key, and serves the YAML files of both sets as typed stores:
// settings and templates in the config dir, secrets in the data dir.
// Links have their own store in package links.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/igrybkov/ha-guest-access/internal/ha"
)

// Config is the process configuration.
type Config struct {
	LogLevel    string // debug | info | warn | error
	DataDir     string // links, secrets, HMAC key, options.json; default /data
	ConfigDir   string // templates.yaml and settings.yaml; default /homeassistant/guest_access
	Listen      string // admin port; default :8099
	GuestListen string // guest port; default :8888
	HAURL       string // websocket URL
	HAToken     string
	Supervisor  bool // running under Supervisor (SUPERVISOR_TOKEN set)
	// DevAdminAnyPeer lets any peer reach the admin port, for make run-dev.
	// It is always false under Supervisor, where only the ingress proxy may
	// reach it.
	DevAdminAnyPeer bool
}

const (
	defaultDataDir   = "/data"
	defaultConfigDir = "/homeassistant/guest_access"
	// DefaultListen is the admin port. Ingress and the watchdog reach the
	// app here. The healthcheck reads its port.
	DefaultListen = ":8099"
	// DefaultGuestListen is the guest port. Supervisor maps it to a host
	// port.
	DefaultGuestListen = ":8888"
)

// options is /data/options.json, written by Supervisor from the app options.
type options struct {
	LogLevel string `json:"log_level"`
}

// Load reads env (HA_URL, HA_TOKEN, SUPERVISOR_TOKEN, DATA_DIR, CONFIG_DIR,
// LISTEN, GUEST_LISTEN, LOG_LEVEL, DEV_ADMIN_ANY_PEER) and
// <DataDir>/options.json (log_level). A missing options.json is fine.
//
// Precedence: env, then options.json, then defaults. Env wins so a dev run
// can reuse a copied options.json.
//
// Under Supervisor (SUPERVISOR_TOKEN set) the HA websocket is the
// Supervisor proxy, and HA_URL, HA_TOKEN and DEV_ADMIN_ANY_PEER are
// ignored. Outside it, HA_URL and HA_TOKEN are required. HA_URL may be the
// HA base URL (https://hass.example) or a full websocket URL.
func Load() (Config, error) { return load(os.Getenv, os.ReadFile) }

func load(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	c := Config{
		DataDir:     firstNonEmpty(getenv("DATA_DIR"), defaultDataDir),
		ConfigDir:   firstNonEmpty(getenv("CONFIG_DIR"), defaultConfigDir),
		Listen:      firstNonEmpty(getenv("LISTEN"), DefaultListen),
		GuestListen: firstNonEmpty(getenv("GUEST_LISTEN"), DefaultGuestListen),
	}

	var opts options
	optsPath := filepath.Join(c.DataDir, "options.json")
	raw, err := readFile(optsPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return Config{}, fmt.Errorf("read %s: %w", optsPath, err)
	default:
		if err := json.Unmarshal(raw, &opts); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", optsPath, err)
		}
	}

	c.LogLevel = strings.ToLower(firstNonEmpty(getenv("LOG_LEVEL"), opts.LogLevel, "info"))
	if !slices.Contains([]string{"debug", "info", "warn", "error"}, c.LogLevel) {
		return Config{}, fmt.Errorf("log level %q is not one of debug, info, warn, error", c.LogLevel)
	}

	if tok := getenv("SUPERVISOR_TOKEN"); tok != "" {
		c.Supervisor = true
		c.HAURL = ha.SupervisorURL
		c.HAToken = tok
		return c, nil
	}
	c.DevAdminAnyPeer = getenv("DEV_ADMIN_ANY_PEER") == "1"
	c.HAToken = getenv("HA_TOKEN")
	if getenv("HA_URL") == "" || c.HAToken == "" {
		return Config{}, errors.New("outside Supervisor, HA_URL and HA_TOKEN are required")
	}
	c.HAURL, err = ha.WebsocketURL(getenv("HA_URL"))
	if err != nil {
		return Config{}, err
	}
	return c, nil
}

// SlogLevel returns the slog level for c.LogLevel.
func (c Config) SlogLevel() slog.Level {
	switch c.LogLevel {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
