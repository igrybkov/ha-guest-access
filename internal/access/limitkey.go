package access

import "net/netip"

// DefaultIPv6Prefix is the IPv6 prefix length that rate limits key on.
// An ISP gives a home or a phone a whole /64, and the device can pick any
// address in it, so one /64 counts as one client.
const DefaultIPv6Prefix = 64

// KeyAddr returns the address that rate limits and the password guard key
// ip on. An IPv4 address is itself. An IPv6 address is cut to its first
// v6Bits bits, with the rest zero. A v6Bits outside 1 to 128 means
// DefaultIPv6Prefix. IPv4-mapped IPv6 counts as IPv4 and a zone is
// dropped. An invalid ip stays invalid.
//
// Logs, audit records and HA events name the full address. Only the
// limiter keys use this one.
func KeyAddr(ip netip.Addr, v6Bits int) netip.Addr {
	ip = normalize(ip)
	if !ip.Is6() {
		return ip
	}
	if v6Bits < 1 || v6Bits > 128 {
		v6Bits = DefaultIPv6Prefix
	}
	p, err := ip.Prefix(v6Bits)
	if err != nil {
		return ip
	}
	return p.Addr()
}

// LimitKey returns KeyAddr(ip, v6Bits) as a limiter key: the address, or
// "invalid" for an invalid ip, so every unparsable peer shares one
// bucket.
func LimitKey(ip netip.Addr, v6Bits int) string {
	a := KeyAddr(ip, v6Bits)
	if !a.IsValid() {
		return "invalid"
	}
	return a.String()
}
