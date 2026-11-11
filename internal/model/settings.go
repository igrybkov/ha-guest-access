package model

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

// Settings are the settings part of the app options (/data/options.json).
// internal/config fills them in; nothing reads or writes them as a file.
// The Music Assistant token is not here: it is in config.Options.
type Settings struct {
	Version     int                 `yaml:"version" json:"version"`
	LocalAccess LocalAccessSettings `yaml:"local_access" json:"local_access"`
	// TrustedProxies are the proxies whose forwarding headers count: CIDRs
	// or single IPs. Empty means the peer address is the client IP.
	TrustedProxies []string `yaml:"trusted_proxies" json:"trusted_proxies"`
	// HomeRadiusDefaultM is the near-home radius the editor suggests.
	HomeRadiusDefaultM int                    `yaml:"home_radius_default_m" json:"home_radius_default_m"`
	MusicAssistant     MusicAssistantSettings `yaml:"music_assistant" json:"music_assistant"`
	Guest              GuestSettings          `yaml:"guest" json:"guest"`
	RateLimits         RateLimits             `yaml:"rate_limits" json:"rate_limits"`
}

// RateLimits are the request limits of the app. Every value must be
// positive. Durations are whole minutes or seconds, as the field name
// says, so YAML and JSON agree.
type RateLimits struct {
	// GuestPerMin and GuestBurst limit every guest request before sign-in
	// per client IP, apart from static assets.
	GuestPerMin int `yaml:"guest_per_min" json:"guest_per_min"`
	GuestBurst  int `yaml:"guest_burst" json:"guest_burst"`
	// AssetsPerMin and AssetsBurst limit static assets (the page's script
	// and style files) per client IP. They do not use GuestPerMin.
	AssetsPerMin int `yaml:"assets_per_min" json:"assets_per_min"`
	AssetsBurst  int `yaml:"assets_burst" json:"assets_burst"`
	// ActionsPerMin and ActionsBurst limit actions per link + client IP.
	ActionsPerMin int `yaml:"actions_per_min" json:"actions_per_min"`
	ActionsBurst  int `yaml:"actions_burst" json:"actions_burst"`
	// ContinuousPerMin and ContinuousBurst limit continuous actions
	// (sliders) per link + client IP + entity. Those actions skip the
	// ActionsPerMin limit.
	ContinuousPerMin int `yaml:"continuous_per_min" json:"continuous_per_min"`
	ContinuousBurst  int `yaml:"continuous_burst" json:"continuous_burst"`
	// MaxStreamsPerLink caps open SSE state streams per link.
	MaxStreamsPerLink int `yaml:"max_streams_per_link" json:"max_streams_per_link"`
	// MissesPerHourPerIP and MissesPerHourGlobal limit unknown slugs; over
	// either, misses get 429 instead of 404.
	MissesPerHourPerIP  int `yaml:"misses_per_hour_per_ip" json:"misses_per_hour_per_ip"`
	MissesPerHourGlobal int `yaml:"misses_per_hour_global" json:"misses_per_hour_global"`
	// PasswordFailsPerIP counts wrong passwords per link + client IP;
	// over it, that IP is blocked on the link for PasswordLockoutMin.
	// PasswordFailsPerLink counts them per link from any IP; over it,
	// the link takes one try per PasswordLinkDelayS. The windows of both
	// counts are app options too (config.Options).
	PasswordFailsPerIP   int `yaml:"password_fails_per_ip" json:"password_fails_per_ip"`
	PasswordFailsPerLink int `yaml:"password_fails_per_link" json:"password_fails_per_link"`
	PasswordLockoutMin   int `yaml:"password_lockout_min" json:"password_lockout_min"`
	PasswordLinkDelayS   int `yaml:"password_link_delay_s" json:"password_link_delay_s"`
	// AdminPerMin limits the admin API.
	AdminPerMin int `yaml:"admin_per_min" json:"admin_per_min"`
}

// DefaultRateLimits returns the limits of a new install.
func DefaultRateLimits() RateLimits {
	return RateLimits{
		GuestPerMin:          20,
		GuestBurst:           15,
		AssetsPerMin:         300,
		AssetsBurst:          100,
		ActionsPerMin:        120,
		ActionsBurst:         40,
		ContinuousPerMin:     600,
		ContinuousBurst:      120,
		MaxStreamsPerLink:    10,
		MissesPerHourPerIP:   10,
		MissesPerHourGlobal:  100,
		PasswordFailsPerIP:   5,
		PasswordFailsPerLink: 30,
		PasswordLockoutMin:   60,
		PasswordLinkDelayS:   10,
		AdminPerMin:          600,
	}
}

// WithDefaults returns r with every zero field set to its default, so a
// value left out gets the default instead of 0.
func (r RateLimits) WithDefaults() RateLimits {
	d := DefaultRateLimits()
	for _, f := range []struct{ v, def *int }{
		{&r.GuestPerMin, &d.GuestPerMin},
		{&r.GuestBurst, &d.GuestBurst},
		{&r.AssetsPerMin, &d.AssetsPerMin},
		{&r.AssetsBurst, &d.AssetsBurst},
		{&r.ActionsPerMin, &d.ActionsPerMin},
		{&r.ActionsBurst, &d.ActionsBurst},
		{&r.ContinuousPerMin, &d.ContinuousPerMin},
		{&r.ContinuousBurst, &d.ContinuousBurst},
		{&r.MaxStreamsPerLink, &d.MaxStreamsPerLink},
		{&r.MissesPerHourPerIP, &d.MissesPerHourPerIP},
		{&r.MissesPerHourGlobal, &d.MissesPerHourGlobal},
		{&r.PasswordFailsPerIP, &d.PasswordFailsPerIP},
		{&r.PasswordFailsPerLink, &d.PasswordFailsPerLink},
		{&r.PasswordLockoutMin, &d.PasswordLockoutMin},
		{&r.PasswordLinkDelayS, &d.PasswordLinkDelayS},
		{&r.AdminPerMin, &d.AdminPerMin},
	} {
		if *f.v == 0 {
			*f.v = *f.def
		}
	}
	return r
}

func (r RateLimits) validate(p *problems, path string) {
	for _, f := range []struct {
		name string
		v    int
	}{
		{"guest_per_min", r.GuestPerMin},
		{"guest_burst", r.GuestBurst},
		{"assets_per_min", r.AssetsPerMin},
		{"assets_burst", r.AssetsBurst},
		{"actions_per_min", r.ActionsPerMin},
		{"actions_burst", r.ActionsBurst},
		{"continuous_per_min", r.ContinuousPerMin},
		{"continuous_burst", r.ContinuousBurst},
		{"max_streams_per_link", r.MaxStreamsPerLink},
		{"misses_per_hour_per_ip", r.MissesPerHourPerIP},
		{"misses_per_hour_global", r.MissesPerHourGlobal},
		{"password_fails_per_ip", r.PasswordFailsPerIP},
		{"password_fails_per_link", r.PasswordFailsPerLink},
		{"password_lockout_min", r.PasswordLockoutMin},
		{"password_link_delay_s", r.PasswordLinkDelayS},
		{"admin_per_min", r.AdminPerMin},
	} {
		if f.v <= 0 {
			p.add(join(path, f.name), "must be positive")
		}
	}
}

// LocalAccessSettings turn on the "local only" network option.
type LocalAccessSettings struct {
	// Enabled is off by default. While off, a rule with network "local"
	// denies every request, and a local request does not count as near
	// home.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Networks are the CIDRs that count as local.
	Networks []string `yaml:"networks" json:"networks"`
}

// MusicAssistantSettings is where Music Assistant runs. With no URL, or no
// token in the app options, the app never calls it.
type MusicAssistantSettings struct {
	URL string `yaml:"url" json:"url"` // such as http://d5369777-music-assistant:8095
}

// GuestSettings brand the guest page. Plain text only.
type GuestSettings struct {
	Title       string `yaml:"title" json:"title"`
	Welcome     string `yaml:"welcome" json:"welcome"`
	AccentColor string `yaml:"accent_color" json:"accent_color"` // "#rrggbb"; empty = app default
	// SliderIntervalMS is the least time between two values the guest
	// page sends while a slider is dragged.
	SliderIntervalMS int `yaml:"slider_interval_ms" json:"slider_interval_ms"`
}

// DefaultSliderIntervalMS and MinSliderIntervalMS bound
// GuestSettings.SliderIntervalMS.
const (
	DefaultSliderIntervalMS = 300
	MinSliderIntervalMS     = 100
	MaxSliderIntervalMS     = 5000
)

// DefaultLocalNetworks are the RFC 1918 ranges and IPv6 ULA.
var DefaultLocalNetworks = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}

// DefaultHomeRadiusM is HomeRadiusDefaultM of a new install.
const DefaultHomeRadiusM = 300

// DefaultSettings returns the settings a new install starts with.
func DefaultSettings() Settings {
	return Settings{
		Version:            1,
		LocalAccess:        LocalAccessSettings{Networks: append([]string(nil), DefaultLocalNetworks...)},
		TrustedProxies:     []string{},
		HomeRadiusDefaultM: DefaultHomeRadiusM,
		Guest:              GuestSettings{Title: "Welcome", SliderIntervalMS: DefaultSliderIntervalMS},
		RateLimits:         DefaultRateLimits(),
	}
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Validate checks networks, proxies, the radius, the MA URL and the color.
func (s Settings) Validate() error {
	var p problems
	for i, n := range s.LocalAccess.Networks {
		if _, err := netip.ParsePrefix(n); err != nil {
			p.add(fmt.Sprintf("local_access.networks[%d]", i), "%q is not a CIDR such as 192.168.0.0/16", n)
		}
	}
	for i, n := range s.TrustedProxies {
		if _, err := ParsePrefix(n); err != nil {
			p.add(fmt.Sprintf("trusted_proxies[%d]", i), "%q is not an IP or a CIDR", n)
		}
	}
	if s.HomeRadiusDefaultM < 0 || s.HomeRadiusDefaultM > MaxNearHomeM {
		p.add("home_radius_default_m", "must be between 0 and %d", MaxNearHomeM)
	}
	if u := s.MusicAssistant.URL; u != "" {
		pu, err := url.Parse(u)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
			p.add("music_assistant.url", "must be an http or https URL")
		}
	}
	s.RateLimits.validate(&p, "rate_limits")
	if v := s.Guest.SliderIntervalMS; v < MinSliderIntervalMS || v > MaxSliderIntervalMS {
		p.add("guest.slider_interval_ms", "must be between %d and %d", MinSliderIntervalMS, MaxSliderIntervalMS)
	}
	if c := s.Guest.AccentColor; c != "" && !colorRe.MatchString(c) {
		p.add("guest.accent_color", "must look like #1e88e5")
	}
	return p.err()
}

// ParsePrefix parses a CIDR, or a single IP as a one-address prefix.
func ParsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// ParsePrefixes parses each entry with ParsePrefix. It stops at the first
// bad one.
func ParsePrefixes(list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		p, err := ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", s, err)
		}
		out = append(out, p)
	}
	return out, nil
}
