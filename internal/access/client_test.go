package access

import (
	"crypto/tls"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func prefixes(t testing.TB, ss ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// npm is the Nginx Proxy Manager address, cloudflared sits next to it.
const (
	npm         = "172.30.33.5"
	cloudflared = "172.30.33.9"
)

func TestClientFrom(t *testing.T) {
	trusted := prefixes(t, npm+"/32", cloudflared+"/32")
	for _, tc := range []struct {
		name    string
		peer    string
		tls     bool
		trusted []netip.Prefix
		headers map[string][]string
		want    Client
	}{
		{
			name: "no trusted proxies: peer is the client",
			peer: "203.0.113.9:5555",
			want: Client{IP: netip.MustParseAddr("203.0.113.9")},
		},
		{
			name: "direct TLS",
			peer: "203.0.113.9:5555", tls: true,
			want: Client{IP: netip.MustParseAddr("203.0.113.9"), HTTPS: true},
		},
		{
			name:    "untrusted peer: every forwarding header ignored",
			peer:    "203.0.113.9:5555",
			trusted: trusted,
			headers: map[string][]string{
				"X-Forwarded-For":   {"192.168.1.10"},
				"X-Forwarded-Proto": {"https"},
				"Cf-Connecting-Ip":  {"192.168.1.10"},
				"X-Real-Ip":         {"192.168.1.10"},
				"Forwarded":         {"for=192.168.1.10;proto=https"},
			},
			want: Client{IP: netip.MustParseAddr("203.0.113.9")},
		},
		{
			name:    "untrusted LAN peer: headers still ignored",
			peer:    "192.168.1.50:5555",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"8.8.8.8"}},
			want:    Client{IP: netip.MustParseAddr("192.168.1.50")},
		},
		{
			name:    "trusted NPM: client-forged left part is not used",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"10.0.0.5, 203.0.113.9"}},
			want:    Client{IP: netip.MustParseAddr("203.0.113.9"), ViaProxy: true},
		},
		{
			name:    "cloudflared then NPM",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{
				"X-Forwarded-For":   {"10.0.0.5, 203.0.113.9, " + cloudflared},
				"X-Forwarded-Proto": {"https"},
			},
			want: Client{IP: netip.MustParseAddr("203.0.113.9"), HTTPS: true, ViaProxy: true},
		},
		{
			name:    "multiple XFF lines form one list",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"10.0.0.5", "203.0.113.9", cloudflared}},
			want:    Client{IP: netip.MustParseAddr("203.0.113.9"), ViaProxy: true},
		},
		{
			name:    "multiple XFF lines, client in the first",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"203.0.113.9", cloudflared + ", " + npm}},
			want:    Client{IP: netip.MustParseAddr("203.0.113.9"), ViaProxy: true},
		},
		{
			name:    "garbage right after the peer: the peer is the client",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"10.0.0.5, nonsense"}},
			want:    Client{IP: netip.MustParseAddr(npm), ViaProxy: true, Unresolved: true},
		},
		{
			name:    "garbage behind a trusted hop: the last trusted hop",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"10.0.0.5, unknown, " + cloudflared}},
			want:    Client{IP: netip.MustParseAddr(cloudflared), ViaProxy: true, Unresolved: true},
		},
		{
			name:    "empty entry is garbage",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"10.0.0.5,,"}},
			want:    Client{IP: netip.MustParseAddr(npm), ViaProxy: true, Unresolved: true},
		},
		{
			name:    "garbage left of the client does not matter",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"<script>, 203.0.113.9"}},
			want:    Client{IP: netip.MustParseAddr("203.0.113.9"), ViaProxy: true},
		},
		{
			name:    "IPv6 with brackets and port",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"[2001:db8::1]:4711"}},
			want:    Client{IP: netip.MustParseAddr("2001:db8::1"), ViaProxy: true},
		},
		{
			name:    "IPv6 with brackets, no port",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"[2001:db8::1]"}},
			want:    Client{IP: netip.MustParseAddr("2001:db8::1"), ViaProxy: true},
		},
		{
			name:    "bare IPv6",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"2001:db8::1"}},
			want:    Client{IP: netip.MustParseAddr("2001:db8::1"), ViaProxy: true},
		},
		{
			name:    "IPv4 with port",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"203.0.113.9:4711"}},
			want:    Client{IP: netip.MustParseAddr("203.0.113.9"), ViaProxy: true},
		},
		{
			name:    "bracketed IPv4 is garbage",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"[203.0.113.9]"}},
			want:    Client{IP: netip.MustParseAddr(npm), ViaProxy: true, Unresolved: true},
		},
		{
			name:    "4in6 entry is unmapped",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"::ffff:203.0.113.9"}},
			want:    Client{IP: netip.MustParseAddr("203.0.113.9"), ViaProxy: true},
		},
		{
			name:    "4in6 trusted hop is skipped",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"203.0.113.9, ::ffff:" + cloudflared}},
			want:    Client{IP: netip.MustParseAddr("203.0.113.9"), ViaProxy: true},
		},
		{
			name:    "empty XFF: the peer",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {""}},
			want:    Client{IP: netip.MustParseAddr(npm), ViaProxy: true, Unresolved: true},
		},
		{
			name:    "no XFF: the peer",
			peer:    npm + ":443",
			trusted: trusted,
			want:    Client{IP: netip.MustParseAddr(npm), ViaProxy: true, Unresolved: true},
		},
		{
			name:    "every entry trusted: the left-most",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {cloudflared + ", " + npm}},
			want:    Client{IP: netip.MustParseAddr(cloudflared), ViaProxy: true, Unresolved: true},
		},
		{
			name:    "peer is 4in6",
			peer:    "[::ffff:" + npm + "]:443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"203.0.113.9"}},
			want:    Client{IP: netip.MustParseAddr("203.0.113.9"), ViaProxy: true},
		},
		{
			name:    "trusted prefix given as 4in6",
			peer:    npm + ":443",
			trusted: prefixes(t, "::ffff:172.30.33.0/120"),
			headers: map[string][]string{"X-Forwarded-For": {"203.0.113.9"}},
			want:    Client{IP: netip.MustParseAddr("203.0.113.9"), ViaProxy: true},
		},
		{
			name:    "peer with a zone, trusted",
			peer:    "[fe80::1%eth0]:443",
			trusted: prefixes(t, "fe80::/10"),
			headers: map[string][]string{"X-Forwarded-For": {"203.0.113.9"}},
			want:    Client{IP: netip.MustParseAddr("203.0.113.9"), ViaProxy: true},
		},
		{
			name: "peer with a zone, untrusted: zone dropped",
			peer: "[fe80::1%eth0]:443",
			want: Client{IP: netip.MustParseAddr("fe80::1")},
		},
		{
			name:    "XFF entry with a zone",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"fe80::1%25eth0, fe80::2%eth0"}},
			want:    Client{IP: netip.MustParseAddr("fe80::2"), ViaProxy: true},
		},
		{
			name:    "trusted as a single-IP prefix",
			peer:    "192.168.1.2:443",
			trusted: prefixes(t, "192.168.1.2/32"),
			headers: map[string][]string{"X-Forwarded-For": {"192.168.1.3"}},
			want:    Client{IP: netip.MustParseAddr("192.168.1.3"), ViaProxy: true},
		},
		{
			name:    "single-IP prefix does not trust its neighbor",
			peer:    "192.168.1.3:443",
			trusted: prefixes(t, "192.168.1.2/32"),
			headers: map[string][]string{"X-Forwarded-For": {"8.8.8.8"}},
			want:    Client{IP: netip.MustParseAddr("192.168.1.3")},
		},
		{
			name:    "proto: right-most value wins",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-Proto": {"http, https"}},
			want:    Client{IP: netip.MustParseAddr(npm), HTTPS: true, ViaProxy: true, Unresolved: true},
		},
		{
			name:    "proto: forged https on the left loses",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-Proto": {"https", "http"}},
			want:    Client{IP: netip.MustParseAddr(npm), ViaProxy: true, Unresolved: true},
		},
		{
			name:    "proto: case does not matter",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-Proto": {"HTTPS"}},
			want:    Client{IP: netip.MustParseAddr(npm), HTTPS: true, ViaProxy: true, Unresolved: true},
		},
		{
			name: "proto http overrides TLS to the proxy",
			peer: npm + ":443", tls: true,
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-Proto": {"http"}},
			want:    Client{IP: netip.MustParseAddr(npm), ViaProxy: true, Unresolved: true},
		},
		{
			name:    "CF-Connecting-IP ignored even from a trusted peer",
			peer:    npm + ":443",
			trusted: trusted,
			headers: map[string][]string{"Cf-Connecting-Ip": {"192.168.1.10"}, "X-Real-Ip": {"192.168.1.10"}},
			want:    Client{IP: netip.MustParseAddr(npm), ViaProxy: true, Unresolved: true},
		},
		{
			name:    "peer that does not parse: invalid client",
			peer:    "garbage",
			trusted: prefixes(t, "0.0.0.0/0", "::/0"),
			headers: map[string][]string{"X-Forwarded-For": {"192.168.1.10"}},
			want:    Client{},
		},
		{
			name: "bare peer IP",
			peer: "203.0.113.9",
			want: Client{IP: netip.MustParseAddr("203.0.113.9")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			for k, vs := range tc.headers {
				r.Header[k] = vs
			}
			got := ClientFrom(r, tc.trusted)
			if got != tc.want {
				t.Errorf("ClientFrom = %+v, want %+v", got, tc.want)
			}
			// The trusted proxies sit inside these nets. An unresolved
			// client must still not be local.
			nets := append(prefixes(t, "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fe80::/10"), tc.trusted...)
			if local := got.Local(nets); local != (!got.Unresolved && IsLocal(got.IP, nets)) || got.Unresolved && local {
				t.Errorf("Local = %v for %+v", local, got)
			}
		})
	}
}

func TestIsLocal(t *testing.T) {
	nets := prefixes(t, "10.0.0.0/8", "192.168.0.0/16", "fc00::/7", "fe80::/10")
	for _, tc := range []struct {
		ip   string
		nets []netip.Prefix
		want bool
	}{
		{"10.1.2.3", nets, true},
		{"192.168.40.20", nets, true},
		{"192.169.0.1", nets, false},
		{"8.8.8.8", nets, false},
		{"::ffff:10.1.2.3", nets, true}, // 4in6 counts as IPv4
		{"::ffff:8.8.8.8", nets, false},
		{"fd00::1", nets, true},
		{"fe80::1%eth0", nets, true}, // zone ignored
		{"2001:db8::1", nets, false},
		{"10.1.2.3", nil, false},
		{"10.1.2.3", prefixes(t, "::ffff:10.0.0.0/104"), true}, // 4in6 prefix
		{"10.1.2.3", prefixes(t, "::/0"), false},               // v6 net, v4 ip
		{"::1", prefixes(t, "0.0.0.0/0"), false},               // v4 net, v6 ip
		{"192.168.1.2", prefixes(t, "192.168.1.2/32"), true},
		{"192.168.1.3", prefixes(t, "192.168.1.2/32"), false},
	} {
		if got := IsLocal(netip.MustParseAddr(tc.ip), tc.nets); got != tc.want {
			t.Errorf("IsLocal(%s, %v) = %v, want %v", tc.ip, tc.nets, got, tc.want)
		}
	}
	all := prefixes(t, "0.0.0.0/0", "::/0")
	if IsLocal(netip.Addr{}, all) {
		t.Error("invalid IP is local")
	}
	if IsLocal(netip.MustParseAddr("10.0.0.1"), []netip.Prefix{{}}) {
		t.Error("zero prefix matches")
	}
}

// FuzzClientFrom checks two things for any X-Forwarded-For value:
//   - an untrusted peer is always the client, with no HTTPS and no proxy;
//   - with a trusted peer, the client is the peer or an address from the
//     header, and it is untrusted whenever an untrusted entry sits right
//     of the first garbage entry.
func FuzzClientFrom(f *testing.F) {
	for _, s := range []string{
		"", "10.0.0.5, 203.0.113.9", "[2001:db8::1]:80", "a,,b", "::ffff:1.2.3.4",
		"fe80::1%eth0", "172.30.33.5, 172.30.33.9", "1.2.3.4:99999", "[::1", ",",
	} {
		f.Add(s, true)
		f.Add(s, false)
	}
	trusted := prefixes(f, "172.30.33.0/24", "fe80::/10")
	f.Fuzz(func(t *testing.T, xff string, peerTrusted bool) {
		peer := netip.MustParseAddr("203.0.113.77")
		if peerTrusted {
			peer = netip.MustParseAddr("172.30.33.5")
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = netip.AddrPortFrom(peer, 443).String()
		r.Header.Set("X-Forwarded-For", xff)
		r.Header.Set("X-Forwarded-Proto", "https")
		c := ClientFrom(r, trusted)
		if !peerTrusted {
			if c != (Client{IP: peer}) {
				t.Fatalf("untrusted peer, XFF %q: %+v", xff, c)
			}
			return
		}
		if !c.ViaProxy {
			t.Fatalf("trusted peer without ViaProxy")
		}
		// A trusted address is never local, whatever the nets.
		everything := prefixes(t, "0.0.0.0/0", "::/0")
		if IsLocal(c.IP, trusted) && (!c.Unresolved || c.Local(everything)) {
			t.Fatalf("XFF %q: trusted client %s counts as resolved or local: %+v", xff, c.IP, c)
		}
		if !c.Unresolved && IsLocal(c.IP, trusted) {
			t.Fatalf("XFF %q: resolved to a trusted hop", xff)
		}
		// Recompute what the walk may see.
		var entries []netip.Addr
		cleanTail := true // every entry parsed so far, from the right
		hasUntrusted := false
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip, ok := parseHop(strings.TrimSpace(parts[i]))
			if !ok {
				cleanTail = false
				continue
			}
			entries = append(entries, ip)
			if cleanTail && !IsLocal(ip, trusted) {
				hasUntrusted = true
			}
		}
		if c.IP != peer && !contains(entries, c.IP) {
			t.Fatalf("XFF %q: client %s is not the peer or an entry", xff, c.IP)
		}
		if hasUntrusted && IsLocal(c.IP, trusted) {
			t.Fatalf("XFF %q: client %s is trusted, but an untrusted hop came first", xff, c.IP)
		}
		if c.IP.Zone() != "" || c.IP.Is4In6() {
			t.Fatalf("client %s not normalized", c.IP)
		}
	})
}

func contains(as []netip.Addr, a netip.Addr) bool {
	for _, b := range as {
		if a == b {
			return true
		}
	}
	return false
}

// Every way the walk can end on a proxy gives an unresolved client that
// is never local, though the proxy's own IP is in the local nets.
func TestClientUnresolvedNeverLocal(t *testing.T) {
	trusted := prefixes(t, "172.30.33.0/24")
	local := prefixes(t, "172.16.0.0/12", "192.168.0.0/16")
	for name, xff := range map[string][]string{
		"no XFF":         nil,
		"empty XFF":      {""},
		"garbage":        {"unknown"},
		"garbage behind": {"203.0.113.9, junk, " + cloudflared},
		"only trusted":   {cloudflared + ", " + npm},
		"empty entry":    {"192.168.1.10,,"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = npm + ":443"
		if xff != nil {
			r.Header["X-Forwarded-For"] = xff
		}
		c := ClientFrom(r, trusted)
		if !c.Unresolved || c.Local(local) {
			t.Errorf("%s: %+v, Local=%v", name, c, c.Local(local))
		}
		if !IsLocal(c.IP, local) {
			t.Errorf("%s: test is weak, %s is not in the local nets", name, c.IP)
		}
	}
	// A resolved LAN client behind the proxy is local.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = npm + ":443"
	r.Header.Set("X-Forwarded-For", "192.168.1.10")
	if c := ClientFrom(r, trusted); c.Unresolved || !c.Local(local) {
		t.Errorf("LAN client behind proxy: %+v", c)
	}
	// A direct LAN peer with no proxies is local.
	r.RemoteAddr = "192.168.1.10:5555"
	if c := ClientFrom(r, nil); c.Unresolved || !c.Local(local) {
		t.Errorf("direct LAN peer: %+v", c)
	}
}
