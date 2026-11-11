package web

import (
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/igrybkov/ha-guest-access/internal/access"
)

// forwardHeaders are the headers a proxy may set to name the client. The
// diagnostics show them; access.ClientFrom reads only X-Forwarded-For and
// X-Forwarded-Proto, and only from a trusted peer.
var forwardHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Real-Ip", "Cf-Connecting-Ip"}

// maxDiagValue bounds each text value a diagnostics entry keeps: a route
// and each forwarding header. A client picks these, and the ring keeps
// many entries, so each one stays small. It is a fixed bound, not an
// option: the values only help to read the proxy setup, and 256 bytes
// hold any real address list.
const maxDiagValue = 256

// clip cuts s to at most n bytes, on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Back up to the start of the rune that crosses n, if one does.
	cut := n
	for cut > 0 && cut > n-utf8.UTFMax && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if !utf8.RuneStart(s[cut]) {
		cut = n // not UTF-8 here: cut at n
	}
	return s[:cut]
}

// DiagEntry is how the app saw one guest request. It never holds the path
// (the slug is in it), a body or a cookie.
type DiagEntry struct {
	At    time.Time `json:"at"`
	Route string    `json:"route"` // such as "g/state", "assets", "other"
	Peer  string    `json:"peer"`
	// Headers are the forwarding headers as received.
	Headers map[string]string `json:"headers,omitempty"`
	// Client is the derived client IP.
	Client     string `json:"client"`
	ViaProxy   bool   `json:"via_proxy"`
	Unresolved bool   `json:"unresolved"`
	// Local is whether the client is in the local networks. It is
	// reported even while local access is off.
	Local bool `json:"local"`
	HTTPS bool `json:"https"`
	// UntrustedForwarding is true when the request had forwarding headers
	// but its peer is not a trusted proxy, so they were ignored.
	UntrustedForwarding bool `json:"untrusted_forwarding"`
}

// diagRing keeps the last few DiagEntry values. Safe for concurrent use.
type diagRing struct {
	mu   sync.Mutex
	buf  []DiagEntry
	next int
	full bool
}

func newDiagRing(n int) *diagRing { return &diagRing{buf: make([]DiagEntry, n)} }

func (d *diagRing) add(e DiagEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf[d.next] = e
	d.next = (d.next + 1) % len(d.buf)
	d.full = d.full || d.next == 0
}

// list returns the entries, newest first.
func (d *diagRing) list() []DiagEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := d.next
	if d.full {
		n = len(d.buf)
	}
	out := make([]DiagEntry, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, d.buf[(d.next-i+len(d.buf))%len(d.buf)])
	}
	return out
}

// diagEntry builds the entry for r.
func diagEntry(r *http.Request, c access.Client, local bool, at time.Time) DiagEntry {
	e := DiagEntry{
		At: at, Route: routeLabel(r.URL.Path), Peer: r.RemoteAddr,
		ViaProxy: c.ViaProxy, Unresolved: c.Unresolved, Local: local, HTTPS: c.HTTPS,
	}
	if c.IP.IsValid() {
		e.Client = c.IP.String()
	}
	for _, h := range forwardHeaders {
		if v := r.Header.Values(h); len(v) > 0 {
			if e.Headers == nil {
				e.Headers = map[string]string{}
			}
			e.Headers[h] = clip(strings.Join(v, ", "), maxDiagValue)
		}
	}
	e.UntrustedForwarding = len(e.Headers) > 0 && !c.ViaProxy
	return e
}

// routeLabel names a path without its slug, for logs and diagnostics:
// "/g/<slug>/state" is "g/state", "/g/<slug>" is "g/page". A path under
// /api/ is kept, cut to maxDiagValue bytes.
func routeLabel(path string) string {
	switch {
	case strings.HasPrefix(path, "/g/"):
		rest := strings.TrimPrefix(path, "/g/")
		_, route, found := strings.Cut(rest, "/")
		switch known := guestRouteNames[route]; {
		case !found || route == "":
			return "g/page"
		case known:
			return "g/" + route
		}
		return "g/other"
	case strings.HasPrefix(path, "/assets/"):
		return "assets"
	case strings.HasPrefix(path, "/api/"):
		return clip(path, maxDiagValue)
	}
	return "other"
}
