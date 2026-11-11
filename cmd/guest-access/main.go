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
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/inventory"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/ma"
	"github.com/igrybkov/ha-guest-access/internal/migrate"
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
	})
	if err := devices.Run(ctx, client.SubscribeEvents); err != nil { // returns once subscribed
		closeAll()
		return fmt.Errorf("device decisions: %w", err)
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
		Now:             opts.Now,
		Version:         version,
		Logger:          log,
	})
	servers := []*http.Server{newServer(ctx, srv.AdminHandler(), log), newServer(ctx, srv.GuestHandler(), log)}

	var wg sync.WaitGroup
	wg.Go(func() { _ = client.Run(ctx) }) // returns only when ctx is done
	serveErr := make(chan error, len(servers))
	for i, ln := range []net.Listener{adminLn, guestLn} {
		s := servers[i]
		go func() { serveErr <- s.Serve(ln) }()
	}
	log.Info("guest-access started", "version", version, "listen", adminLn.Addr().String(),
		"guest_listen", guestLn.Addr().String(), "supervisor", cfg.Supervisor,
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

func newServer(ctx context.Context, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: event streams stay open.
		IdleTimeout: 2 * time.Minute,
		BaseContext: func(net.Listener) context.Context { return ctx },
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
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
