//go:build demo

// The demo server: the real app wiring (run) against an in-process fake
// HA with a small made-up house. It never connects to a real Home
// Assistant: HA_URL, HA_TOKEN and every other env setting are ignored.
// Data lives in a temp folder that is removed on exit.
//
// Build or run it with the demo tag: make demo, or
// go run -tags demo ./cmd/guest-access.

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/demo"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
)

func init() { demoMain = runDemo }

func runDemo() {
	adminAddr := flag.String("admin", "127.0.0.1:8099", "admin port address (loopback only)")
	guestAddr := flag.String("guest", "127.0.0.1:8888", "guest port address (loopback only)")
	controlAddr := flag.String("control", "", "optional loopback address for test hooks: GET /calls lists the service calls the fake HA got, GET /events the events the app fired")
	scriptRun := flag.Duration("script-run", 10*time.Second, "how long a demo script stays on")
	debug := flag.Bool("debug", false, "log at debug level")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := demoServe(*adminAddr, *guestAddr, *controlAddr, *scriptRun, log); err != nil {
		log.Error("demo", "err", err)
		os.Exit(1)
	}
}

func demoServe(adminAddr, guestAddr, controlAddr string, scriptRun time.Duration, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// The admin port runs with the dev peer bypass, so it must not be
	// reachable from the network.
	adminLn, err := listenLoopback(adminAddr)
	if err != nil {
		return err
	}
	guestLn, err := listenLoopback(guestAddr)
	if err != nil {
		adminLn.Close()
		return err
	}

	fake := demo.House()
	token := rand.Text()
	haSrv := hatest.NewServer(fake, token)
	defer haSrv.Close()
	sim := hatest.NewSim(fake, hatest.SimOptions{
		Emit:      func(typ string, data any) { haSrv.Emit(typ, data) },
		ScriptRun: scriptRun,
	})
	defer sim.Stop()
	fake.Respond = sim.Respond

	dir, err := os.MkdirTemp("", "guest-access-demo-")
	if err != nil {
		adminLn.Close()
		guestLn.Close()
		return err
	}
	defer os.RemoveAll(dir)
	cfgDir, dataDir := filepath.Join(dir, "config"), filepath.Join(dir, "data")
	for _, d := range []string{cfgDir, dataDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			adminLn.Close()
			guestLn.Close()
			return err
		}
	}

	opts := config.DefaultOptions()
	opts.PublicBaseURL = "http://" + guestLn.Addr().String()
	opts.AuditEnabled = true
	cfg := config.Config{
		LogLevel: "info", ConfigDir: cfgDir, DataDir: dataDir,
		Listen: adminLn.Addr().String(), GuestListen: guestLn.Addr().String(),
		HAURL: haSrv.URL(), HAToken: token, DevAdminAnyPeer: true, Options: opts,
	}

	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, adminLn, guestLn, options{Logger: log}) }()

	adminURL := "http://" + adminLn.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second}
	if err := waitHealthy(ctx, client, adminURL, done); err != nil {
		stop()
		return err
	}
	seeded, err := demo.Seed(ctx, client, adminURL, time.Now())
	if err != nil {
		stop()
		<-done
		return fmt.Errorf("seed: %w", err)
	}

	if controlAddr != "" {
		ctl, err := listenLoopback(controlAddr)
		if err != nil {
			stop()
			<-done
			return err
		}
		ctlSrv := &http.Server{Handler: controlHandler(sim, fake), ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = ctlSrv.Serve(ctl) }()
		defer ctlSrv.Close()
	}

	fmt.Printf("\nGuest Access demo. Nothing here talks to a real Home Assistant.\n\n"+
		"  Admin: %s/\n  Guest: %s\n\nStop with Ctrl+C. Data is removed on exit.\n\n", adminURL, seeded.URL)
	return <-done
}

// listenLoopback listens on addr, which must be a loopback IP and port.
func listenLoopback(addr string) (net.Listener, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().IsLoopback() {
		return nil, fmt.Errorf("%q: the demo listens on loopback only, such as 127.0.0.1:8099", addr)
	}
	return net.Listen("tcp", addr)
}

// waitHealthy polls /healthz until the app answers or run stops.
func waitHealthy(ctx context.Context, c *http.Client, adminURL string, done <-chan error) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			if err == nil {
				err = errors.New("the app stopped")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if resp, err := c.Get(adminURL + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("the app did not start within 10s")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// controlHandler serves the test hooks: the calls the fake HA got, with
// times, and the events the app fired.
func controlHandler(sim *hatest.Sim, fake *hatest.Fake) http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /calls", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, sim.Calls()) })
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, fake.FiredEvents()) })
	return mux
}
