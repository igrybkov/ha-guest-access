package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// Options are the app options from Supervisor's Configuration tab
// (/data/options.json), turned into the types the app uses. Saving them in
// Supervisor restarts the app, so they never change while it runs.
//
// guest_access/config.yaml holds the same defaults as DefaultOptions; a
// test keeps the two in step.
type Options struct {
	// Settings are the shared settings. RateLimits has its defaults filled.
	Settings model.Settings
	// PublicBaseURL is how guests reach the guest port, such as
	// https://guest.example.com. Empty means unknown: the admin API then
	// gives link paths only.
	PublicBaseURL string
	// LimitsEnabled is false when the user turned every rate limit off.
	// Not recommended; the app warns at each start.
	LimitsEnabled bool
	// MusicAssistantToken is the long-lived MA token. Never log it or
	// return it over the API.
	MusicAssistantToken string
	// MATimeout bounds every Music Assistant call.
	MATimeout time.Duration
	// NearTTL is how long a location check lasts.
	NearTTL time.Duration
	// MaxAccuracyM is the worst location accuracy the guest page takes.
	MaxAccuracyM int
	// PasswordWindowPerIP and PasswordWindowPerLink are the windows of
	// RateLimits.PasswordFailsPerIP and PasswordFailsPerLink.
	PasswordWindowPerIP   time.Duration
	PasswordWindowPerLink time.Duration
	// CustomSlugMinLen and CustomSlugMaxLen bound a slug the admin types.
	CustomSlugMinLen int
	CustomSlugMaxLen int
	// GuestBodyBytes and AdminBodyBytes cap JSON request bodies.
	GuestBodyBytes int64
	AdminBodyBytes int64
	// DiagRequests is how many guest requests the diagnostics keep.
	DiagRequests int
	// InventoryMaxAge is how long a registry snapshot is served without a
	// registry event.
	InventoryMaxAge time.Duration
	// Approval tunes device approval.
	Approval ApprovalOptions
}

// ApprovalOptions tune device approval.
type ApprovalOptions struct {
	PendingTTL    time.Duration // how long a device request waits
	MaxPending    int           // pending requests per link
	MaxDevices    int           // approved devices per link
	EventsPerHour int           // device request events per link per hour
	TouchThrottle time.Duration // least time between LastSeenAt writes
}

// rawOptions is options.json as Supervisor writes it. Its shape is the
// options and schema in guest_access/config.yaml. Optional keys
// (public_base_url, music_assistant url and token, guest welcome and
// accent_color) are left out when not set.
type rawOptions struct {
	LogLevel           string                    `json:"log_level"`
	PublicBaseURL      string                    `json:"public_base_url"`
	LocalAccess        model.LocalAccessSettings `json:"local_access"`
	TrustedProxies     []string                  `json:"trusted_proxies"`
	HomeRadiusDefaultM int                       `json:"home_radius_default_m"`
	MusicAssistant     struct {
		URL       string `json:"url"`
		Token     string `json:"token"`
		TimeoutMS int    `json:"timeout_ms"`
	} `json:"music_assistant"`
	Guest    model.GuestSettings `json:"guest"`
	Location struct {
		TTLMin       int `json:"ttl_min"`
		MaxAccuracyM int `json:"max_accuracy_m"`
	} `json:"location"`
	RateLimits struct {
		Enabled bool `json:"enabled"`
		model.RateLimits
		PasswordWindowPerIPMin   int `json:"password_window_per_ip_min"`
		PasswordWindowPerLinkMin int `json:"password_window_per_link_min"`
	} `json:"rate_limits"`
	Links struct {
		CustomSlugMinLen int `json:"custom_slug_min_len"`
		CustomSlugMaxLen int `json:"custom_slug_max_len"`
	} `json:"links"`
	HTTP struct {
		GuestBodyKiB int `json:"guest_body_kib"`
		AdminBodyKiB int `json:"admin_body_kib"`
	} `json:"http"`
	Diagnostics struct {
		Requests int `json:"requests"`
	} `json:"diagnostics"`
	Inventory struct {
		MaxAgeS int `json:"max_age_s"`
	} `json:"inventory"`
	Approval struct {
		PendingTTLMin    int `json:"pending_ttl_min"`
		MaxPending       int `json:"max_pending"`
		MaxDevices       int `json:"max_devices"`
		EventsPerHour    int `json:"events_per_hour"`
		TouchThrottleMin int `json:"touch_throttle_min"`
	} `json:"approval"`
}

// defaultRaw returns the defaults, as in guest_access/config.yaml.
func defaultRaw() rawOptions {
	var r rawOptions
	d := model.DefaultSettings()
	r.LogLevel = "info"
	r.LocalAccess = d.LocalAccess
	r.TrustedProxies = []string{}
	r.HomeRadiusDefaultM = d.HomeRadiusDefaultM
	r.MusicAssistant.TimeoutMS = 2000
	r.Guest = d.Guest
	r.Location.TTLMin = 15
	r.Location.MaxAccuracyM = 1000
	r.RateLimits.Enabled = true
	r.RateLimits.RateLimits = model.DefaultRateLimits()
	r.RateLimits.PasswordWindowPerIPMin = 15
	r.RateLimits.PasswordWindowPerLinkMin = 60
	r.Links.CustomSlugMinLen = 12
	r.Links.CustomSlugMaxLen = 64
	r.HTTP.GuestBodyKiB = 16
	r.HTTP.AdminBodyKiB = 1024
	r.Diagnostics.Requests = 20
	r.Inventory.MaxAgeS = 5
	r.Approval.PendingTTLMin = 10
	r.Approval.MaxPending = 3
	r.Approval.MaxDevices = 10
	r.Approval.EventsPerHour = 5
	r.Approval.TouchThrottleMin = 5
	return r
}

// DefaultOptions returns the options of a new install.
func DefaultOptions() Options {
	o, err := defaultRaw().options()
	if err != nil {
		panic("config: defaults do not validate: " + err.Error())
	}
	return o
}

// parseOptions reads options.json. Keys left out keep their defaults, so
// a dev options.json may hold only log_level. Unknown keys are an error:
// they mean config.yaml and this file disagree, or a typo.
func parseOptions(raw []byte) (rawOptions, error) {
	r := defaultRaw()
	if raw == nil {
		return r, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return rawOptions{}, err
	}
	return r, nil
}

// options checks r and builds Options. Every problem is listed.
func (r rawOptions) options() (Options, error) {
	s := model.Settings{
		Version:            1,
		LocalAccess:        r.LocalAccess,
		TrustedProxies:     r.TrustedProxies,
		HomeRadiusDefaultM: r.HomeRadiusDefaultM,
		Guest:              r.Guest,
		RateLimits:         r.RateLimits.RateLimits.WithDefaults(),
	}
	s.MusicAssistant.URL = r.MusicAssistant.URL
	if s.LocalAccess.Networks == nil {
		s.LocalAccess.Networks = []string{}
	}
	if s.TrustedProxies == nil {
		s.TrustedProxies = []string{}
	}
	var errs []error
	if err := s.Validate(); err != nil {
		errs = append(errs, err)
	}
	if u := r.PublicBaseURL; u != "" {
		pu, err := url.Parse(u)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" || pu.User != nil || pu.RawQuery != "" || pu.Fragment != "" {
			errs = append(errs, errors.New("public_base_url: must be an http or https URL such as https://guest.example.com"))
		}
	}
	for _, f := range []struct {
		name    string
		v, lo   int
		hi      int
		comment string
	}{
		{"music_assistant.timeout_ms", r.MusicAssistant.TimeoutMS, 100, 60_000, ""},
		{"location.ttl_min", r.Location.TTLMin, 1, 24 * 60, ""},
		{"location.max_accuracy_m", r.Location.MaxAccuracyM, 1, 100_000, ""},
		{"rate_limits.password_window_per_ip_min", r.RateLimits.PasswordWindowPerIPMin, 1, 7 * 24 * 60, ""},
		{"rate_limits.password_window_per_link_min", r.RateLimits.PasswordWindowPerLinkMin, 1, 7 * 24 * 60, ""},
		{"links.custom_slug_min_len", r.Links.CustomSlugMinLen, 12, 128, "stored slugs are 12 to 128 characters"},
		{"links.custom_slug_max_len", r.Links.CustomSlugMaxLen, max(12, r.Links.CustomSlugMinLen), 128, "and at least custom_slug_min_len"},
		{"http.guest_body_kib", r.HTTP.GuestBodyKiB, 1, 1024, ""},
		{"http.admin_body_kib", r.HTTP.AdminBodyKiB, 16, 16 * 1024, ""},
		{"diagnostics.requests", r.Diagnostics.Requests, 1, 1000, ""},
		{"inventory.max_age_s", r.Inventory.MaxAgeS, 1, 3600, ""},
		{"approval.pending_ttl_min", r.Approval.PendingTTLMin, 1, 24 * 60, ""},
		{"approval.max_pending", r.Approval.MaxPending, 1, 100, ""},
		{"approval.max_devices", r.Approval.MaxDevices, 1, 1000, ""},
		{"approval.events_per_hour", r.Approval.EventsPerHour, 1, 1000, ""},
		{"approval.touch_throttle_min", r.Approval.TouchThrottleMin, 1, 24 * 60, ""},
	} {
		if f.v < f.lo || f.v > f.hi {
			msg := fmt.Sprintf("%s: must be between %d and %d", f.name, f.lo, f.hi)
			if f.comment != "" {
				msg += " (" + f.comment + ")"
			}
			errs = append(errs, errors.New(msg))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Options{}, err
	}
	minutes := func(n int) time.Duration { return time.Duration(n) * time.Minute }
	return Options{
		Settings:              s,
		PublicBaseURL:         r.PublicBaseURL,
		LimitsEnabled:         r.RateLimits.Enabled,
		MusicAssistantToken:   r.MusicAssistant.Token,
		MATimeout:             time.Duration(r.MusicAssistant.TimeoutMS) * time.Millisecond,
		NearTTL:               minutes(r.Location.TTLMin),
		MaxAccuracyM:          r.Location.MaxAccuracyM,
		PasswordWindowPerIP:   minutes(r.RateLimits.PasswordWindowPerIPMin),
		PasswordWindowPerLink: minutes(r.RateLimits.PasswordWindowPerLinkMin),
		CustomSlugMinLen:      r.Links.CustomSlugMinLen,
		CustomSlugMaxLen:      r.Links.CustomSlugMaxLen,
		GuestBodyBytes:        int64(r.HTTP.GuestBodyKiB) << 10,
		AdminBodyBytes:        int64(r.HTTP.AdminBodyKiB) << 10,
		DiagRequests:          r.Diagnostics.Requests,
		InventoryMaxAge:       time.Duration(r.Inventory.MaxAgeS) * time.Second,
		Approval: ApprovalOptions{
			PendingTTL:    minutes(r.Approval.PendingTTLMin),
			MaxPending:    r.Approval.MaxPending,
			MaxDevices:    r.Approval.MaxDevices,
			EventsPerHour: r.Approval.EventsPerHour,
			TouchThrottle: minutes(r.Approval.TouchThrottleMin),
		},
	}, nil
}

// MusicAssistantEnabled reports whether the app may call Music Assistant:
// both the URL and the token are set.
func (o Options) MusicAssistantEnabled() bool {
	return o.Settings.MusicAssistant.URL != "" && o.MusicAssistantToken != ""
}
