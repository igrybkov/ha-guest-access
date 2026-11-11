package access

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"
)

var testKey = bytes.Repeat([]byte{0x42}, MinKeyLen)

// t0 is a whole second, so Unix-second expiry has no rounding.
var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func TestNewSignerShortKeyPanics(t *testing.T) {
	for _, n := range []int{0, 1, MinKeyLen - 1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewSigner with a %d-byte key did not panic", n)
				}
			}()
			NewSigner(make([]byte, n))
		}()
	}
	NewSigner(make([]byte, MinKeyLen)) // no panic
}

func TestSignerCopiesKey(t *testing.T) {
	k := bytes.Clone(testKey)
	s := NewSigner(k)
	tok := s.Sign("l1", t0.Add(time.Minute))
	k[0] ^= 0xff
	if !s.Verify("l1", tok, t0) {
		t.Error("changing the caller's key slice broke the signer")
	}
}

func TestSignerRoundTrip(t *testing.T) {
	s := NewSigner(testKey)
	exp := t0.Add(15 * time.Minute)
	if !s.Verify("l1", s.Sign("l1", exp), t0) {
		t.Error("location token does not verify")
	}
	pid, ok := s.VerifySession("l1", s.SignSession("l1", "p1", exp), t0)
	if !ok || pid != "p1" {
		t.Errorf("VerifySession = %q, %v", pid, ok)
	}
	tok := s.Sign("l1", exp)
	if len(tok) > maxTokenLen {
		t.Errorf("token is %d bytes", len(tok))
	}
	if _, err := base64.RawURLEncoding.Strict().DecodeString(tok); err != nil {
		t.Errorf("token is not base64url: %v", err)
	}
}

func TestSignerExpiry(t *testing.T) {
	s := NewSigner(testKey)
	exp := t0.Add(time.Minute)
	loc := s.Sign("l1", exp)
	ses := s.SignSession("l1", "p1", exp)
	for _, tc := range []struct {
		now  time.Time
		want bool
	}{
		{t0, true},
		{exp.Add(-time.Nanosecond), true},
		{exp, false},
		{exp.Add(time.Second), false},
		{exp.Add(24 * time.Hour), false},
	} {
		if got := s.Verify("l1", loc, tc.now); got != tc.want {
			t.Errorf("location at %v: %v, want %v", tc.now.Sub(exp), got, tc.want)
		}
		if _, got := s.VerifySession("l1", ses, tc.now); got != tc.want {
			t.Errorf("session at %v: %v, want %v", tc.now.Sub(exp), got, tc.want)
		}
	}
	if s.Verify("l1", s.Sign("l1", t0.Add(-time.Hour)), t0) {
		t.Error("token signed already expired verifies")
	}
}

func TestSignerBinding(t *testing.T) {
	s := NewSigner(testKey)
	exp := t0.Add(time.Hour)
	loc := s.Sign("linkA", exp)
	ses := s.SignSession("linkA", "p1", exp)
	dev := s.SignDevice("linkA", "d1", exp)
	sesD1 := s.SignSession("linkA", "d1", exp) // same id bytes as dev
	other := NewSigner(bytes.Repeat([]byte{0x43}, MinKeyLen))

	checks := []struct {
		name string
		ok   bool
	}{
		{"location for another link", s.Verify("linkB", loc, t0)},
		{"location for a prefix link", s.Verify("link", loc, t0)},
		{"location for an empty link", s.Verify("", loc, t0)},
		{"location used as a session", ok2(s.VerifySession("linkA", loc, t0))},
		{"session used as a location", s.Verify("linkA", ses, t0)},
		{"session for another link", ok2(s.VerifySession("linkB", ses, t0))},
		{"location from another key", other.Verify("linkA", loc, t0)},
		{"session from another key", ok2(other.VerifySession("linkA", ses, t0))},
		{"device used as a location", s.Verify("linkA", dev, t0)},
		{"device used as a session", ok2(s.VerifySession("linkA", dev, t0))},
		{"session used as a device", ok3(s.VerifyDevice("linkA", sesD1, t0))},
		{"location used as a device", ok3(s.VerifyDevice("linkA", loc, t0))},
		{"device for another link", ok3(s.VerifyDevice("linkB", dev, t0))},
		{"device from another key", ok3(other.VerifyDevice("linkA", dev, t0))},
		{"empty device token", ok3(s.VerifyDevice("linkA", "", t0))},
		{"empty token", s.Verify("linkA", "", t0)},
		{"empty session token", ok2(s.VerifySession("linkA", "", t0))},
		{"huge token", s.Verify("linkA", string(bytes.Repeat([]byte("A"), 1<<20)), t0)},
	}
	for _, c := range checks {
		if c.ok {
			t.Errorf("%s: verifies", c.name)
		}
	}
}

// The link and password ids are length-prefixed in the MAC, so moving
// bytes between them gives a different MAC.
func TestSignerNoFieldShift(t *testing.T) {
	s := NewSigner(testKey)
	exp := t0.Add(time.Hour)
	ses := s.SignSession("ab", "c", exp)
	if _, ok := s.VerifySession("a", ses, t0); ok {
		t.Error("shifted link id verifies")
	}
}

func TestSignerTampered(t *testing.T) {
	s := NewSigner(testKey)
	exp := t0.Add(time.Hour)
	for name, tok := range map[string]string{
		"location": s.Sign("l1", exp),
		"session":  s.SignSession("l1", "p1", exp),
		"device":   s.SignDevice("l1", "d1", exp),
	} {
		verify := func(tk string) bool {
			switch name {
			case "location":
				return s.Verify("l1", tk, t0)
			case "device":
				return ok3(s.VerifyDevice("l1", tk, t0))
			}
			_, ok := s.VerifySession("l1", tk, t0)
			return ok
		}
		if !verify(tok) {
			t.Fatalf("%s: clean token fails", name)
		}
		for i := range len(tok) {
			for _, bit := range []byte{0x01, 0x02, 0x20} {
				b := []byte(tok)
				b[i] ^= bit
				if verify(string(b)) {
					t.Errorf("%s: byte %d ^ %#x verifies", name, i, bit)
				}
			}
		}
		for n := range len(tok) {
			if verify(tok[:n]) {
				t.Errorf("%s: truncated to %d verifies", name, n)
			}
		}
		for _, extra := range []string{"A", "AA", "AAAA", "=", "==", ".", "\n", "\r"} {
			if verify(tok + extra) {
				t.Errorf("%s: with %q added verifies", name, extra)
			}
			// The base64 decoder skips \r and \n; the token must not.
			if verify(tok[:5] + extra + tok[5:]) {
				t.Errorf("%s: with %q inside verifies", name, extra)
			}
		}
		// Decoded bytes, flipped one bit at a time and re-encoded.
		raw, _ := base64.RawURLEncoding.DecodeString(tok)
		for i := range raw {
			b := bytes.Clone(raw)
			b[i] ^= 0x80
			if verify(base64.RawURLEncoding.EncodeToString(b)) {
				t.Errorf("%s: raw byte %d flipped verifies", name, i)
			}
		}
	}
}

func TestSignSessionBadIDPanics(t *testing.T) {
	s := NewSigner(testKey)
	for _, id := range []string{"", string(bytes.Repeat([]byte("x"), maxPasswordIDLen+1))} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("SignSession with a %d-byte id did not panic", len(id))
				}
			}()
			s.SignSession("l1", id, t0)
		}()
	}
	long := string(bytes.Repeat([]byte("x"), maxPasswordIDLen))
	if pid, ok := s.VerifySession("l1", s.SignSession("l1", long, t0.Add(time.Hour)), t0); !ok || pid != long {
		t.Error("longest id does not round trip")
	}
}

func ok2(_ string, ok bool) bool { return ok }

func ok3(_ string, _ time.Time, ok bool) bool { return ok }

func TestSignerDevice(t *testing.T) {
	s := NewSigner(testKey)
	exp := t0.Add(48 * time.Hour)
	tok := s.SignDevice("l1", "3f9a1c0d7e2b4a65", exp)
	id, gotExp, ok := s.VerifyDevice("l1", tok, t0)
	if !ok || id != "3f9a1c0d7e2b4a65" || !gotExp.Equal(exp) {
		t.Fatalf("VerifyDevice = %q, %v, %v", id, gotExp, ok)
	}
	if _, _, ok := s.VerifyDevice("l1", tok, exp); ok {
		t.Error("device token verifies at its expiry")
	}
	if _, _, ok := s.VerifyDevice("l1", tok, exp.Add(-time.Second)); !ok {
		t.Error("device token fails a second before its expiry")
	}
	for _, id := range []string{"", string(bytes.Repeat([]byte("x"), maxPasswordIDLen+1))} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("SignDevice with a %d-byte id did not panic", len(id))
				}
			}()
			s.SignDevice("l1", id, exp)
		}()
	}
}

// FuzzVerify feeds random tokens. None may verify, and none may panic.
func FuzzVerify(f *testing.F) {
	s := NewSigner(testKey)
	exp := t0.Add(time.Hour)
	good := s.Sign("l1", exp)
	goodSes := s.SignSession("l1", "p1", exp)
	goodDev := s.SignDevice("l1", "d1", exp)
	for _, seed := range []string{"", "A", good[:len(good)-1], good + "A", goodSes[:10], "AQE", goodDev[:len(goodDev)-2], "AQM"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, tok string) {
		if tok == good || tok == goodSes || tok == goodDev {
			return
		}
		if _, _, ok := s.VerifyDevice("l1", tok, t0); ok {
			t.Fatalf("random device token %q verifies", tok)
		}
		if s.Verify("l1", tok, t0) {
			t.Fatalf("random token %q verifies", tok)
		}
		if _, ok := s.VerifySession("l1", tok, t0); ok {
			t.Fatalf("random session token %q verifies", tok)
		}
	})
}

// Every token kind passes only its own check. Each kind is verified
// against every other kind. The near distance 0x03616263 has the same
// bytes as a session or device id "abc", so the layouts line up and only
// the purpose byte keeps them apart.
func TestSignerPurposesExclusive(t *testing.T) {
	s := NewSigner(testKey)
	exp := t0.Add(time.Hour)
	tokens := map[string]string{
		"location": s.Sign("l1", exp),
		"session":  s.SignSession("l1", "abc", exp),
		"near":     s.SignNear("l1", 0x03616263, exp),
		"device":   s.SignDevice("l1", "abc", exp),
	}
	checks := map[string]func(string) bool{
		"location": func(tk string) bool { return s.Verify("l1", tk, t0) },
		"session":  func(tk string) bool { return ok2(s.VerifySession("l1", tk, t0)) },
		"near":     func(tk string) bool { return okNear(s.VerifyNear("l1", tk, t0)) },
		"device":   func(tk string) bool { return ok3(s.VerifyDevice("l1", tk, t0)) },
	}
	for kind, tk := range tokens {
		for as, check := range checks {
			if got, want := check(tk), kind == as; got != want {
				t.Errorf("%s token checked as %s: %v, want %v", kind, as, got, want)
			}
		}
	}
	purposes := map[byte]bool{}
	for _, p := range []byte{purposeLocation, purposeSession, purposeNear, purposeDevice} {
		if purposes[p] {
			t.Errorf("purpose byte %d used twice", p)
		}
		purposes[p] = true
	}
}
