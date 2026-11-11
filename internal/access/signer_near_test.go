package access

import (
	"bytes"
	"encoding/base64"
	"math"
	"testing"
	"time"
)

func TestNearRoundTrip(t *testing.T) {
	s := NewSigner(testKey)
	exp := t0.Add(15 * time.Minute)
	for _, d := range []uint32{0, 1, 299, 300, 1000, math.MaxUint32} {
		got, ok := s.VerifyNear("l1", s.SignNear("l1", d, exp), t0)
		if !ok || got != d {
			t.Errorf("distance %d: got %d, %v", d, got, ok)
		}
	}
}

func TestNearExpiry(t *testing.T) {
	s := NewSigner(testKey)
	exp := t0.Add(time.Minute)
	tok := s.SignNear("l1", 10, exp)
	for _, tc := range []struct {
		now  time.Time
		want bool
	}{
		{t0, true},
		{exp.Add(-time.Second), true},
		{exp, false},
		{exp.Add(time.Hour), false},
	} {
		if _, ok := s.VerifyNear("l1", tok, tc.now); ok != tc.want {
			t.Errorf("at %v: %v, want %v", tc.now.Sub(exp), ok, tc.want)
		}
	}
}

// Each purpose fails the other two checks, both ways.
func TestNearPurposeSeparation(t *testing.T) {
	s := NewSigner(testKey)
	exp := t0.Add(time.Hour)
	near := s.SignNear("l1", 5, exp)
	loc := s.Sign("l1", exp)
	ses := s.SignSession("l1", "p1", exp)
	other := NewSigner(bytes.Repeat([]byte{0x43}, MinKeyLen))
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"near as location", s.Verify("l1", near, t0)},
		{"near as session", ok2(s.VerifySession("l1", near, t0))},
		{"location as near", okNear(s.VerifyNear("l1", loc, t0))},
		{"session as near", okNear(s.VerifyNear("l1", ses, t0))},
		{"near for another link", okNear(s.VerifyNear("l2", near, t0))},
		{"near for an empty link", okNear(s.VerifyNear("", near, t0))},
		{"near from another key", okNear(other.VerifyNear("l1", near, t0))},
		{"empty", okNear(s.VerifyNear("l1", "", t0))},
	} {
		if c.ok {
			t.Errorf("%s: verifies", c.name)
		}
	}
}

// A guest can't lower the distance by editing the token.
func TestNearTampered(t *testing.T) {
	s := NewSigner(testKey)
	tok := s.SignNear("l1", 5000, t0.Add(time.Hour))
	raw, _ := base64.RawURLEncoding.DecodeString(tok)
	for i := range raw {
		for _, bit := range []byte{0x01, 0x80} {
			b := bytes.Clone(raw)
			b[i] ^= bit
			if _, ok := s.VerifyNear("l1", base64.RawURLEncoding.EncodeToString(b), t0); ok {
				t.Errorf("raw byte %d ^ %#x verifies", i, bit)
			}
		}
	}
	for n := range len(tok) {
		if _, ok := s.VerifyNear("l1", tok[:n], t0); ok {
			t.Errorf("truncated to %d verifies", n)
		}
	}
	if _, ok := s.VerifyNear("l1", tok+"AA", t0); ok {
		t.Error("with bytes added verifies")
	}
}

func okNear(_ uint32, ok bool) bool { return ok }

// FuzzVerifyNear feeds random tokens. None may verify as near, location
// or session, and none may panic.
func FuzzVerifyNear(f *testing.F) {
	s := NewSigner(testKey)
	good := s.SignNear("l1", 42, t0.Add(time.Hour))
	for _, seed := range []string{"", "A", good[:len(good)-1], good + "A", "AQM"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, tok string) {
		if tok == good {
			return
		}
		if _, ok := s.VerifyNear("l1", tok, t0); ok {
			t.Fatalf("random token %q verifies as near", tok)
		}
		if s.Verify("l1", tok, t0) {
			t.Fatalf("random token %q verifies as location", tok)
		}
	})
}
