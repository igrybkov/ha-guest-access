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
	"fmt"
	"math"
	"strings"
)

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

// slugBytes is the entropy of a random slug: 128 bits.
const slugBytes = 16

// RandomSlugLen is the length of a slug from NewSlug.
const RandomSlugLen = 22

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

// Custom slug lengths.
const (
	MinCustomSlugLen = 12
	MaxCustomSlugLen = 64
)

// ValidCustomSlug checks a slug the admin typed: only a-z, 0-9 and "-",
// MinCustomSlugLen to MaxCustomSlugLen characters, no "-" at either end and
// no "--". The error wraps ErrBadSlug and says why. Uniqueness is the
// caller's check.
func ValidCustomSlug(s string) error {
	return ValidCustomSlugLen(s, MinCustomSlugLen, MaxCustomSlugLen)
}

// ValidCustomSlugLen is ValidCustomSlug with the lengths from the app
// options. minLen is raised to MinCustomSlugLen: a shorter slug is easy
// to guess, and links.yaml refuses it.
func ValidCustomSlugLen(s string, minLen, maxLen int) error {
	minLen = max(minLen, MinCustomSlugLen)
	switch {
	case len(s) < minLen:
		return fmt.Errorf("%w: shorter than %d characters", ErrBadSlug, minLen)
	case len(s) > maxLen:
		return fmt.Errorf("%w: longer than %d characters", ErrBadSlug, maxLen)
	case s[0] == '-' || s[len(s)-1] == '-':
		return fmt.Errorf("%w: starts or ends with \"-\"", ErrBadSlug)
	case strings.Contains(s, "--"):
		return fmt.Errorf("%w: has \"--\"", ErrBadSlug)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return fmt.Errorf("%w: %q is not allowed, use a-z, 0-9 and \"-\"", ErrBadSlug, rune(c))
		}
	}
	return nil
}
