package access

import (
	"math"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestDistanceM(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		lat1, lon1, lat2, lon2 float64
		want, tol              float64
	}{
		{"same point", 41.88, -87.63, 41.88, -87.63, 0, 0.001},
		{"one degree of latitude", 0, 0, 1, 0, 111_195, 10},
		{"Chicago to Milwaukee", 41.8781, -87.6298, 43.0389, -87.9065, 130_900, 1_000},
		{"antipodes", 0, 0, 0, 180, math.Pi * earthRadiusM, 1},
	} {
		got := DistanceM(tc.lat1, tc.lon1, tc.lat2, tc.lon2)
		if math.Abs(got-tc.want) > tc.tol {
			t.Errorf("%s: %.1f m, want %.1f ± %.0f", tc.name, got, tc.want, tc.tol)
		}
	}
}

func TestNewSlug(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		s := NewSlug()
		if len(s) != 22 {
			t.Fatalf("slug %q has %d characters", s, len(s))
		}
		for _, c := range s {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				t.Fatalf("slug %q has %q", s, c)
			}
		}
		if seen[s] {
			t.Fatalf("slug %q repeated", s)
		}
		seen[s] = true
	}
}

func TestHashSlug(t *testing.T) {
	// sha256("abc")
	if got := HashSlug("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("HashSlug = %s", got)
	}
	if HashSlug("abc") == HashSlug("ABC") {
		t.Fatal("hash ignores case")
	}
}

// The stubs fail closed until wave 1 fills them. Replace this test with
// real table tests when you implement them.
func TestStubsFailClosed(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if c := ClientFrom(r, nil); IsLocal(c.IP, []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}) {
		t.Error("stub client is local")
	}
	s := NewSigner([]byte("k"))
	now := time.Now()
	if s.Verify("l1", s.Sign("l1", now.Add(time.Minute)), now) {
		t.Error("stub signer verifies")
	}
	if ValidCustomSlug("a-long-custom-slug") == nil {
		t.Error("stub accepts a slug")
	}
	if NewLimiter(10, time.Minute).Allow("k", now) {
		t.Error("stub limiter allows")
	}
}
