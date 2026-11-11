// Package access holds the guest-side checks that sit around authz: who
// the client is (IP behind trusted proxies), whether it is local, how far
// it is from home, the signed location cookie, slugs and rate limits.
package access

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"net/netip"
	"time"
)

// Client is how the app sees one request.
type Client struct {
	// IP is the client address: the peer, or, when the peer is a trusted
	// proxy, the right-most X-Forwarded-For entry that is not one.
	IP netip.Addr
	// HTTPS is true when the request came over TLS, directly or per the
	// trusted proxy's X-Forwarded-Proto.
	HTTPS bool
	// ViaProxy is true when the forwarding headers were used.
	ViaProxy bool
}

// ClientFrom derives the Client of r. Forwarding headers count only when
// the peer (r.RemoteAddr) is in trusted. With trusted empty, the peer is
// the client.
//
// TODO(wave 1): implement. The stub returns the zero Client, which is
// never local.
func ClientFrom(r *http.Request, trusted []netip.Prefix) Client {
	return Client{}
}

// IsLocal reports whether ip is in one of nets. An invalid ip is never
// local. IPv4-mapped IPv6 addresses count as their IPv4 address.
//
// TODO(wave 1): implement. The stub says no.
func IsLocal(ip netip.Addr, nets []netip.Prefix) bool {
	return false
}

// earthRadiusM is the mean Earth radius used by DistanceM.
const earthRadiusM = 6_371_008.8

// DistanceM returns the great-circle distance between two points in
// degrees, in meters (haversine).
func DistanceM(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(min(1, a)))
}

// Signer makes and checks the near-home cookie: an HMAC over the link id
// and an expiry time.
type Signer struct {
	key []byte
}

// NewSigner returns a Signer with key. The key lives in /data.
func NewSigner(key []byte) *Signer {
	return &Signer{key: append([]byte(nil), key...)}
}

// Sign returns a token for linkID that expires at exp.
//
// TODO(wave 1): implement. The stub returns "".
func (s *Signer) Sign(linkID string, exp time.Time) string {
	return ""
}

// Verify reports whether token was signed for linkID and has not expired
// at now. It compares in constant time.
//
// TODO(wave 1): implement. The stub says no.
func (s *Signer) Verify(linkID, token string, now time.Time) bool {
	return false
}

// slugBytes is the entropy of a random slug: 128 bits.
const slugBytes = 16

// NewSlug returns a random slug: 16 bytes from crypto/rand, base64url
// without padding (22 characters).
func NewSlug() string {
	b := make([]byte, slugBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashSlug returns the hex SHA-256 of a slug. Links are looked up by it.
func HashSlug(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ErrBadSlug is wrapped by ValidCustomSlug errors.
var ErrBadSlug = errors.New("bad slug")

// MinCustomSlugLen is the shortest custom slug accepted.
const MinCustomSlugLen = 12

// ValidCustomSlug checks a slug the admin typed: only a-z, 0-9 and "-", at
// least MinCustomSlugLen characters. Uniqueness is the caller's check.
//
// TODO(wave 1): implement. The stub refuses every slug.
func ValidCustomSlug(s string) error {
	return ErrBadSlug
}

// Limiter is a fixed-budget rate limiter per key, such as a client IP for
// slug misses, or link id plus client IP for actions. Safe for concurrent
// use.
type Limiter struct {
	n   int
	per time.Duration
}

// NewLimiter allows n events per key in any window of length per.
func NewLimiter(n int, per time.Duration) *Limiter {
	return &Limiter{n: n, per: per}
}

// Allow records an event for key at now and reports whether it is within
// the limit.
//
// TODO(wave 1): implement. The stub refuses everything.
func (l *Limiter) Allow(key string, now time.Time) bool {
	return false
}
