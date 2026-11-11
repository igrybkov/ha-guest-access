package access

// Security review tests. Each one locks in behavior that was reviewed and
// found safe.

import (
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Forwarding headers from an untrusted peer change nothing. From a trusted
// peer only the right-most untrusted entry counts, and anything doubtful
// is unresolved and never local.
func TestSecClientSpoofing(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("192.0.2.50/32")}
	local := []netip.Prefix{netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("fc00::/7")}
	cases := []struct {
		name       string
		peer       string
		hdr        map[string][]string
		wantIP     string
		wantLocal  bool
		wantHTTPS  bool
		unresolved bool
	}{
		{"untrusted peer, spoofed local XFF", "203.0.113.5:1", map[string][]string{"X-Forwarded-For": {"192.168.1.5"}, "X-Forwarded-Proto": {"https"}}, "203.0.113.5", false, false, false},
		{"untrusted peer is local", "192.168.1.9:1", map[string][]string{"X-Forwarded-For": {"203.0.113.5"}}, "192.168.1.9", true, false, false},
		{"leftmost spoof ignored", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"192.168.1.5, 203.0.113.9"}}, "203.0.113.9", false, false, false},
		{"two header lines, spoof first", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"192.168.1.5", "203.0.113.9"}}, "203.0.113.9", false, false, false},
		{"real local client", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"203.0.113.9, 192.168.1.5"}}, "192.168.1.5", true, false, false},
		{"4in6 client is local", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"::ffff:192.168.1.5"}}, "192.168.1.5", true, false, false},
		{"4in6 proxy peer", "[::ffff:192.0.2.50]:1", map[string][]string{"X-Forwarded-For": {"203.0.113.9"}}, "203.0.113.9", false, false, false},
		{"garbage", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"192.168.1.5, unknown"}}, "192.0.2.50", false, false, true},
		{"garbage after good", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"unknown, 192.168.1.5"}}, "192.168.1.5", true, false, false},
		{"empty entry", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"192.168.1.5,,203.0.113.9"}}, "203.0.113.9", false, false, false},
		{"zone on v4", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"192.168.1.5%eth0"}}, "192.0.2.50", false, false, true},
		{"no XFF", "192.0.2.50:1", nil, "192.0.2.50", false, false, true},
		{"only trusted hops", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"192.0.2.50, 192.0.2.50"}}, "192.0.2.50", false, false, true},
		{"other headers ignored", "192.0.2.50:1", map[string][]string{"Forwarded": {"for=192.168.1.5"}, "X-Real-Ip": {"192.168.1.5"}, "Cf-Connecting-Ip": {"192.168.1.5"}}, "192.0.2.50", false, false, true},
		{"https from proxy", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"203.0.113.9"}, "X-Forwarded-Proto": {"https"}}, "203.0.113.9", false, true, false},
		{"proto spoof then proxy http", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"203.0.113.9"}, "X-Forwarded-Proto": {"https, http"}}, "203.0.113.9", false, false, false},
		{"ula client", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"fd00::1"}}, "fd00::1", true, false, false},
		{"bracketed port", "192.0.2.50:1", map[string][]string{"X-Forwarded-For": {"[fd00::1]:4433"}}, "fd00::1", true, false, false},
		{"bad peer", "bogus", map[string][]string{"X-Forwarded-For": {"192.168.1.5"}}, "", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			for k, v := range tc.hdr {
				r.Header[k] = v
			}
			c := ClientFrom(r, trusted)
			got := ""
			if c.IP.IsValid() {
				got = c.IP.String()
			}
			if got != tc.wantIP || c.Local(local) != tc.wantLocal || c.HTTPS != tc.wantHTTPS || c.Unresolved != tc.unresolved {
				t.Errorf("got ip=%q local=%v https=%v unresolved=%v", got, c.Local(local), c.HTTPS, c.Unresolved)
			}
		})
	}
	// A very long list neither panics nor takes long.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.50:1"
	r.Header.Set("X-Forwarded-For", strings.Repeat("192.0.2.50, ", 50000)+"203.0.113.9")
	start := time.Now()
	if c := ClientFrom(r, trusted); c.IP.String() != "203.0.113.9" || time.Since(start) > time.Second {
		t.Errorf("long list: %v in %v", c.IP, time.Since(start))
	}
}

// A token passes only the check of its own purpose, link and id, and no
// edit of it passes.
func TestSecSignerBinding(t *testing.T) {
	s := NewSigner([]byte(strings.Repeat("k", 32)))
	other := NewSigner([]byte(strings.Repeat("j", 32)))
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	exp := now.Add(time.Hour)
	checks := map[string]func(sg *Signer, link, tok string) bool{
		"location": func(sg *Signer, link, tok string) bool { return sg.Verify(link, tok, now) },
		"session":  func(sg *Signer, link, tok string) bool { _, ok := sg.VerifySession(link, tok, now); return ok },
		"device":   func(sg *Signer, link, tok string) bool { _, _, ok := sg.VerifyDevice(link, tok, now); return ok },
		"near":     func(sg *Signer, link, tok string) bool { _, ok := sg.VerifyNear(link, tok, now); return ok },
	}
	tokens := map[string]string{
		"location": s.Sign("link1", exp), "session": s.SignSession("link1", "pw1", exp),
		"device": s.SignDevice("link1", "pw1", exp), "near": s.SignNear("link1", 5, exp),
	}
	for kind, tok := range tokens {
		for want, check := range checks {
			if got := check(s, "link1", tok); got != (kind == want) {
				t.Errorf("%s token at the %s check: %v", kind, want, got)
			}
		}
		check := checks[kind]
		if check(s, "link2", tok) || check(other, "link1", tok) {
			t.Errorf("%s token passes for another link or key", kind)
		}
		if check(s, "link1", tok+"A") || check(s, "link1", tok[:len(tok)-1]) || check(s, "link1", tok+"=") || check(s, "link1", "") {
			t.Errorf("%s token passes edited", kind)
		}
		if !check(s, "link1", tok) {
			t.Errorf("%s token does not pass its own check", kind)
		}
		later := now.Add(time.Hour)
		if _, ok := s.verify(purposeOf(kind), "link1", tok, later); ok && kind != "near" {
			t.Errorf("%s token passes at its expiry", kind)
		}
		const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
		for i := range len(tok) {
			for _, c := range []byte{alpha[(strings.IndexByte(alpha, tok[i])+1)%64]} {
				edited := tok[:i] + string(c) + tok[i+1:]
				if check(s, "link1", edited) {
					t.Fatalf("%s token with byte %d changed still passes", kind, i)
				}
			}
		}
	}
	// A session for pw1 is not a session for pw1 plus a suffix.
	if id, ok := s.VerifySession("link1", tokens["session"], now); !ok || id != "pw1" {
		t.Errorf("session id = %q %v", id, ok)
	}
	if strings.ContainsAny(tokens["session"], "+/=") {
		t.Errorf("token is not base64url: %q", tokens["session"])
	}
}

func purposeOf(kind string) byte {
	switch kind {
	case "session":
		return purposeSession
	case "device":
		return purposeDevice
	case "near":
		return purposeNear
	}
	return purposeLocation
}

// Parallel guesses all pass Allow before any Fail lands. The guard still
// lets through no more than its limits.
func TestSecGuardParallelAllow(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	t.Run("one address", func(t *testing.T) {
		g := NewPasswordGuard(GuardOptions{})
		var n atomic.Int32
		var wg sync.WaitGroup
		for range 300 {
			wg.Go(func() {
				if ok, _ := g.Allow("l", netip.MustParseAddr("203.0.113.1"), now); ok {
					n.Add(1)
				}
			})
		}
		wg.Wait()
		if n.Load() > GuardPerIPFails {
			t.Fatalf("%d parallel tries passed, limit %d", n.Load(), GuardPerIPFails)
		}
	})
	t.Run("many addresses", func(t *testing.T) {
		g := NewPasswordGuard(GuardOptions{})
		var n atomic.Int32
		var wg sync.WaitGroup
		for i := range 300 {
			wg.Go(func() {
				if ok, _ := g.Allow("l", netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}), now); ok {
					n.Add(1)
				}
			})
		}
		wg.Wait()
		if n.Load() > GuardPerLinkFails {
			t.Fatalf("%d parallel tries passed, limit %d", n.Load(), GuardPerLinkFails)
		}
	})
}
