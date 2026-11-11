package access

import (
	"net/http"
	"net/netip"
	"strings"
)

// Client is how the app sees one request.
type Client struct {
	// IP is the client address. It is the peer, unless the peer is a
	// trusted proxy; then it comes from X-Forwarded-For (see ClientFrom).
	// It is never zoned and never IPv4-mapped IPv6. It is invalid when the
	// peer address does not parse, and an invalid IP is never local.
	IP netip.Addr
	// HTTPS is true when the request came over TLS, directly or per the
	// trusted proxy's X-Forwarded-Proto.
	HTTPS bool
	// ViaProxy is true when the peer is a trusted proxy, so the forwarding
	// headers were used.
	ViaProxy bool
	// Unresolved is true when the peer is trusted but no untrusted client
	// address was found: no X-Forwarded-For, garbage in it, or only
	// trusted hops. IP is then a proxy's own address. It still keys rate
	// limits, but it must never count as local.
	Unresolved bool
}

// Local reports whether the client is in one of nets. An unresolved
// client is never local: a proxy that drops or mangles X-Forwarded-For
// would otherwise make every internet request look local. Use this, not
// IsLocal(c.IP, ...), for access decisions.
func (c Client) Local(nets []netip.Prefix) bool {
	return !c.Unresolved && IsLocal(c.IP, nets)
}

// ClientFrom derives the Client of r.
//
// The peer is r.RemoteAddr. When the peer is not in trusted (and with
// trusted empty, it never is), every forwarding header is ignored: the
// client is the peer and HTTPS means r.TLS is set.
//
// When the peer is trusted, X-Forwarded-For is read from the right. All
// header lines count as one comma list, in the order received. Each
// trusted entry is a proxy hop and is skipped. The client is the first
// entry that is not trusted. Two edge cases:
//
//   - An entry that does not parse stops the walk. The client is then the
//     last trusted hop seen (the peer, if none). Anything left of garbage
//     came through a hop we can't vouch for, so we never trust it.
//   - When every entry is trusted, the client is the left-most one.
//
// In both cases, and with no X-Forwarded-For at all, IP is a trusted hop
// and Unresolved is true, so Local says no.
//
// HTTPS then comes from the right-most X-Forwarded-Proto value, the one
// set by the nearest proxy. Without that header, r.TLS decides.
//
// Forwarded, X-Real-IP and CF-Connecting-IP are ignored on purpose. Our
// proxy chain always sets X-Forwarded-For, and each extra source is one
// more way to spoof the client.
func ClientFrom(r *http.Request, trusted []netip.Prefix) Client {
	peer := parsePeer(r.RemoteAddr)
	direct := Client{IP: peer, HTTPS: r.TLS != nil}
	if !peer.IsValid() || !IsLocal(peer, trusted) {
		return direct
	}
	c := Client{IP: peer, HTTPS: r.TLS != nil, ViaProxy: true, Unresolved: true}
	if xff := headerList(r.Header, "X-Forwarded-For"); len(xff) > 0 {
		var found bool
		c.IP, found = walkXFF(xff, peer, trusted)
		c.Unresolved = !found
	}
	if xfp := headerList(r.Header, "X-Forwarded-Proto"); len(xfp) > 0 {
		c.HTTPS = strings.EqualFold(xfp[len(xfp)-1], "https")
	}
	return c
}

// walkXFF returns the client from a forwarded-for list sent by a trusted
// peer. found is false when the walk ends on a trusted hop. See
// ClientFrom for the rules.
func walkXFF(xff []string, peer netip.Addr, trusted []netip.Prefix) (ip netip.Addr, found bool) {
	last := peer // the last trusted hop seen
	for i := len(xff) - 1; i >= 0; i-- {
		ip, ok := parseHop(xff[i])
		if !ok {
			return last, false
		}
		if !IsLocal(ip, trusted) {
			return ip, true
		}
		last = ip
	}
	return last, false
}

// headerList joins every line of header name into one comma list. Each
// entry is trimmed of spaces. Empty lines add nothing, but an empty entry
// inside a line (as in "a,,b") stays, so it reads as garbage.
func headerList(h http.Header, name string) []string {
	var out []string
	for _, line := range h.Values(name) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, e := range strings.Split(line, ",") {
			out = append(out, strings.TrimSpace(e))
		}
	}
	return out
}

// parsePeer parses r.RemoteAddr: "ip:port", "[v6]:port", or a bare IP.
func parsePeer(s string) netip.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return normalize(ap.Addr())
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return normalize(a)
	}
	return netip.Addr{}
}

// parseHop parses one X-Forwarded-For entry. It takes a bare IP, an IP
// with a port ("1.2.3.4:80", "[::1]:80"), or a bracketed IPv6 ("[::1]").
func parseHop(s string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(s); err == nil {
		return normalize(a), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return normalize(ap.Addr()), true
	}
	if len(s) > 2 && s[0] == '[' && s[len(s)-1] == ']' {
		if a, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil && a.Is6() {
			return normalize(a), true
		}
	}
	return netip.Addr{}, false
}

// normalize drops the zone and unmaps IPv4-mapped IPv6, so prefix checks
// work. netip.Prefix.Contains is false for any zoned address.
func normalize(a netip.Addr) netip.Addr {
	return a.WithZone("").Unmap()
}

// normalizePrefix turns an IPv4-mapped prefix such as ::ffff:10.0.0.0/104
// into its IPv4 form, 10.0.0.0/8. Other prefixes pass through.
func normalizePrefix(p netip.Prefix) netip.Prefix {
	a := p.Addr()
	if !a.Is4In6() || p.Bits() < 96 {
		return p
	}
	return netip.PrefixFrom(a.Unmap(), p.Bits()-96)
}

// IsLocal reports whether ip is in one of nets. An invalid ip is never
// local. IPv4-mapped IPv6 addresses count as their IPv4 address, and a
// zone is ignored.
func IsLocal(ip netip.Addr, nets []netip.Prefix) bool {
	if !ip.IsValid() {
		return false
	}
	ip = normalize(ip)
	for _, n := range nets {
		if n.IsValid() && normalizePrefix(n).Contains(ip) {
			return true
		}
	}
	return false
}
