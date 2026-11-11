package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/config"
)

// With ssl on, the guest port serves TLS itself (r.TLS is set). The
// cookie's Secure flag, the diagnostics' https field and the same-origin
// check must follow, with or without a trusted proxy in front.
func TestDirectTLSRequests(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.TrustedProxies = []string{"192.0.2.50"} })
	l := h.link("guest_room")
	hash, _ := access.HashPassword("pw")
	if _, err := h.links.AddPassword(l.ID, "Anna", hash); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		peer      string
		tls       bool
		headers   map[string]string
		origin    string
		wantCode  int
		wantHTTPS bool
	}{
		{"direct TLS", "203.0.113.10:1", true, nil, "https://guest.test", 200, true},
		{"direct TLS, http origin on the same host", "203.0.113.10:1", true, nil, "http://guest.test", 200, true},
		{"direct TLS, forged proto http from an untrusted peer", "203.0.113.10:1", true,
			map[string]string{"X-Forwarded-Proto": "http"}, "https://guest.test", 200, true},
		{"trusted proxy over TLS, no proto", "192.0.2.50:1", true,
			map[string]string{"X-Forwarded-For": "203.0.113.9"}, "https://guest.test", 200, true},
		{"trusted proxy over TLS, proto https", "192.0.2.50:1", true,
			map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"}, "https://guest.test", 200, true},
		{"trusted proxy over TLS, proto http", "192.0.2.50:1", true,
			map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "http"}, "http://guest.test", 200, false},
		{"trusted proxy over plain HTTP, proto https", "192.0.2.50:1", false,
			map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"}, "https://guest.test", 200, true},
		{"direct TLS, other host", "203.0.113.10:1", true, nil, "https://evil.test", 403, true},
		{"direct TLS, same host on another port", "203.0.113.10:1", true, nil, "https://guest.test:8443", 403, true},
		{"direct TLS, no origin", "203.0.113.10:1", true, nil, "-", 403, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": "pw"},
				peer: c.peer, tls: c.tls, headers: c.headers, origin: c.origin})
			if rec.Code != c.wantCode {
				t.Fatalf("code %d, want %d: %s", rec.Code, c.wantCode, rec.Body)
			}
			if c.wantCode == http.StatusOK {
				ck := cookie(rec, sessionCookie)
				if ck == nil || ck.Secure != c.wantHTTPS || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode {
					t.Errorf("cookie = %+v, want Secure %v", ck, c.wantHTTPS)
				}
			}
			d := decode[diagnosticsView](t, h.a(req{method: "GET", path: "/api/diagnostics"}))
			if len(d.Requests) == 0 || d.Requests[0].HTTPS != c.wantHTTPS {
				t.Errorf("diagnostics https = %+v, want %v", d.Requests, c.wantHTTPS)
			}
		})
	}
}

// GET /api/settings shows the guest TLS state. Without a TLSStatus dep it
// is off, with the file options shown.
func TestSettingsTLS(t *testing.T) {
	h := newHarness(t)
	v := decode[settingsView](t, h.a(req{method: "GET", path: "/api/settings"}))
	if want := (TLSStatus{CertFile: "fullchain.pem", KeyFile: "privkey.pem", SelfSignedDays: 365}); v.TLS != want {
		t.Errorf("off: %+v, want %+v", v.TLS, want)
	}
	raw := h.a(req{method: "GET", path: "/api/settings"}).Body.String()
	for _, k := range []string{`"fingerprint"`, `"not_after"`, `"source"`} {
		if strings.Contains(raw, k) {
			t.Errorf("off: settings hold %s: %s", k, raw)
		}
	}

	on := TLSStatus{Enabled: true, Source: "self-signed", Fingerprint: "AB:CD", NotAfter: t0.Add(365 * 24 * time.Hour),
		CertFile: "fullchain.pem", KeyFile: "privkey.pem", SelfSignedDays: 365}
	h = newHarness(t, func(_ *config.Options, d *Deps) { d.TLSStatus = func() TLSStatus { return on } })
	v = decode[settingsView](t, h.a(req{method: "GET", path: "/api/settings"}))
	if !v.TLS.NotAfter.Equal(on.NotAfter) {
		t.Errorf("not after %v", v.TLS.NotAfter)
	}
	v.TLS.NotAfter = on.NotAfter
	if v.TLS != on {
		t.Errorf("on: %+v, want %+v", v.TLS, on)
	}
}
