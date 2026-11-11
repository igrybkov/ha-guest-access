package guesttls

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/migrate"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

const lifetime = 90 * 24 * time.Hour

// logBuf collects log lines for checks.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logBuf) count(s string) int { return strings.Count(l.String(), s) }

// env is an ssl folder, a data dir and a clock.
type env struct {
	t       *testing.T
	ssl     string
	data    string
	log     *logBuf
	mu      sync.Mutex
	now     time.Time
	options Options
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, ssl: t.TempDir(), data: t.TempDir(), log: &logBuf{}, now: t0}
	e.options = Options{
		SSLDir: e.ssl, CertFile: "fullchain.pem", KeyFile: "privkey.pem", DataDir: e.data,
		Lifetime: lifetime, Now: e.clock, Logger: slog.New(slog.NewTextHandler(e.log, nil)),
	}
	return e
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func (e *env) manager() *Manager {
	e.t.Helper()
	m, err := New(e.options)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = m.Close() })
	return m
}

// writeFiles writes a new pair to the ssl folder and moves both mtimes
// forward by bump, so a change is seen even on a coarse clock. It returns
// the pair's fingerprint.
func (e *env) writeFiles(bump time.Duration) string {
	e.t.Helper()
	certPEM, keyPEM, err := SelfSigned(t0, lifetime)
	if err != nil {
		e.t.Fatal(err)
	}
	e.writeRaw(certPEM, keyPEM, bump)
	c, err := parsePair(certPEM, keyPEM)
	if err != nil {
		e.t.Fatal(err)
	}
	return Fingerprint(c.Leaf.Raw)
}

func (e *env) writeRaw(certPEM, keyPEM []byte, bump time.Duration) {
	e.t.Helper()
	for name, b := range map[string][]byte{"fullchain.pem": certPEM, "privkey.pem": keyPEM} {
		p := filepath.Join(e.ssl, name)
		if b == nil {
			_ = os.Remove(p)
			continue
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			e.t.Fatal(err)
		}
		mt := t0.Add(bump)
		if err := os.Chtimes(p, mt, mt); err != nil {
			e.t.Fatal(err)
		}
	}
}

func fingerprintOf(c *tls.Certificate) string { return Fingerprint(c.Leaf.Raw) }

func TestSelfSignedCreated(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	st := m.Status()
	if st.Source != SourceSelfSigned {
		t.Fatalf("source = %q", st.Source)
	}
	leaf := m.Current().Leaf
	if leaf.Subject.CommonName != CommonName || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != CommonName {
		t.Errorf("subject %v names %v", leaf.Subject, leaf.DNSNames)
	}
	if k, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || k.Curve != elliptic.P256() {
		t.Errorf("key = %T", leaf.PublicKey)
	}
	if !leaf.NotAfter.Equal(t0.Add(lifetime)) || !leaf.NotBefore.Equal(t0.Add(-clockSkew)) {
		t.Errorf("valid %v to %v", leaf.NotBefore, leaf.NotAfter)
	}
	if leaf.IsCA || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("CA %v, usage %v", leaf.IsCA, leaf.ExtKeyUsage)
	}
	if st.Fingerprint != fingerprintOf(m.Current()) || !st.NotAfter.Equal(leaf.NotAfter) {
		t.Errorf("status = %+v", st)
	}
	if len(st.Fingerprint) != 32*3-1 || strings.ToUpper(st.Fingerprint) != st.Fingerprint {
		t.Errorf("fingerprint form %q", st.Fingerprint)
	}
	for _, name := range []string{migrate.SelfSignedKeyFile, migrate.SelfSignedCertFile} {
		fi, err := os.Stat(filepath.Join(e.data, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", name, fi.Mode().Perm())
		}
	}
	if !strings.Contains(e.log.String(), st.Fingerprint) || !strings.Contains(e.log.String(), "level=INFO") {
		t.Errorf("fingerprint not logged at info: %s", e.log)
	}
}

func TestSelfSignedReusedAndRenewed(t *testing.T) {
	e := newEnv(t)
	first := e.manager().Status().Fingerprint

	for _, c := range []struct {
		name  string
		after time.Duration // since t0
		same  bool
	}{
		{"restart soon after", time.Hour, true},
		{"restart with more than a third left", lifetime * 6 / 10, true},
		{"restart with less than a third left", lifetime * 7 / 10, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e.mu.Lock()
			e.now = t0.Add(c.after)
			e.mu.Unlock()
			// Each run starts from the first pair.
			got := e.manager().Status().Fingerprint
			if (got == first) != c.same {
				t.Errorf("fingerprint same = %v, want %v", got == first, c.same)
			}
		})
		if !c.same {
			break
		}
	}
}

// A long uptime can outlive the self-signed certificate. A handshake
// near its end makes a new one, and the next start reuses that.
func TestSelfSignedRenewedWhileRunning(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	first := m.Status().Fingerprint
	e.advance(lifetime / 2)
	if fingerprintOf(m.Current()) != first {
		t.Fatal("renewed too early")
	}
	e.advance(lifetime / 4)
	// Status alone renews too, for a proxy that keeps one connection.
	renewed := m.Status().Fingerprint
	if renewed == first || fingerprintOf(m.Current()) != renewed {
		t.Fatal("not renewed with less than a third left")
	}
	if !m.Status().NotAfter.Equal(e.clock().Add(lifetime)) {
		t.Errorf("not after %v", m.Status().NotAfter)
	}
	if e.manager().Status().Fingerprint != renewed {
		t.Error("the renewed pair is not reused at the next start")
	}
}

// A broken or half-written self-signed pair in /data is made again.
func TestSelfSignedBrokenInData(t *testing.T) {
	for name, write := range map[string]func(dir string){
		"garbage cert": func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, migrate.SelfSignedCertFile), []byte("junk"), 0o600)
		},
		"key of another pair": func(dir string) {
			_, key, _ := SelfSigned(t0, lifetime)
			_ = os.WriteFile(filepath.Join(dir, migrate.SelfSignedKeyFile), key, 0o600)
		},
		"key missing": func(dir string) { _ = os.Remove(filepath.Join(dir, migrate.SelfSignedKeyFile)) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			first := e.manager().Status().Fingerprint
			write(e.data)
			if got := e.manager().Status(); got.Source != SourceSelfSigned || got.Fingerprint == first {
				t.Errorf("status = %+v", got)
			}
		})
	}
}

func TestFilesUsed(t *testing.T) {
	e := newEnv(t)
	want := e.writeFiles(0)
	m := e.manager()
	if st := m.Status(); st.Source != SourceFiles || st.Fingerprint != want {
		t.Fatalf("status = %+v, want files %s", st, want)
	}
	if _, err := os.Stat(filepath.Join(e.data, migrate.SelfSignedKeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("a self-signed key was made while the files work")
	}
}

// Missing, half or broken files fall back to a self-signed certificate,
// never to no TLS. Broken files are logged as an error.
func TestFilesFallback(t *testing.T) {
	goodCert, goodKey, _ := SelfSigned(t0, lifetime)
	_, otherKey, _ := SelfSigned(t0, lifetime)
	for _, c := range []struct {
		name      string
		cert, key []byte
		errLogged bool
	}{
		{"both missing", nil, nil, false},
		{"key missing", goodCert, nil, true},
		{"cert missing", nil, goodKey, true},
		{"cert garbage", []byte("not a pem"), goodKey, true},
		{"key garbage", goodCert, []byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), true},
		{"key of another cert", goodCert, otherKey, true},
		{"empty files", []byte{}, []byte{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.writeRaw(c.cert, c.key, 0)
			m := e.manager()
			if st := m.Status(); st.Source != SourceSelfSigned || m.Current() == nil {
				t.Fatalf("status = %+v", st)
			}
			if got := strings.Contains(e.log.String(), "level=ERROR"); got != c.errLogged {
				t.Errorf("error logged = %v, want %v: %s", got, c.errLogged, e.log)
			}
		})
	}
}

// The file names come from the options, but the files are read through an
// os.Root: a symlink or ".." can't reach outside the ssl folder.
func TestFilesStayInSSLDir(t *testing.T) {
	outside := t.TempDir()
	cert, key, _ := SelfSigned(t0, lifetime)
	_ = os.WriteFile(filepath.Join(outside, "c.pem"), cert, 0o600)
	_ = os.WriteFile(filepath.Join(outside, "k.pem"), key, 0o600)

	t.Run("symlinks", func(t *testing.T) {
		e := newEnv(t)
		if err := os.Symlink(filepath.Join(outside, "c.pem"), filepath.Join(e.ssl, "fullchain.pem")); err != nil {
			t.Skip("no symlinks:", err)
		}
		_ = os.Symlink(filepath.Join(outside, "k.pem"), filepath.Join(e.ssl, "privkey.pem"))
		if st := e.manager().Status(); st.Source != SourceSelfSigned {
			t.Errorf("a symlink out of the ssl folder was followed: %+v", st)
		}
	})
	t.Run("dot dot", func(t *testing.T) {
		e := newEnv(t)
		rel, err := filepath.Rel(e.ssl, outside)
		if err != nil {
			t.Fatal(err)
		}
		e.options.CertFile, e.options.KeyFile = filepath.Join(rel, "c.pem"), filepath.Join(rel, "k.pem")
		if st := e.manager().Status(); st.Source != SourceSelfSigned {
			t.Errorf("a path with .. left the ssl folder: %+v", st)
		}
	})
	t.Run("symlink inside is fine", func(t *testing.T) {
		e := newEnv(t)
		want := e.writeFiles(0)
		_ = os.Mkdir(filepath.Join(e.ssl, "live"), 0o700)
		if err := os.Symlink("../fullchain.pem", filepath.Join(e.ssl, "live", "cert.pem")); err != nil {
			t.Skip("no symlinks:", err)
		}
		e.options.CertFile = "live/cert.pem"
		if st := e.manager().Status(); st.Source != SourceFiles || st.Fingerprint != want {
			t.Errorf("status = %+v", st)
		}
	})
}

func TestNoSSLDir(t *testing.T) {
	e := newEnv(t)
	e.options.SSLDir = filepath.Join(e.ssl, "missing")
	if st := e.manager().Status(); st.Source != SourceSelfSigned {
		t.Errorf("status = %+v", st)
	}
}

// A data dir that can't be written leaves no certificate, so New fails
// and the caller does not serve plain HTTP.
func TestNoCertificateIsAnError(t *testing.T) {
	e := newEnv(t)
	blocker := filepath.Join(e.data, "file")
	_ = os.WriteFile(blocker, nil, 0o600)
	e.options.DataDir = filepath.Join(blocker, "sub")
	if _, err := New(e.options); err == nil {
		t.Fatal("New made no certificate and returned no error")
	}
	e.options.DataDir, e.options.Lifetime = e.data, 0
	if _, err := New(e.options); err == nil {
		t.Fatal("zero lifetime accepted")
	}
}

func TestReloadOnChange(t *testing.T) {
	e := newEnv(t)
	first := e.writeFiles(0)
	m := e.manager()

	second := e.writeFiles(time.Minute)
	if got := m.Status().Fingerprint; got != second || second == first {
		t.Fatalf("status after renewal got %s, want %s", got, second)
	}
	if got := fingerprintOf(m.Current()); got != second {
		t.Fatalf("after renewal got %s, want %s", got, second)
	}
	if m.Status().Fingerprint != second {
		t.Error("status not updated")
	}

	// A broken write keeps the last good pair, and logs once, not at
	// every handshake.
	e.writeRaw([]byte("half written"), []byte("junk"), 2*time.Minute)
	for range 3 {
		if got := fingerprintOf(m.Current()); got != second {
			t.Fatalf("broken files replaced the good pair: %s", got)
		}
	}
	if n := e.log.count("do not load, keeping"); n != 1 {
		t.Errorf("broken reload logged %d times", n)
	}

	// Files removed: keep serving.
	e.writeRaw(nil, nil, 0)
	if got := fingerprintOf(m.Current()); got != second {
		t.Fatalf("removed files dropped the pair: %s", got)
	}

	// A good pair again.
	third := e.writeFiles(3 * time.Minute)
	if got := fingerprintOf(m.Current()); got != third {
		t.Fatalf("got %s, want %s", got, third)
	}
}

// Files that show up after a start on the self-signed certificate take
// over at the next handshake.
func TestFilesAppearLater(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	if m.Status().Source != SourceSelfSigned {
		t.Fatal("not self-signed")
	}
	want := e.writeFiles(time.Minute)
	if got := fingerprintOf(m.Current()); got != want || m.Status().Source != SourceFiles {
		t.Fatalf("got %s %s, want files %s", got, m.Status().Source, want)
	}
	// Files never renew as self-signed, whatever the clock says.
	e.advance(lifetime)
	if got := fingerprintOf(m.Current()); got != want {
		t.Error("files pair replaced by a renewal")
	}
}

// A real TLS server on loopback serves the manager's certificate, takes
// TLS 1.2 and later only, and never answers plain HTTP with the page.
func TestTLSRoundTrip(t *testing.T) {
	e := newEnv(t)
	m := e.manager()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "secret page") }),
		TLSConfig: m.TLSConfig(),
		ErrorLog:  slog.NewLogLogger(slog.DiscardHandler, slog.LevelError),
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	addr := ln.Addr().String()

	// The client pins the fingerprint, as a proxy could.
	want := m.Status().Fingerprint
	var seen string
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			seen = Fingerprint(cs.PeerCertificates[0].Raw)
			if seen != want {
				return errors.New("fingerprint mismatch")
			}
			return nil
		},
	}}}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "secret page" || seen != want || resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("body %q, cert %s, want %s", body, seen, want)
	}

	// A client that verifies the name guest-access with the cert as a
	// trusted root also works.
	pool := x509.NewCertPool()
	pool.AddCert(m.Current().Leaf)
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool, ServerName: CommonName})
	if err != nil {
		t.Fatalf("verified dial: %v", err)
	}
	conn.Close()

	// TLS 1.1 is refused.
	if c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11}); err == nil {
		c.Close()
		t.Error("TLS 1.1 handshake succeeded")
	}

	// Plain HTTP gets no page.
	resp, err = (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/")
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || strings.Contains(string(body), "secret page") {
			t.Errorf("plain HTTP got %d %q", resp.StatusCode, body)
		}
	}
}

func TestFingerprint(t *testing.T) {
	if got := Fingerprint([]byte("abc")); got != "BA:78:16:BF:8F:01:CF:EA:41:41:40:DE:5D:AE:22:23:B0:03:61:A3:96:17:7A:9C:B4:10:FF:61:F2:00:15:AD" {
		t.Errorf("Fingerprint = %s", got)
	}
}
