// Command guest-access is a Home Assistant app that serves links guests use
// to control a few things at home.
//
// main only parses flags and loads config; run wires the components so the
// end-to-end test can start the whole app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/approval"
	"github.com/igrybkov/ha-guest-access/internal/audit"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/guesttls"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/inventory"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/ma"
	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/supervisor"
	"github.com/igrybkov/ha-guest-access/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	// shutdownTimeout bounds the stop after SIGTERM. The binary is PID 1
	// under Supervisor (init: false), and Docker kills it after 10s anyway.
	shutdownTimeout = 10 * time.Second
	healthTimeout   = 3 * time.Second
)

// demoMain, when set, runs in place of main. Only a build with the demo
// tag sets it (see demo.go), so the release binary never holds the fake
// HA.
var demoMain func()

func main() {
	if demoMain != nil {
		demoMain()
		return
	}
	showVersion := flag.Bool("version", false, "print the version and exit")
	healthcheck := flag.Bool("healthcheck", false, "exit 0 if the local server answers /healthz, else 1")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *healthcheck {
		// Before config.Load: a health probe must not need HA_URL/HA_TOKEN.
		if err := checkHealth(os.Getenv("LISTEN")); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	adminLn, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("listen", "addr", cfg.Listen, "err", err)
		os.Exit(1)
	}
	guestLn, err := net.Listen("tcp", cfg.GuestListen)
	if err != nil {
		adminLn.Close()
		logger.Error("listen", "addr", cfg.GuestListen, "err", err)
		os.Exit(1)
	}
	if err := run(ctx, cfg, adminLn, guestLn, options{Logger: logger}); err != nil {
		logger.Error("stopped", "err", err)
		os.Exit(1)
	}
}

// options are the parts of run that tests replace. Zero values mean the
// production defaults.
type options struct {
	Logger *slog.Logger
	Now    func() time.Time
	NewMA  func(url, token string, timeout time.Duration) (ma.Client, error)
}

// run wires the app, serves the admin port on adminLn and the guest port
// on guestLn, and blocks until ctx is done or a server fails. HA being
// unreachable is not an error: the websocket client keeps reconnecting,
// and the pages still load. On return everything is stopped and both
// listeners are closed.
func run(ctx context.Context, cfg config.Config, adminLn, guestLn net.Listener, opts options) error {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	closeAll := func() { adminLn.Close(); guestLn.Close() }
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	o := cfg.Options
	if !o.LimitsEnabled {
		log.Warn("rate limits are off (rate_limits.enabled: false); nothing limits guesses at links or passwords")
	}
	for _, w := range web.OptionWarnings(o) {
		log.Warn(w)
	}

	// A bad key stops startup. It is never made again: that would end
	// every signed cookie, and may hide a bad restore.
	key, err := config.LoadOrCreateKey(cfg.DataDir)
	if err != nil {
		closeAll()
		return fmt.Errorf("hmac key: %w", err)
	}

	// The audit log. A file that can't be opened stops startup: running
	// without the record the user asked for would hide what happens.
	auditLog, err := audit.New(audit.Options{
		Path: filepath.Join(cfg.DataDir, audit.FileName), Enabled: o.AuditEnabled,
		MaxBytes: o.AuditMaxFileBytes, Logger: log, Now: opts.Now,
	})
	if err != nil {
		closeAll()
		return err
	}
	defer auditLog.Close()
	limitsOn := o.LimitsEnabled
	auditLog.Log(audit.Record{Event: "app.start", Result: audit.ResultOK, Version: version, LimitsEnabled: &limitsOn})

	// Storage. Missing files read as empty and are created on the first
	// save. Older schemas are migrated in memory; see package migrate.
	templates := config.NewTemplatesStore(migrate.OpenConfig(cfg.ConfigDir, log))
	linkStore := links.New(migrate.OpenData(cfg.DataDir, log), links.Options{
		Logger: log,
		ValidCustomSlug: func(s string) error {
			return access.ValidCustomSlugLen(s, o.CustomSlugMinLen, o.CustomSlugMaxLen)
		},
	})

	client := ha.New(ha.Options{URL: cfg.HAURL, Token: cfg.HAToken, Logger: log})
	inv := inventory.New(client, log)
	inv.SetMaxAge(o.InventoryMaxAge)
	if err := inv.Run(ctx); err != nil { // returns once subscribed
		closeAll()
		return fmt.Errorf("inventory: %w", err)
	}

	// Device approval. Decisions from HA come in as events; the
	// subscription survives reconnects.
	signer := access.NewSigner(key)
	notifier := newNotifier(cfg, client, log)
	devices := approval.New(approval.Options{
		Links:         linkStore,
		FireEvent:     client.FireEvent,
		Signer:        signer,
		Now:           opts.Now,
		Logger:        log,
		PendingTTL:    o.Approval.PendingTTL,
		MaxPending:    o.Approval.MaxPending,
		MaxDevices:    o.Approval.MaxDevices,
		EventsPerHour: o.Approval.EventsPerHour,
		TouchThrottle: o.Approval.TouchThrottle,
		MaxBodyBytes:  o.GuestBodyBytes,
		EventTimeout:  o.HAEventTimeout,
		Audit:         auditLog,
		IPv6Prefix:    o.Settings.RateLimits.IPv6Prefix,
		Notifier:      notifier,
	})
	if err := devices.Run(ctx, client.SubscribeEvents); err != nil { // returns once subscribed
		closeAll()
		return fmt.Errorf("device decisions: %w", err)
	}

	// HTTPS on the guest port. With ssl on, the port serves TLS only, or
	// the app does not start: it never falls back to plain HTTP.
	var guestTLS *guesttls.Manager
	if o.TLS.Enabled {
		guestTLS, err = guesttls.New(guesttls.Options{
			SSLDir: cfg.SSLDir, CertFile: o.TLS.CertFile, KeyFile: o.TLS.KeyFile,
			DataDir: cfg.DataDir, Lifetime: o.TLS.SelfSignedLifetime, Now: opts.Now, Logger: log,
		})
		if err != nil {
			closeAll()
			return fmt.Errorf("guest TLS: %w", err)
		}
		defer guestTLS.Close()
	}

	srv := web.New(web.Deps{
		Options:         o,
		Templates:       templates,
		Links:           linkStore,
		Inventory:       inv,
		HA:              client,
		Signer:          signer,
		Devices:         devices,
		Audit:           auditLog,
		NewMA:           opts.NewMA,
		DevAdminAnyPeer: cfg.DevAdminAnyPeer,
		TLSStatus:       tlsStatus(o.TLS, guestTLS),
		Now:             opts.Now,
		Version:         version,
		Logger:          log,
	})
	adminSrv, guestSrv := newServer(ctx, srv.AdminHandler(), log), newServer(ctx, srv.GuestHandler(), log)
	servers := []*http.Server{adminSrv, guestSrv}
	for _, s := range servers {
		setBounds(s, o)
	}
	// The admin port stays plain HTTP: Supervisor's ingress proxy talks
	// HTTP to it.
	serveGuest := func() error { return guestSrv.Serve(guestLn) }
	if guestTLS != nil {
		guestSrv.TLSConfig = guestTLS.TLSConfig()
		serveGuest = func() error { return guestSrv.ServeTLS(guestLn, "", "") }
	}

	var wg sync.WaitGroup
	wg.Go(func() { _ = client.Run(ctx) }) // returns only when ctx is done
	wg.Go(func() { srv.RunMACheck(ctx) }) // returns at once without Music Assistant
	wg.Go(func() { devices.RunExpiry(ctx) })
	if notifier != nil {
		wg.Go(func() { notifier.Run(ctx) })
	}
	serveErr := make(chan error, len(servers))
	go func() { serveErr <- adminSrv.Serve(adminLn) }()
	go func() { serveErr <- serveGuest() }()
	log.Info("guest-access started", "version", version, "listen", adminLn.Addr().String(),
		"guest_listen", guestLn.Addr().String(), "guest_tls", guestTLS != nil, "supervisor", cfg.Supervisor,
		"config_dir", cfg.ConfigDir, "data_dir", cfg.DataDir)

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case runErr = <-serveErr:
		runErr = fmt.Errorf("http server: %w", runErr)
	}

	// Cancel first: it stops the HA client, and ends open event streams
	// and requests that wait on HA, so Shutdown does not wait for them.
	cancel()
	sctx, scancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer scancel()
	for _, s := range servers {
		if err := s.Shutdown(sctx); err != nil {
			log.Warn("http shutdown", "err", err)
			_ = s.Close()
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-sctx.Done():
		log.Warn("background tasks did not stop in time")
	}
	return runErr
}

// tlsStatus returns the guest TLS state for the settings page. m is nil
// when ssl is off.
func tlsStatus(o config.TLSOptions, m *guesttls.Manager) func() web.TLSStatus {
	return func() web.TLSStatus {
		st := web.TLSStatus{Enabled: o.Enabled, CertFile: o.CertFile, KeyFile: o.KeyFile,
			SelfSignedDays: int(o.SelfSignedLifetime / (24 * time.Hour))}
		if m != nil {
			s := m.Status()
			st.Source, st.Fingerprint, st.NotAfter = string(s.Source), s.Fingerprint, s.NotAfter
		}
		return st
	}
}

// newNotifier returns the HA notifier of device requests, or nil when
// approval.persistent_notification is off. Under Supervisor the
// notification links to the admin page's pending panel. The app's slug
// comes from Supervisor at the first notification; outside Supervisor
// there is no link.
func newNotifier(cfg config.Config, client ha.Client, log *slog.Logger) *approval.Notifier {
	o := cfg.Options
	if !o.Approval.PersistentNotification {
		return nil
	}
	var panelLink func(context.Context) (string, error)
	if cfg.Supervisor {
		// Under Supervisor, HAToken is SUPERVISOR_TOKEN.
		sup := supervisor.Client{URL: supervisor.DefaultURL, Token: cfg.HAToken}
		panelLink = func(ctx context.Context) (string, error) {
			info, err := sup.SelfInfo(ctx)
			if err != nil {
				return "", err
			}
			return supervisor.PanelPath(info.Slug, web.PendingRoute), nil
		}
	}
	return approval.NewNotifier(approval.NotifierOptions{
		CallService: func(ctx context.Context, domain, service string, data map[string]any) error {
			_, err := client.CallService(ctx, domain, service, data, nil, false)
			return err
		},
		PanelLink: panelLink,
		Timeout:   o.HAEventTimeout,
		Logger:    log,
	})
}

// newServer returns a server for h with the request bounds of the
// default app options. run sets the bounds from the real options.
func newServer(ctx context.Context, h http.Handler, log *slog.Logger) *http.Server {
	s := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: it would cut every event stream.
		IdleTimeout: 2 * time.Minute,
		BaseContext: func(net.Listener) context.Context { return ctx },
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	setBounds(s, config.DefaultOptions())
	return s
}

// setBounds sets how long and how big a request may be. ReadTimeout
// covers the headers and the body, so a client that sends a body one
// byte at a time is cut off. It does not end a response that streams:
// once the body is read, the server clears the read deadline. The header
// timeout is never longer than the read timeout.
func setBounds(s *http.Server, o config.Options) {
	s.ReadTimeout = o.HTTPReadTimeout
	s.ReadHeaderTimeout = min(10*time.Second, o.HTTPReadTimeout)
	s.MaxHeaderBytes = o.MaxHeaderBytes
}

// checkHealth GETs /healthz on the loopback port of listen (LISTEN, default
// config.DefaultListen). Only the port is used: the server may listen on all
// interfaces, but the probe runs inside the container.
func checkHealth(listen string) error {
	if listen == "" {
		listen = config.DefaultListen
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("LISTEN %q: %w", listen, err)
	}
	c := &http.Client{Timeout: healthTimeout}
	resp, err := c.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return nil
}
