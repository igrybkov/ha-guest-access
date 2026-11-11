package access

import (
	"errors"
	"math"
	"strings"
	"testing"
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

func TestValidCustomSlug(t *testing.T) {
	for _, tc := range []struct {
		slug string
		ok   bool
		why  string // part of the error, when not ok
	}{
		{"guest-room-2025", true, ""},
		{"abcdefghijkl", true, ""},          // exactly 12
		{strings.Repeat("a", 64), true, ""}, // exactly 64
		{"0123456789ab", true, ""},
		{"a-b-c-d-e-f-g", true, ""},
		{"abcdefghijk", false, "shorter"}, // 11
		{"", false, "shorter"},
		{strings.Repeat("a", 65), false, "longer"},
		{"-guest-room-2025", false, "starts or ends"},
		{"guest-room-2025-", false, "starts or ends"},
		{"guest--room-2025", false, "--"},
		{"Guest-room-2025", false, "not allowed"},
		{"guest_room_2025", false, "not allowed"},
		{"guest room 2025", false, "not allowed"},
		{"guest.room.2025", false, "not allowed"},
		{"guest/room/2025", false, "not allowed"},
		{"guest-room-%2e%2e", false, "not allowed"},
		{"guest-room-caf\u00e9", false, "not allowed"},
		{"guest-room-\x00abc", false, "not allowed"},
	} {
		err := ValidCustomSlug(tc.slug)
		if tc.ok {
			if err != nil {
				t.Errorf("ValidCustomSlug(%q) = %v", tc.slug, err)
			}
			continue
		}
		if !errors.Is(err, ErrBadSlug) || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("ValidCustomSlug(%q) = %v, want ErrBadSlug with %q", tc.slug, err, tc.why)
		}
	}
	// Every random slug is not a valid custom one (it may hold "_" or
	// upper case), but a lower-case hex slug of the right length is.
	if err := ValidCustomSlug(HashSlug("x")[:32]); err != nil {
		t.Errorf("hex slug: %v", err)
	}
}
