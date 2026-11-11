package config

import (
	"strings"
	"testing"
	"time"
)

// certfile and keyfile name files inside /ssl. Anything that can leave
// the folder is refused.
func TestSSLFileOK(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{"fullchain.pem", true},
		{"privkey.pem", true},
		{"live/example/fullchain.pem", true},
		{"./fullchain.pem", true},
		{"a/../b.pem", true},
		{"", false},
		{".", false},
		{"..", false},
		{"../fullchain.pem", false},
		{"live/../../etc/passwd", false},
		{"/ssl/fullchain.pem", false},
		{"/etc/passwd", false},
		{"/", false},
		{"\x00", false},
		{"cert\x00.pem", false},
	} {
		if got := SSLFileOK(c.in); got != c.ok {
			t.Errorf("SSLFileOK(%q) = %v, want %v", c.in, got, c.ok)
		}
	}
}

func FuzzSSLFileOK(f *testing.F) {
	for _, s := range []string{"fullchain.pem", "../x", "/ssl/x", "a/../../b", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !SSLFileOK(s) {
			return
		}
		if strings.HasPrefix(s, "/") || s == ".." || strings.HasPrefix(s, "../") || strings.Contains(s, "\x00") {
			t.Fatalf("SSLFileOK(%q) = true", s)
		}
	})
}

func TestTLSOptions(t *testing.T) {
	d := DefaultOptions().TLS
	if d.Enabled || d.CertFile != "fullchain.pem" || d.KeyFile != "privkey.pem" || d.SelfSignedLifetime != 365*24*time.Hour {
		t.Errorf("defaults = %+v", d)
	}
	for _, c := range []struct {
		name, raw string
		errKeys   []string // keys the error must name; none = valid
	}{
		{"on with defaults", `{"ssl": true}`, nil},
		{"on with sub folder", `{"ssl": true, "certfile": "live/cert.pem", "keyfile": "live/key.pem"}`, nil},
		{"off ignores bad names", `{"ssl": false, "certfile": "../x", "keyfile": "/etc/passwd"}`, nil},
		{"on with dot dot", `{"ssl": true, "certfile": "../etc/cert.pem"}`, []string{"certfile"}},
		{"on with absolute", `{"ssl": true, "keyfile": "/ssl/privkey.pem"}`, []string{"keyfile"}},
		{"on with empty", `{"ssl": true, "certfile": "", "keyfile": ""}`, []string{"certfile", "keyfile"}},
		{"lifetime too low", `{"ssl_self_signed_days": 0}`, []string{"ssl_self_signed_days"}},
		{"lifetime too high", `{"ssl_self_signed_days": 3651}`, []string{"ssl_self_signed_days"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := parseOptions([]byte(c.raw))
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.options()
			if len(c.errKeys) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("no error")
			}
			for _, k := range c.errKeys {
				if !strings.Contains(err.Error(), k+":") {
					t.Errorf("error does not name %s: %v", k, err)
				}
			}
		})
	}
	r, _ := parseOptions([]byte(`{"ssl": true, "certfile": "a.pem", "keyfile": "b.pem", "ssl_self_signed_days": 30}`))
	o, err := r.options()
	if err != nil {
		t.Fatal(err)
	}
	if want := (TLSOptions{Enabled: true, CertFile: "a.pem", KeyFile: "b.pem", SelfSignedLifetime: 30 * 24 * time.Hour}); o.TLS != want {
		t.Errorf("TLS = %+v, want %+v", o.TLS, want)
	}
}

// The ssl folder holds private keys of other apps too. The app only
// reads it.
func TestSecManifestMapsSSLReadOnly(t *testing.T) {
	m := readManifest(t)
	maps, _ := m["map"].([]any)
	found := false
	for _, e := range maps {
		switch e := e.(type) {
		case string:
			if e == "ssl:rw" {
				t.Error("ssl is mapped read-write")
			}
			found = found || e == "ssl" || e == "ssl:ro"
		case map[string]any:
			if e["type"] == "ssl" {
				if e["read_only"] == false {
					t.Error("ssl is mapped read-write")
				}
				found = true
			}
		}
	}
	if !found {
		t.Errorf("config.yaml does not map ssl; map = %v", m["map"])
	}
}
