// Command guest-access is a Home Assistant app that serves links guests use
// to control a few things at home.
//
// This is the skeleton. It answers health checks on the admin port and logs
// how requests reach the guest port, so the network rules can be set from
// real data.
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
	"syscall"
	"time"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	// defaultListen is the admin port. Supervisor ingress and the watchdog
	// reach the app here.
	defaultListen = ":8099"
	// defaultGuestListen is the guest port. Supervisor maps it to a host port.
	defaultGuestListen = ":8888"
	// shutdownTimeout bounds the stop after SIGTERM. The binary is PID 1
	// under Supervisor (init: false), and Docker kills it after 10s anyway.
	shutdownTimeout = 10 * time.Second
	healthTimeout   = 3 * time.Second
)

// forwardHeaders are the headers a proxy may set to name the real client.
// The guest port logs them; the network rules will trust them only from
// known proxies.
var forwardHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip", "Cf-Connecting-Ip", "Cf-Ipcountry"}

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	healthcheck := flag.Bool("healthcheck", false, "exit 0 if the local server answers /healthz, else 1")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *healthcheck {
		if err := checkHealth(env("LISTEN", defaultListen)); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := run(ctx, env("LISTEN", defaultListen), env("GUEST_LISTEN", defaultGuestListen), logger); err != nil {
		logger.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// run serves the admin and guest ports until ctx is done or a server fails.
func run(ctx context.Context, listen, guestListen string, log *slog.Logger) error {
	servers := []*http.Server{
		newServer(listen, adminHandler(), log),
		newServer(guestListen, guestHandler(log), log),
	}
	serveErr := make(chan error, len(servers))
	for _, s := range servers {
		ln, err := net.Listen("tcp", s.Addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", s.Addr, err)
		}
		go func() { serveErr <- s.Serve(ln) }()
	}
	log.Info("guest-access started", "version", version, "listen", listen, "guest_listen", guestListen)

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case runErr = <-serveErr:
		runErr = fmt.Errorf("http server: %w", runErr)
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(sctx); err != nil {
			log.Warn("http shutdown", "addr", s.Addr, "err", err)
			_ = s.Close()
		}
	}
	return runErr
}

func newServer(addr string, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// adminHandler serves the admin port. For now that is the health check.
func adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "Guest Access %s: not ready yet.\n", version)
	})
	return mux
}

// guestHandler serves the guest port. It has no admin routes. For now it
// logs where each request came from and answers 503.
func guestHandler(log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attrs := []any{"peer", r.RemoteAddr, "host", r.Host}
		for _, h := range forwardHeaders {
			if v := r.Header.Get(h); v != "" {
				attrs = append(attrs, h, v)
			}
		}
		log.Info("guest request", attrs...)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Error(w, "Not ready yet.", http.StatusServiceUnavailable)
	})
}

// checkHealth GETs /healthz on the loopback port of listen. Only the port is
// used: the server may listen on all interfaces, but the probe runs inside
// the container.
func checkHealth(listen string) error {
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
