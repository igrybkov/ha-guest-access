// Package guesttls serves the certificate of the guest port when the ssl
// option is on.
//
// The certificate comes from certfile and keyfile in the ssl folder
// (/ssl). When they are missing or broken, the app makes a self-signed
// certificate in the data dir and uses it. It never falls back to plain
// HTTP: the guest port either serves TLS or the app does not start.
//
// Each TLS handshake checks the size and modification time of both files.
// When they change, the pair is read again, so a renewal needs no restart.
// A pair that fails to load is logged once, and the last good certificate
// stays in use. A self-signed certificate is made again when less than a
// third of its lifetime is left.
package guesttls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/store"
)

// Source says where the certificate in use came from.
type Source string

const (
	// SourceFiles is certfile and keyfile in the ssl folder.
	SourceFiles Source = "files"
	// SourceSelfSigned is the certificate the app made in the data dir.
	SourceSelfSigned Source = "self-signed"
)

// CommonName is the subject and the only DNS name of a self-signed
// certificate. It names nothing about the home.
const CommonName = "guest-access"

// clockSkew is how far back a self-signed certificate's NotBefore goes,
// so a peer with a clock a bit behind still takes it.
const clockSkew = time.Hour

// Options configure a Manager.
type Options struct {
	// SSLDir is the ssl folder. CertFile and KeyFile are paths inside it.
	// They are read through an os.Root, so neither a ".." nor a symlink
	// can leave the folder.
	SSLDir   string
	CertFile string
	KeyFile  string
	// DataDir holds the self-signed pair.
	DataDir string
	// Lifetime is how long a new self-signed certificate is valid.
	Lifetime time.Duration
	// Now is the clock. nil = time.Now.
	Now    func() time.Time
	Logger *slog.Logger
}

// Status is the certificate in use, for the settings page.
type Status struct {
	Source Source
	// Fingerprint is the SHA-256 of the certificate, as uppercase hex
	// pairs split by colons, as openssl prints it.
	Fingerprint string
	NotAfter    time.Time
}

// Manager picks and reloads the guest port's certificate. Safe for
// concurrent use.
type Manager struct {
	o    Options
	log  *slog.Logger
	now  func() time.Time
	root *os.Root // nil when the ssl folder can't be opened

	mu      sync.Mutex
	cert    *tls.Certificate
	status  Status
	tried   stamp     // the files as last read, or tried
	renewAt time.Time // when a self-signed certificate is made again
}

// stamp is what a cheap stat sees of the two files. A change in either
// one means the pair is read again.
type stamp struct {
	cert, key fileStamp
}

type fileStamp struct {
	exists  bool
	size    int64
	modTime time.Time
}

func (s stamp) bothExist() bool { return s.cert.exists && s.key.exists }
func (s stamp) noneExist() bool { return !s.cert.exists && !s.key.exists }

// New picks the first certificate. It uses the files when both load, and
// a self-signed certificate otherwise. It returns an error only when no
// certificate can be had at all; the caller must then not serve the port.
func New(o Options) (*Manager, error) {
	m := &Manager{o: o, log: o.Logger, now: o.Now}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.now == nil {
		m.now = time.Now
	}
	if o.Lifetime <= 0 {
		return nil, errors.New("guesttls: lifetime must be positive")
	}
	if root, err := os.OpenRoot(o.SSLDir); err == nil {
		m.root = root
	} else {
		m.log.Warn("guest TLS: can't open the ssl folder", "dir", o.SSLDir, "err", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.tried = m.stat()
	switch st := m.tried; {
	case st.noneExist():
		m.log.Info("guest TLS: certfile and keyfile not found, using a self-signed certificate",
			"dir", o.SSLDir, "certfile", o.CertFile, "keyfile", o.KeyFile)
	case !st.bothExist():
		m.log.Error("guest TLS: only one of certfile and keyfile exists, using a self-signed certificate",
			"dir", o.SSLDir, "certfile", o.CertFile, "cert_found", st.cert.exists, "keyfile", o.KeyFile, "key_found", st.key.exists)
	default:
		c, err := m.loadFiles()
		if err == nil {
			m.use(c, SourceFiles)
			m.log.Info("guest TLS: using certfile and keyfile", "certfile", o.CertFile, "keyfile", o.KeyFile,
				"sha256", m.status.Fingerprint, "not_after", m.status.NotAfter)
			return m, nil
		}
		m.log.Error("guest TLS: certfile and keyfile do not load, using a self-signed certificate",
			"certfile", o.CertFile, "keyfile", o.KeyFile, "err", err)
	}
	if err := m.useSelfSigned(); err != nil {
		return nil, err
	}
	return m, nil
}

// Close releases the ssl folder.
func (m *Manager) Close() error {
	if m.root == nil {
		return nil
	}
	return m.root.Close()
}

// TLSConfig returns the server config of the guest port: TLS 1.2 or
// later, Go defaults otherwise. It sets only GetCertificate. A static
// Certificates list would win for a client that sends no server name,
// such as a proxy that dials an IP.
func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return m.Current(), nil
		},
	}
}

// Status returns the certificate in use. Like Current, it first picks up
// changed files and renews a self-signed certificate near its end, so it
// is right even when no handshake happened for a while: a proxy can keep
// one connection open for long.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh()
	return m.status
}

// Current returns the certificate to serve, after a refresh.
func (m *Manager) Current() *tls.Certificate {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh()
	return m.cert
}

// refresh reloads the files when they changed, and renews a self-signed
// certificate that is near its end. On any failure it keeps the
// certificate it has. The caller holds m.mu.
func (m *Manager) refresh() {
	if st := m.stat(); st != m.tried {
		m.tried = st
		m.reloadFiles(st)
	}
	if m.status.Source == SourceSelfSigned && !m.now().Before(m.renewAt) {
		if err := m.useSelfSigned(); err != nil {
			m.log.Error("guest TLS: can't renew the self-signed certificate, keeping the old one", "err", err)
			// Try again at the next handshake after a minute, not at
			// every one.
			m.renewAt = m.now().Add(time.Minute)
		}
	}
}

// reloadFiles reads the pair after a change seen in st. It logs what
// happened and keeps the current certificate on failure.
func (m *Manager) reloadFiles(st stamp) {
	if !st.bothExist() {
		if m.status.Source == SourceFiles {
			m.log.Warn("guest TLS: certfile or keyfile is gone, keeping the certificate in use",
				"cert_found", st.cert.exists, "key_found", st.key.exists)
		}
		return
	}
	c, err := m.loadFiles()
	if err != nil {
		m.log.Error("guest TLS: certfile and keyfile changed but do not load, keeping the certificate in use",
			"source", m.status.Source, "err", err)
		return
	}
	m.use(c, SourceFiles)
	m.log.Info("guest TLS: loaded certfile and keyfile", "sha256", m.status.Fingerprint, "not_after", m.status.NotAfter)
}

// stat reads the size and modification time of both files.
func (m *Manager) stat() stamp {
	return stamp{cert: m.statFile(m.o.CertFile), key: m.statFile(m.o.KeyFile)}
}

func (m *Manager) statFile(name string) fileStamp {
	if m.root == nil {
		return fileStamp{}
	}
	fi, err := m.root.Stat(name)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, size: fi.Size(), modTime: fi.ModTime()}
}

// loadFiles reads and checks the pair in the ssl folder.
func (m *Manager) loadFiles() (*tls.Certificate, error) {
	if m.root == nil {
		return nil, errors.New("the ssl folder can't be opened")
	}
	certPEM, err := m.root.ReadFile(m.o.CertFile)
	if err != nil {
		return nil, fmt.Errorf("read certfile: %w", err)
	}
	keyPEM, err := m.root.ReadFile(m.o.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("read keyfile: %w", err)
	}
	return parsePair(certPEM, keyPEM)
}

// parsePair parses a PEM pair and checks that the key matches.
func parsePair(certPEM, keyPEM []byte) (*tls.Certificate, error) {
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if c.Leaf == nil {
		if c.Leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

// use makes c the certificate in use. The caller holds m.mu.
func (m *Manager) use(c *tls.Certificate, src Source) {
	m.cert = c
	m.status = Status{Source: src, Fingerprint: Fingerprint(c.Leaf.Raw), NotAfter: c.Leaf.NotAfter}
}

// useSelfSigned loads the self-signed pair from the data dir, or makes a
// new one when it is missing, broken or near its end. The caller holds
// m.mu.
func (m *Manager) useSelfSigned() error {
	certPath := filepath.Join(m.o.DataDir, migrate.SelfSignedCertFile)
	keyPath := filepath.Join(m.o.DataDir, migrate.SelfSignedKeyFile)
	now := m.now()
	c, err := loadPair(certPath, keyPath)
	switch {
	case err == nil && now.Before(m.renewTime(c.Leaf)):
		m.useSelf(c)
		m.log.Info("guest TLS: using the self-signed certificate", "sha256", m.status.Fingerprint, "not_after", m.status.NotAfter)
		return nil
	case err == nil:
		m.log.Info("guest TLS: the self-signed certificate is near its end, making a new one", "not_after", c.Leaf.NotAfter)
	case errors.Is(err, fs.ErrNotExist):
	default:
		m.log.Warn("guest TLS: the self-signed certificate does not load, making a new one", "err", err)
	}
	certPEM, keyPEM, err := SelfSigned(now, m.o.Lifetime)
	if err != nil {
		return fmt.Errorf("make self-signed certificate: %w", err)
	}
	if err := os.MkdirAll(m.o.DataDir, 0o700); err != nil {
		return err
	}
	// The key goes first. A crash between the two writes leaves a pair
	// that does not match, and the next start makes a new one.
	if err := store.WriteSecretFileAtomic(keyPath, keyPEM); err != nil {
		return fmt.Errorf("save %s: %w", keyPath, err)
	}
	if err := store.WriteSecretFileAtomic(certPath, certPEM); err != nil {
		return fmt.Errorf("save %s: %w", certPath, err)
	}
	if c, err = parsePair(certPEM, keyPEM); err != nil {
		return err
	}
	m.useSelf(c)
	m.log.Info("guest TLS: made a self-signed certificate", "sha256", m.status.Fingerprint, "not_after", m.status.NotAfter)
	return nil
}

func (m *Manager) useSelf(c *tls.Certificate) {
	m.use(c, SourceSelfSigned)
	m.renewAt = m.renewTime(c.Leaf)
}

// renewTime is when a self-signed certificate is made again: when a third
// of the configured lifetime is left.
func (m *Manager) renewTime(leaf *x509.Certificate) time.Time {
	return leaf.NotAfter.Add(-m.o.Lifetime / 3)
}

func loadPair(certPath, keyPath string) (*tls.Certificate, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	return parsePair(certPEM, keyPEM)
}

// SelfSigned makes a self-signed ECDSA P-256 server certificate valid from
// now minus an hour to now plus lifetime. It returns the certificate and
// its PKCS #8 key, as PEM.
func SelfSigned(now time.Time, lifetime time.Duration) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: CommonName},
		DNSNames:              []string{CommonName},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(lifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// Fingerprint is the SHA-256 of der as uppercase hex pairs split by
// colons, the form of openssl x509 -fingerprint -sha256.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b bytes.Buffer
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}
