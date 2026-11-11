package access

import (
	"net/netip"
	"testing"
	"time"
)

func TestKeyAddr(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		bits int
		want string
	}{
		{"203.0.113.5", 64, "203.0.113.5"},
		{"203.0.113.5", 48, "203.0.113.5"}, // IPv4 is never cut
		{"::ffff:203.0.113.5", 64, "203.0.113.5"},
		{"2001:db8:1:2::1", 64, "2001:db8:1:2::"},
		{"2001:db8:1:2:aaaa:bbbb:cccc:dddd", 64, "2001:db8:1:2::"},
		{"2001:db8:1:2:aaaa:bbbb:cccc:dddd", 56, "2001:db8:1::"},
		{"2001:db8:1:2:aaaa:bbbb:cccc:dddd", 128, "2001:db8:1:2:aaaa:bbbb:cccc:dddd"},
		{"2001:db8:1:2::1", 0, "2001:db8:1:2::"},   // out of range: the default
		{"2001:db8:1:2::1", 129, "2001:db8:1:2::"}, // out of range: the default
		{"fe80::1%eth0", 64, "fe80::"},
		{"::1", 64, "::"},
	} {
		got := KeyAddr(netip.MustParseAddr(tc.ip), tc.bits)
		if got.String() != tc.want {
			t.Errorf("KeyAddr(%s, %d) = %s, want %s", tc.ip, tc.bits, got, tc.want)
		}
	}
	if k := LimitKey(netip.Addr{}, 64); k != "invalid" {
		t.Errorf("invalid ip key = %q", k)
	}
	if a, b := LimitKey(netip.MustParseAddr("2001:db8::1"), 64), LimitKey(netip.MustParseAddr("2001:db8::ffff:1"), 64); a != b {
		t.Errorf("one /64, two keys: %s %s", a, b)
	}
	if a, b := LimitKey(netip.MustParseAddr("2001:db8:0:1::1"), 64), LimitKey(netip.MustParseAddr("2001:db8:0:2::1"), 64); a == b {
		t.Errorf("two /64s, one key: %s", a)
	}
}

// Wrong passwords from many addresses of one /64 count as one client:
// the per-IP block hits the whole prefix.
func TestGuardKeysIPv6Prefix(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	g := NewPasswordGuard(GuardOptions{PerIPFails: 3, PerLinkFails: 1000})
	for i := range 3 {
		ip := netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 1, 0, 2, 0, 0, 0, 0, 0, 0, 0, byte(i + 1)})
		if ok, _, _ := g.Allow("l", ip, now); !ok {
			t.Fatalf("try %d refused", i)
		}
		g.Fail("l", ip, now)
	}
	if ok, _, _ := g.Allow("l", netip.MustParseAddr("2001:db8:1:2::abcd"), now); ok {
		t.Error("a new address in the blocked /64 got a try")
	}
	if ok, _, _ := g.Allow("l", netip.MustParseAddr("2001:db8:1:3::1"), now); !ok {
		t.Error("another /64 is blocked too")
	}
	// With a /128 prefix every address is its own client.
	g = NewPasswordGuard(GuardOptions{PerIPFails: 1, PerLinkFails: 1000, IPv6Prefix: 128})
	a, b := netip.MustParseAddr("2001:db8:1:2::1"), netip.MustParseAddr("2001:db8:1:2::2")
	g.Allow("l", a, now)
	g.Fail("l", a, now)
	if ok, _, _ := g.Allow("l", b, now); !ok {
		t.Error("prefix 128: a neighbor address is blocked")
	}
}
