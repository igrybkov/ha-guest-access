package access

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// ErrEmptyPassword is returned by HashPassword for "".
var ErrEmptyPassword = errors.New("password is empty")

// MaxPasswordLen caps password input in bytes, so a huge body can't keep
// argon2 busy hashing it.
const MaxPasswordLen = 1024

// ErrPasswordTooLong is returned by HashPassword for input over
// MaxPasswordLen bytes.
var ErrPasswordTooLong = fmt.Errorf("password is longer than %d bytes", MaxPasswordLen)

// Argon2id parameters for new hashes. OWASP's pick for a small box.
const (
	argonMemoryKiB = 19456
	argonTime      = 2
	argonThreads   = 1
	argonSaltLen   = 16
	argonKeyLen    = 32
)

// Caps on the parameters of a stored hash. A tampered links.yaml can't
// make MatchPassword allocate more than this.
const (
	maxArgonMemoryKiB = 256 * 1024
	maxArgonTime      = 10
	maxArgonThreads   = 16
	minArgonSaltLen   = 8
	maxArgonSaltLen   = 64
	minArgonKeyLen    = 16
	maxArgonKeyLen    = 64
)

// deriveKey is argon2.IDKey. Tests swap it to count calls, and the fuzz
// target swaps it so it does not spend real memory.
var deriveKey = argon2.IDKey

// b64 is the PHC base64: standard alphabet, no padding.
var b64 = base64.RawStdEncoding.Strict()

// HashPassword returns the argon2id PHC string of plain, as typed: no
// trimming, no normalizing. The format is
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>, with a 16-byte random salt
// and a 32-byte key (golang.org/x/crypto/argon2). It refuses an empty
// password and one over MaxPasswordLen bytes.
func HashPassword(plain string) (string, error) {
	if plain == "" {
		return "", ErrEmptyPassword
	}
	if len(plain) > MaxPasswordLen {
		return "", ErrPasswordTooLong
	}
	salt := make([]byte, argonSaltLen)
	_, _ = rand.Read(salt) // crypto/rand.Read never fails
	key := deriveKey([]byte(plain), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// MatchPassword compares plain, as typed, against every password of a link
// and returns the id of the one it matches. Each stored hash is checked
// with its own parameters. It checks every entry, with no early exit, so
// the time taken does not tell which one matched. If two entries match,
// the first wins. An empty plain, one over MaxPasswordLen bytes, or a hash
// that does not parse (or asks for more than the caps) never matches.
func MatchPassword(pws []model.LinkPassword, plain string) (id string, ok bool) {
	if plain == "" || len(plain) > MaxPasswordLen {
		return "", false
	}
	for _, pw := range pws {
		h, err := parsePHC(pw.Hash)
		if err != nil {
			continue
		}
		got := deriveKey([]byte(plain), h.salt, h.time, h.memory, h.threads, uint32(len(h.key)))
		if subtle.ConstantTimeCompare(got, h.key) == 1 && !ok {
			id, ok = pw.ID, true
		}
	}
	return id, ok
}

// phc is a parsed argon2id PHC string.
type phc struct {
	memory, time uint32
	threads      uint8
	salt, key    []byte
}

// parsePHC parses $argon2id$v=19$m=<m>,t=<t>,p=<p>$<salt>$<key> strictly:
// fields in that order, decimal numbers, values within the caps.
func parsePHC(s string) (phc, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return phc{}, errors.New("not an argon2id PHC string")
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return phc{}, errors.New("unsupported argon2 version")
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return phc{}, errors.New("want m, t and p")
	}
	m, err1 := phcUint(params[0], "m=", maxArgonMemoryKiB)
	t, err2 := phcUint(params[1], "t=", maxArgonTime)
	p, err3 := phcUint(params[2], "p=", maxArgonThreads)
	if err := errors.Join(err1, err2, err3); err != nil {
		return phc{}, err
	}
	// argon2 needs at least 8 KiB of memory per thread.
	if m < 8*p {
		return phc{}, errors.New("memory too small for the thread count")
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < minArgonSaltLen || len(salt) > maxArgonSaltLen {
		return phc{}, errors.New("bad salt")
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < minArgonKeyLen || len(key) > maxArgonKeyLen {
		return phc{}, errors.New("bad key")
	}
	return phc{memory: uint32(m), time: uint32(t), threads: uint8(p), salt: salt, key: key}, nil
}

// phcUint parses "<prefix><n>" with 1 <= n <= limit. Only plain digits
// count: no sign, no spaces.
func phcUint(s, prefix string, limit uint64) (uint64, error) {
	num, ok := strings.CutPrefix(s, prefix)
	if !ok || num == "" || len(num) > 10 || strings.Trim(num, "0123456789") != "" {
		return 0, fmt.Errorf("bad %q", prefix)
	}
	n, err := strconv.ParseUint(num, 10, 32)
	if err != nil || n < 1 || n > limit {
		return 0, fmt.Errorf("%s out of range", prefix)
	}
	return n, nil
}
