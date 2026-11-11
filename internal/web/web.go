// Package web is the HTTP side of the app. It serves two ports with two
// separate muxes:
//
//   - Admin (AdminHandler): the JSON API and the admin UI. Only the
//     Supervisor ingress proxy may reach it.
//   - Guest (GuestHandler): the guest page and the guest API under
//     /g/{slug}. It has no admin routes at all.
//
// Every check that decides what a guest may do lives in authz and access.
// This package wires them in a fixed order and turns their answers into
// HTTP responses that never echo internal details.
package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/ma"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// HA is the part of the HA client the server uses. ha.Client satisfies it.
type HA interface {
	CallService(ctx context.Context, domain, service string, data map[string]any, target *ha.Target, returnResponse bool) (json.RawMessage, error)
	FireEvent(ctx context.Context, eventType string, data map[string]any) error
}

// Inventory is the registry snapshot and state source. *inventory.Cache
// satisfies it.
type Inventory interface {
	Inventory(ctx context.Context) (model.Inventory, error)
	States(ctx context.Context, ids map[string]bool) (map[string]model.State, error)
	Watch(ctx context.Context, ids map[string]bool, fn func(model.State)) error
}

// DeviceGate decides whether this request's device may use the link. On
// ok=false it has already written the response. It runs after the
// password session and before any grant check.
type DeviceGate interface {
	Admit(w http.ResponseWriter, r *http.Request, link model.Link, c access.Client, label string) (deviceID string, ok bool)
}

// AllowAllDevices is the DeviceGate of links without device approval: it
// admits every device.
type AllowAllDevices struct{}

// Admit admits the request.
func (AllowAllDevices) Admit(http.ResponseWriter, *http.Request, model.Link, access.Client, string) (string, bool) {
	return "", true
}

// Policy holds the save checks that need more than one file: no
// network: local while local access is off, actions not in the catalog,
// templates a link names that do not exist. Errors should be
// *model.ValidationError, so the admin UI can mark fields.
//
// ValidateLink gets drafts too: on create, the link has no id and no slug
// yet. It must not require them; Link.Validate in the store checks shape.
type Policy interface {
	ValidateTemplates(model.Templates, model.Settings) error
	ValidateLink(model.Link, model.Templates, model.Settings) error
}

// Deps are the collaborators of the server.
type Deps struct {
	// Options are the app options: settings and every tunable value. They
	// never change while the app runs; Supervisor restarts it on a save.
	Options   config.Options
	Templates *config.TemplatesStore
	Links     *links.Store
	Inventory Inventory
	HA        HA
	Signer    *access.Signer

	Policy  Policy     // nil = a basic policy (see basicPolicy)
	Devices DeviceGate // nil = AllowAllDevices

	// NewMA builds the Music Assistant client. nil = ma.NewWithTimeout.
	// It may return a nil Client: MA is off.
	NewMA func(url, token string, timeout time.Duration) (ma.Client, error)

	// DevAdminAnyPeer lets any peer reach the admin port (make run-dev).
	DevAdminAnyPeer bool

	// Now is the clock. nil = time.Now.
	Now func() time.Time
	// StreamCheck is how often an open event stream checks its link
	// again. 0 = defaultStreamCheck.
	StreamCheck time.Duration

	Version string
	Logger  *slog.Logger
}

// Timeouts for calls into HA, and fallbacks for zero options (tests).
// The real values come from the app options.
const (
	haCallTimeout       = 10 * time.Second
	haEventTimeout      = 5 * time.Second
	defaultStreamCheck  = 5 * time.Second
	defaultGuestBody    = 16 << 10
	defaultAdminBody    = 1 << 20
	defaultNearTTL      = 15 * time.Minute
	defaultMaxAccuracyM = 1000
)

// Server serves both ports. Safe for concurrent use.
type Server struct {
	deps    Deps
	log     *slog.Logger
	policy  Policy
	devices DeviceGate
	newMA   func(url, token string, timeout time.Duration) (ma.Client, error)

	lim     *limitSet
	trusted []netip.Prefix // trusted proxies
	nets    []netip.Prefix // local networks

	streams streamCounter
	diag    *diagRing
	assets  *assetSet
}

// New returns a Server.
func New(d Deps) *Server {
	s := &Server{
		deps:    d,
		log:     d.Logger,
		policy:  d.Policy,
		devices: d.Devices,
		newMA:   d.NewMA,
		streams: streamCounter{open: map[string]int{}},
		diag:    newDiagRing(max(1, d.Options.DiagRequests)),
		assets:  loadAssets(),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.policy == nil {
		s.policy = basicPolicy{}
	}
	if s.devices == nil {
		s.devices = AllowAllDevices{}
	}
	if s.newMA == nil {
		s.newMA = ma.NewWithTimeout
	}
	if s.deps.Now == nil {
		s.deps.Now = time.Now
	}
	o := &s.deps.Options
	if o.MATimeout <= 0 {
		o.MATimeout = ma.Timeout
	}
	if o.GuestBodyBytes <= 0 {
		o.GuestBodyBytes = defaultGuestBody
	}
	if o.AdminBodyBytes <= 0 {
		o.AdminBodyBytes = defaultAdminBody
	}
	if o.NearTTL <= 0 {
		o.NearTTL = defaultNearTTL
	}
	if o.MaxAccuracyM <= 0 {
		o.MaxAccuracyM = defaultMaxAccuracyM
	}
	o.Settings.RateLimits = o.Settings.RateLimits.WithDefaults()
	// Options are checked at load; a bad entry here only drops it, which
	// fails closed (no trusted proxy, nothing local).
	s.trusted = parsePrefixes(o.Settings.TrustedProxies, s.log, "trusted proxy")
	s.nets = parsePrefixes(o.Settings.LocalAccess.Networks, s.log, "local network")
	s.lim = newLimitSet(*o)
	if s.deps.StreamCheck <= 0 {
		s.deps.StreamCheck = defaultStreamCheck
	}
	return s
}

func (s *Server) now() time.Time { return s.deps.Now() }

// settings returns the settings from the app options.
func (s *Server) settings() model.Settings { return s.deps.Options.Settings }

func parsePrefixes(list []string, log *slog.Logger, what string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, v := range list {
		p, err := model.ParsePrefix(v)
		if err != nil {
			log.Warn("ignoring a bad "+what, "value", v)
			continue
		}
		out = append(out, p)
	}
	return out
}

// recoverPanics turns a panic in a handler into a 500 so one bad request
// does not take the process down.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("web: panic", "route", routeLabel(r.URL.Path), "panic", v)
				writeError(w, http.StatusInternalServerError, msgInternal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
