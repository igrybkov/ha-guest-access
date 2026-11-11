package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
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
	// MACheckInterval is how often the app checks that Music Assistant
	// takes the token, after the check at start. Zero means only at start.
	MACheckInterval time.Duration
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
	// CustomSlugWarnLen is the length under which the admin API warns that
	// a slug is short. It is at most access.RandomSlugLen, so a random
	// slug never gets the warning.
	CustomSlugWarnLen int
	// GuestBodyBytes and AdminBodyBytes cap JSON request bodies.
	GuestBodyBytes int64
	AdminBodyBytes int64
	// HTTPReadTimeout bounds how long both servers wait for one request,
	// headers and body. It does not end an open event stream.
	HTTPReadTimeout time.Duration
	// MaxHeaderBytes caps the request headers on both servers.
	MaxHeaderBytes int
	// DiagRequests is how many guest requests the diagnostics keep.
	DiagRequests int
	// InventoryMaxAge is how long a registry snapshot is served without a
	// registry event.
	InventoryMaxAge time.Duration
	// Approval tunes device approval.
	Approval ApprovalOptions
	// HACallTimeout bounds one service call to HA. HAEventTimeout bounds
	// one event the app fires.
	HACallTimeout  time.Duration
	HAEventTimeout time.Duration
	// StreamCheck is how often an open event stream checks its link,
	// password and device again. StreamHeartbeat is how often an idle
	// stream sends a comment, so proxies keep it open. StreamRetry is the
	// Retry-After for a link at its stream cap.
	StreamCheck     time.Duration
	StreamHeartbeat time.Duration
	StreamRetry     time.Duration
	// StreamMaxPerIP caps the open event streams of one client (an IPv6
	// client by its prefix) on one link, inside the per-link cap.
	StreamMaxPerIP int
	// AuditEnabled writes the audit file. The audit log line goes to the
	// app log either way. AuditMaxFileBytes is the size at which the file
	// is rotated.
	AuditEnabled      bool
	AuditMaxFileBytes int64
	// AuditMaxDenialsPerMin caps the guest denial records a minute, from
	// all clients together. Past it, one summary record counts the rest.
	// Admin, device and allowed records are never capped.
	AuditMaxDenialsPerMin int
	// NotFound is what a browser gets on the guest port for an unknown
	// path or link.
	NotFound NotFoundOptions
	// TLS sets HTTPS on the guest port. Off by default.
	TLS TLSOptions
}

// TLSOptions set HTTPS on the guest port.
type TLSOptions struct {
	// Enabled makes the guest port serve TLS only.
	Enabled bool
	// CertFile and KeyFile are file names in the ssl folder. They are
	// checked with SSLFileOK when Enabled is set.
	CertFile string
	KeyFile  string
	// SelfSignedLifetime is how long a self-signed certificate is valid.
	// The app makes one when the files are missing or broken.
	SelfSignedLifetime time.Duration
}

// SSLFileOK reports whether name is a path inside the ssl folder: not
// empty, not absolute, not the folder itself, without ".." that leaves
// the folder, and without a NUL byte.
func SSLFileOK(name string) bool {
	return filepath.IsLocal(name) && filepath.Clean(name) != "." && !strings.ContainsRune(name, 0)
}

// NotFoundOptions set the answer a browser gets on the guest port for
// the root, an unknown path or an unknown link.
type NotFoundOptions struct {
	// Redirect is the URL the browser is sent to. Empty shows the page.
	// It is checked with NotFoundRedirectOK; a bad value shows the page.
	Redirect string
	// Title and Message are the page text. Plain text.
	Title   string
	Message string
}

// Default text of the not-found page.
const (
	DefaultNotFoundTitle   = "This link doesn't work"
	DefaultNotFoundMessage = "Ask the person who sent it for a new one."
)

// NotFoundRedirectOK reports whether u can be the not-found redirect: an
// absolute http or https URL with a host and no user info.
func NotFoundRedirectOK(u string) bool {
	pu, err := url.Parse(u)
	return err == nil && (pu.Scheme == "http" || pu.Scheme == "https") && pu.Host != "" && pu.User == nil
}

// ApprovalOptions tune device approval.
type ApprovalOptions struct {
	PendingTTL    time.Duration // how long a device request waits
	MaxPending    int           // pending requests per link
	MaxDevices    int           // approved devices per link
	EventsPerHour int           // device request events per link per hour
	TouchThrottle time.Duration // least time between LastSeenAt writes
	// PersistentNotification creates an HA persistent notification for
	// each request that waits, and dismisses it when the request ends.
	PersistentNotification bool
	// AdminPoll is how often the admin page asks for pending requests
	// while its tab is visible.
	AdminPoll time.Duration
}

// rawOptions is options.json as Supervisor writes it. Its shape is the
// options and schema in guest_access/config.yaml. Optional keys
// (public_base_url, music_assistant url and token, guest welcome,
// accent_color and not_found_redirect) are left out when not set.
type rawOptions struct {
	LogLevel           string                    `json:"log_level"`
	SSL                bool                      `json:"ssl"`
	CertFile           string                    `json:"certfile"`
	KeyFile            string                    `json:"keyfile"`
	SSLSelfSignedDays  int                       `json:"ssl_self_signed_days"`
	PublicBaseURL      string                    `json:"public_base_url"`
	LocalAccess        model.LocalAccessSettings `json:"local_access"`
	TrustedProxies     []string                  `json:"trusted_proxies"`
	HomeRadiusDefaultM int                       `json:"home_radius_default_m"`
	MusicAssistant     struct {
		URL              string `json:"url"`
		Token            string `json:"token"`
		TimeoutMS        int    `json:"timeout_ms"`
		CheckIntervalMin int    `json:"check_interval_min"`
	} `json:"music_assistant"`
	Guest struct {
		model.GuestSettings
		NotFoundRedirect string `json:"not_found_redirect"`
		NotFoundTitle    string `json:"not_found_title"`
		NotFoundMessage  string `json:"not_found_message"`
	} `json:"guest"`
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
		CustomSlugMinLen  int `json:"custom_slug_min_len"`
		CustomSlugMaxLen  int `json:"custom_slug_max_len"`
		CustomSlugWarnLen int `json:"custom_slug_warn_len"`
	} `json:"links"`
	HTTP struct {
		GuestBodyKiB int `json:"guest_body_kib"`
		AdminBodyKiB int `json:"admin_body_kib"`
		ReadTimeoutS int `json:"read_timeout_s"`
		MaxHeaderKiB int `json:"max_header_kib"`
	} `json:"http"`
	Diagnostics struct {
		Requests int `json:"requests"`
	} `json:"diagnostics"`
	Inventory struct {
		MaxAgeS int `json:"max_age_s"`
	} `json:"inventory"`
	Approval struct {
		PendingTTLMin          int  `json:"pending_ttl_min"`
		MaxPending             int  `json:"max_pending"`
		MaxDevices             int  `json:"max_devices"`
		EventsPerHour          int  `json:"events_per_hour"`
		TouchThrottleMin       int  `json:"touch_throttle_min"`
		PersistentNotification bool `json:"persistent_notification"`
		AdminPollS             int  `json:"admin_poll_s"`
	} `json:"approval"`
	HA struct {
		CallTimeoutS  int `json:"call_timeout_s"`
		EventTimeoutS int `json:"event_timeout_s"`
	} `json:"ha"`
	Streams struct {
		RecheckS    int `json:"recheck_s"`
		HeartbeatS  int `json:"heartbeat_s"`
		RetryAfterS int `json:"retry_after_s"`
		MaxPerIP    int `json:"max_per_ip"`
	} `json:"streams"`
	Audit struct {
		Enabled          bool `json:"enabled"`
		MaxFileKiB       int  `json:"max_file_kib"`
		MaxDenialsPerMin int  `json:"max_denials_per_min"`
	} `json:"audit"`
}

// defaultRaw returns the defaults, as in guest_access/config.yaml.
func defaultRaw() rawOptions {
	var r rawOptions
	d := model.DefaultSettings()
	r.LogLevel = "info"
	r.CertFile = "fullchain.pem"
	r.KeyFile = "privkey.pem"
	r.SSLSelfSignedDays = 365
	r.LocalAccess = d.LocalAccess
	r.TrustedProxies = []string{}
	r.HomeRadiusDefaultM = d.HomeRadiusDefaultM
	r.MusicAssistant.TimeoutMS = 2000
	r.MusicAssistant.CheckIntervalMin = 60
	r.Guest.GuestSettings = d.Guest
	r.Guest.NotFoundTitle = DefaultNotFoundTitle
	r.Guest.NotFoundMessage = DefaultNotFoundMessage
	r.Location.TTLMin = 15
	r.Location.MaxAccuracyM = 1000
	r.RateLimits.Enabled = true
	r.RateLimits.RateLimits = model.DefaultRateLimits()
	r.RateLimits.PasswordWindowPerIPMin = 15
	r.RateLimits.PasswordWindowPerLinkMin = 60
	r.Links.CustomSlugMinLen = 12
	r.Links.CustomSlugMaxLen = 64
	r.Links.CustomSlugWarnLen = 16
	r.HTTP.GuestBodyKiB = 16
	r.HTTP.AdminBodyKiB = 1024
	r.HTTP.ReadTimeoutS = 30
	r.HTTP.MaxHeaderKiB = 64
	r.Diagnostics.Requests = 20
	r.Inventory.MaxAgeS = 5
	r.Approval.PendingTTLMin = 10
	r.Approval.MaxPending = 3
	r.Approval.MaxDevices = 10
	r.Approval.EventsPerHour = 5
	r.Approval.TouchThrottleMin = 5
	r.Approval.PersistentNotification = true
	r.Approval.AdminPollS = 5
	r.HA.CallTimeoutS = 10
	r.HA.EventTimeoutS = 5
	r.Streams.RecheckS = 5
	r.Streams.HeartbeatS = 25
	r.Streams.RetryAfterS = 30
	r.Streams.MaxPerIP = 3
	r.Audit.Enabled = true
	r.Audit.MaxFileKiB = 2048
	r.Audit.MaxDenialsPerMin = 120
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
		Guest:              r.Guest.GuestSettings,
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
		{"music_assistant.check_interval_min", r.MusicAssistant.CheckIntervalMin, 0, 7 * 24 * 60, "0 checks at start only"},
		{"location.ttl_min", r.Location.TTLMin, 1, 24 * 60, ""},
		{"location.max_accuracy_m", r.Location.MaxAccuracyM, 1, 100_000, ""},
		{"rate_limits.password_window_per_ip_min", r.RateLimits.PasswordWindowPerIPMin, 1, 7 * 24 * 60, ""},
		{"rate_limits.password_window_per_link_min", r.RateLimits.PasswordWindowPerLinkMin, 1, 7 * 24 * 60, ""},
		{"links.custom_slug_min_len", r.Links.CustomSlugMinLen, 12, 128, "stored slugs are 12 to 128 characters"},
		{"links.custom_slug_max_len", r.Links.CustomSlugMaxLen, max(12, r.Links.CustomSlugMinLen), 128, "and at least custom_slug_min_len"},
		{"links.custom_slug_warn_len", r.Links.CustomSlugWarnLen, 12, access.RandomSlugLen, "a random slug is 22 characters"},
		{"http.guest_body_kib", r.HTTP.GuestBodyKiB, 1, 1024, ""},
		{"http.admin_body_kib", r.HTTP.AdminBodyKiB, 16, 16 * 1024, ""},
		{"http.read_timeout_s", r.HTTP.ReadTimeoutS, 1, 3600, ""},
		{"http.max_header_kib", r.HTTP.MaxHeaderKiB, 4, 1024, ""},
		{"diagnostics.requests", r.Diagnostics.Requests, 1, 1000, ""},
		{"inventory.max_age_s", r.Inventory.MaxAgeS, 1, 3600, ""},
		{"approval.pending_ttl_min", r.Approval.PendingTTLMin, 1, 24 * 60, ""},
		{"approval.max_pending", r.Approval.MaxPending, 1, 100, ""},
		{"approval.max_devices", r.Approval.MaxDevices, 1, 1000, ""},
		{"approval.events_per_hour", r.Approval.EventsPerHour, 1, 1000, ""},
		{"approval.touch_throttle_min", r.Approval.TouchThrottleMin, 1, 24 * 60, ""},
		{"approval.admin_poll_s", r.Approval.AdminPollS, 1, 3600, ""},
		{"ha.call_timeout_s", r.HA.CallTimeoutS, 1, 300, ""},
		{"ha.event_timeout_s", r.HA.EventTimeoutS, 1, 300, ""},
		{"streams.recheck_s", r.Streams.RecheckS, 1, 300, ""},
		{"streams.heartbeat_s", r.Streams.HeartbeatS, 5, 600, ""},
		{"streams.retry_after_s", r.Streams.RetryAfterS, 1, 3600, ""},
		{"streams.max_per_ip", r.Streams.MaxPerIP, 1, 1000, ""},
		{"audit.max_file_kib", r.Audit.MaxFileKiB, 64, 64 << 10, ""},
		{"audit.max_denials_per_min", r.Audit.MaxDenialsPerMin, 1, 100_000, ""},
		{"ssl_self_signed_days", r.SSLSelfSignedDays, 1, 3650, ""},
	} {
		if f.v < f.lo || f.v > f.hi {
			msg := fmt.Sprintf("%s: must be between %d and %d", f.name, f.lo, f.hi)
			if f.comment != "" {
				msg += " (" + f.comment + ")"
			}
			errs = append(errs, errors.New(msg))
		}
	}
	// The file names count only with ssl on, so a stray value does not
	// stop an app that does not use them.
	if r.SSL {
		for _, f := range []struct{ name, v string }{{"certfile", r.CertFile}, {"keyfile", r.KeyFile}} {
			if !SSLFileOK(f.v) {
				errs = append(errs, fmt.Errorf("%s: must be a file name in /ssl, such as fullchain.pem, not %q", f.name, f.v))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Options{}, err
	}
	minutes := func(n int) time.Duration { return time.Duration(n) * time.Minute }
	seconds := func(n int) time.Duration { return time.Duration(n) * time.Second }
	return Options{
		Settings:              s,
		PublicBaseURL:         r.PublicBaseURL,
		LimitsEnabled:         r.RateLimits.Enabled,
		MusicAssistantToken:   r.MusicAssistant.Token,
		MATimeout:             time.Duration(r.MusicAssistant.TimeoutMS) * time.Millisecond,
		MACheckInterval:       minutes(r.MusicAssistant.CheckIntervalMin),
		NearTTL:               minutes(r.Location.TTLMin),
		MaxAccuracyM:          r.Location.MaxAccuracyM,
		PasswordWindowPerIP:   minutes(r.RateLimits.PasswordWindowPerIPMin),
		PasswordWindowPerLink: minutes(r.RateLimits.PasswordWindowPerLinkMin),
		CustomSlugMinLen:      r.Links.CustomSlugMinLen,
		CustomSlugMaxLen:      r.Links.CustomSlugMaxLen,
		CustomSlugWarnLen:     r.Links.CustomSlugWarnLen,
		GuestBodyBytes:        int64(r.HTTP.GuestBodyKiB) << 10,
		AdminBodyBytes:        int64(r.HTTP.AdminBodyKiB) << 10,
		HTTPReadTimeout:       seconds(r.HTTP.ReadTimeoutS),
		MaxHeaderBytes:        r.HTTP.MaxHeaderKiB << 10,
		DiagRequests:          r.Diagnostics.Requests,
		InventoryMaxAge:       time.Duration(r.Inventory.MaxAgeS) * time.Second,
		Approval: ApprovalOptions{
			PendingTTL:             minutes(r.Approval.PendingTTLMin),
			MaxPending:             r.Approval.MaxPending,
			MaxDevices:             r.Approval.MaxDevices,
			EventsPerHour:          r.Approval.EventsPerHour,
			TouchThrottle:          minutes(r.Approval.TouchThrottleMin),
			PersistentNotification: r.Approval.PersistentNotification,
			AdminPoll:              seconds(r.Approval.AdminPollS),
		},
		HACallTimeout:         seconds(r.HA.CallTimeoutS),
		HAEventTimeout:        seconds(r.HA.EventTimeoutS),
		StreamCheck:           seconds(r.Streams.RecheckS),
		StreamHeartbeat:       seconds(r.Streams.HeartbeatS),
		StreamRetry:           seconds(r.Streams.RetryAfterS),
		StreamMaxPerIP:        r.Streams.MaxPerIP,
		AuditEnabled:          r.Audit.Enabled,
		AuditMaxFileBytes:     int64(r.Audit.MaxFileKiB) << 10,
		AuditMaxDenialsPerMin: r.Audit.MaxDenialsPerMin,
		NotFound: NotFoundOptions{
			Redirect: r.Guest.NotFoundRedirect,
			Title:    r.Guest.NotFoundTitle,
			Message:  r.Guest.NotFoundMessage,
		},
		TLS: TLSOptions{
			Enabled:            r.SSL,
			CertFile:           r.CertFile,
			KeyFile:            r.KeyFile,
			SelfSignedLifetime: time.Duration(r.SSLSelfSignedDays) * 24 * time.Hour,
		},
	}, nil
}

// MusicAssistantEnabled reports whether the app may call Music Assistant:
// both the URL and the token are set.
func (o Options) MusicAssistantEnabled() bool {
	return o.Settings.MusicAssistant.URL != "" && o.MusicAssistantToken != ""
}
