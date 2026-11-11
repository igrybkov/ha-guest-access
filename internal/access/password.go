package access

import (
	"errors"
	"net/netip"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// ErrEmptyPassword is returned by HashPassword for "".
var ErrEmptyPassword = errors.New("password is empty")

// HashPassword returns the argon2id PHC string of plain, as typed: no
// trimming, no normalizing. Parameters follow OWASP for a small box: 19 MiB,
// t=2, p=1, a 16-byte random salt and a 32-byte key
// (golang.org/x/crypto/argon2). It refuses an empty password.
//
// TODO(wave 1): implement. The stub refuses every password.
func HashPassword(plain string) (string, error) {
	if plain == "" {
		return "", ErrEmptyPassword
	}
	return "", errors.New("access: HashPassword not implemented")
}

// MatchPassword compares plain, as typed, against every password of a link
// and returns the id of the one it matches. It checks every entry, with no
// early exit, so the time taken does not tell which one matched. An empty
// plain or a hash that does not parse never matches.
//
// TODO(wave 1): implement. The stub matches nothing.
func MatchPassword(pws []model.LinkPassword, plain string) (id string, ok bool) {
	return "", false
}

// SignSession returns a session token for a guest who entered password
// passwordID of linkID. It expires at exp. The caller must also check that
// the password still exists on the link, so deleting it ends its sessions.
//
// TODO(wave 1): implement. The stub returns "".
func (s *Signer) SignSession(linkID, passwordID string, exp time.Time) string {
	return ""
}

// VerifySession checks a session token for linkID at now and returns the
// password id it was signed for. Tokens of the location cookie never pass
// here, and session tokens never pass Verify.
//
// TODO(wave 1): implement. The stub says no.
func (s *Signer) VerifySession(linkID, token string, now time.Time) (passwordID string, ok bool) {
	return "", false
}

// Password guard defaults.
const (
	GuardPerIPFails   = 5                // fails per link + client IP ...
	GuardPerIPWindow  = 15 * time.Minute // ... in this window
	GuardPerLinkFails = 30               // fails per link, from any IP ...
	GuardPerLinkWin   = time.Hour        // ... in this window
	GuardBlock        = time.Hour        // block length once over a limit
)

// GuardOptions set the PasswordGuard limits. Zero fields take the Guard*
// defaults.
type GuardOptions struct {
	PerIPFails   int
	PerIPWindow  time.Duration
	PerLinkFails int
	PerLinkWin   time.Duration
	Block        time.Duration
}

// PasswordGuard locks the password form after too many wrong tries: per
// link + client IP, and per link from any IP. Over either limit, the form is
// blocked for Block. Safe for concurrent use.
type PasswordGuard struct {
	opts GuardOptions
}

// NewPasswordGuard returns a guard with o, defaults filled in.
func NewPasswordGuard(o GuardOptions) *PasswordGuard {
	if o.PerIPFails == 0 {
		o.PerIPFails = GuardPerIPFails
	}
	if o.PerIPWindow == 0 {
		o.PerIPWindow = GuardPerIPWindow
	}
	if o.PerLinkFails == 0 {
		o.PerLinkFails = GuardPerLinkFails
	}
	if o.PerLinkWin == 0 {
		o.PerLinkWin = GuardPerLinkWin
	}
	if o.Block == 0 {
		o.Block = GuardBlock
	}
	return &PasswordGuard{opts: o}
}

// Allow reports whether ip may try a password on linkID at now. When not,
// retryAfter says how long the block still lasts.
//
// TODO(wave 1): implement. The stub blocks everything.
func (g *PasswordGuard) Allow(linkID string, ip netip.Addr, now time.Time) (ok bool, retryAfter time.Duration) {
	return false, g.opts.Block
}

// Fail records a wrong password. lockedNow is true when this fail starts a
// block, so the caller fires guest_access_lockout once per lockout.
//
// TODO(wave 1): implement. The stub never reports a lockout.
func (g *PasswordGuard) Fail(linkID string, ip netip.Addr, now time.Time) (lockedNow bool) {
	return false
}
