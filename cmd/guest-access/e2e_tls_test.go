package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/guesttls"
	"github.com/igrybkov/ha-guest-access/internal/migrate"
)

// peerFingerprint dials addr and returns the SHA-256 of the certificate
// it serves, with no server name sent, as a proxy that dials an IP does.
func peerFingerprint(t *testing.T, addr string) string {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	defer c.Close()
	return guesttls.Fingerprint(c.ConnectionState().PeerCertificates[0].Raw)
}

type tlsSettings struct {
	TLS struct {
		Enabled     bool      `json:"enabled"`
		Source      string    `json:"source"`
		Fingerprint string    `json:"fingerprint"`
		NotAfter    time.Time `json:"not_after"`
	} `json:"tls"`
}

// With ssl on and no files in /ssl, the guest port serves TLS only with a
// self-signed certificate, and the guest flow works over it. The admin
// port stays plain HTTP.
func TestE2EGuestTLSSelfSigned(t *testing.T) {
	a := startApp(t, appOpts{options: func(o *config.Options) { o.TLS.Enabled = true }})
	addr := strings.TrimPrefix(a.guest, "https://")

	r := a.adminJSON("GET", "/api/settings", nil)
	if r.code != 200 {
		t.Fatalf("settings over plain HTTP on the admin port: %d", r.code)
	}
	var s tlsSettings
	_ = json.Unmarshal(r.body, &s)
	if !s.TLS.Enabled || s.TLS.Source != "self-signed" || s.TLS.NotAfter.IsZero() {
		t.Fatalf("settings tls = %+v", s.TLS)
	}
	if got := peerFingerprint(t, addr); got != s.TLS.Fingerprint {
		t.Fatalf("served %s, settings say %s", got, s.TLS.Fingerprint)
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, migrate.SelfSignedKeyFile)); err != nil {
		t.Errorf("self-signed key not in /data: %v", err)
	}

	// The guest flow over TLS: the session cookie is Secure.
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	if r := a.adminJSON("POST", "/api/links/"+l.ID+"/passwords", map[string]any{"label": "Anna", "password": "pw"}); r.code != 201 {
		t.Fatalf("add password: %d %s", r.code, r.body)
	}
	r = a.do(call{method: "POST", url: a.guest + "/g/" + l.Slug + "/password", body: map[string]any{"password": "pw"}})
	session := named(r.cookies, "ga_session")
	if r.code != 200 || session == nil || !session.Secure {
		t.Fatalf("login over TLS: %d %+v", r.code, session)
	}
	if r := a.state(l, session); r.code != 200 {
		t.Fatalf("state over TLS: %d %s", r.code, r.body)
	}
	if r := a.do(call{method: "POST", url: a.guest + "/g/" + l.Slug + "/password", origin: "https://evil.example",
		body: map[string]any{"password": "pw"}}); r.code != 403 {
		t.Errorf("cross-site POST over TLS: %d", r.code)
	}

	// Plain HTTP on the guest port never reaches the app.
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/g/" + l.Slug)
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 || strings.Contains(string(body), "<script") {
			t.Errorf("plain HTTP on the guest port: %d %q", resp.StatusCode, body)
		}
	}
}

// With valid files in /ssl, the guest port serves them, and a renewal
// on disk is picked up without a restart.
func TestE2EGuestTLSFiles(t *testing.T) {
	sslDir := t.TempDir()
	write := func(at time.Time) string {
		cert, key, err := guesttls.SelfSigned(t0, 30*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		for name, b := range map[string][]byte{"fullchain.pem": cert, "privkey.pem": key} {
			p := filepath.Join(sslDir, name)
			if err := os.WriteFile(p, b, 0o600); err != nil {
				t.Fatal(err)
			}
			_ = os.Chtimes(p, at, at)
		}
		c, err := tls.X509KeyPair(cert, key)
		if err != nil {
			t.Fatal(err)
		}
		return guesttls.Fingerprint(c.Certificate[0])
	}
	first := write(t0)
	a := startApp(t, appOpts{sslDir: sslDir, options: func(o *config.Options) { o.TLS.Enabled = true }})
	addr := strings.TrimPrefix(a.guest, "https://")
	if got := peerFingerprint(t, addr); got != first {
		t.Fatalf("served %s, want the files %s", got, first)
	}
	second := write(t0.Add(time.Hour))
	if got := peerFingerprint(t, addr); got != second {
		t.Fatalf("after renewal served %s, want %s", got, second)
	}
	var s tlsSettings
	_ = json.Unmarshal(a.adminJSON("GET", "/api/settings", nil).body, &s)
	if s.TLS.Source != "files" || s.TLS.Fingerprint != second {
		t.Errorf("settings tls = %+v", s.TLS)
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, migrate.SelfSignedKeyFile)); err == nil {
		t.Error("a self-signed key was made while the files work")
	}
}

// A proxy may speak HTTP/2 to an https backend. The page and the live
// event stream work over it.
func TestE2EGuestTLSHTTP2(t *testing.T) {
	a := startApp(t, appOpts{options: func(o *config.Options) { o.TLS.Enabled = true }})
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	h2 := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
	}}
	t.Cleanup(h2.CloseIdleConnections)
	resp, err := h2.Get(a.guest + "/g/" + l.Slug + "/state")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != 200 || !strings.Contains(string(body), `"light.guest_room"`) {
		t.Fatalf("state over %s: %d %s", resp.Proto, resp.StatusCode, body)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.guest+"/g/"+l.Slug+"/events", nil)
	resp, err = h2.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != 200 {
		t.Fatalf("events over %s: %d", resp.Proto, resp.StatusCode)
	}
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	deadline := time.After(10 * time.Second)
	for {
		a.ha.Emit("state_changed", map[string]any{"entity_id": "light.guest_room",
			"new_state": map[string]any{"entity_id": "light.guest_room", "state": "on", "attributes": map[string]any{"brightness": 77}}})
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stream closed")
			}
			if strings.Contains(line, `"brightness":77`) {
				return
			}
		case <-deadline:
			t.Fatal("no event over HTTP/2")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// With ssl off (the default) the guest port is plain HTTP, as before.
func TestE2EGuestTLSOff(t *testing.T) {
	a := startApp(t, appOpts{})
	if !strings.HasPrefix(a.guest, "http://") {
		t.Fatal(a.guest)
	}
	if r := a.do(call{method: "GET", url: a.guest + "/"}); r.code != 404 {
		t.Errorf("plain HTTP guest port: %d", r.code)
	}
	var s tlsSettings
	_ = json.Unmarshal(a.adminJSON("GET", "/api/settings", nil).body, &s)
	if s.TLS.Enabled || s.TLS.Source != "" || s.TLS.Fingerprint != "" {
		t.Errorf("settings tls = %+v", s.TLS)
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, migrate.SelfSignedKeyFile)); err == nil {
		t.Error("ssl off made a key")
	}
}
