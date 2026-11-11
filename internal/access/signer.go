package access

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"
)

// MinKeyLen is the shortest HMAC key NewSigner takes, in bytes.
const MinKeyLen = 32

// Token purposes. A token signed for one never passes the check of the
// other, so a location cookie can't stand in for a session and back.
const (
	purposeLocation byte = 1
	purposeSession  byte = 2
	// purposeNear is a location token that carries the measured distance
	// to home. See SignNear.
	purposeNear byte = 3
)

// tokenVersion is the first byte of every token. Bump it to change the
// format; old tokens then fail and guests sign in again.
const tokenVersion byte = 1

// maxPasswordIDLen caps the password id in a session token. It keeps the
// token small and its length prefix one byte.
const maxPasswordIDLen = 255

// maxTokenLen caps the encoded token before decoding. The longest valid
// token is well under it.
const maxTokenLen = 512

// Signer makes and checks signed cookie tokens. A token is base64url (no
// padding) of:
//
//	version (1) | purpose (1) | expiry, Unix seconds (8) |
//	[session only: password id length (1) | password id] |
//	[near only: distance in meters, uint32 (4)] | HMAC-SHA256 (32)
//
// The HMAC covers the version, purpose, expiry, the length-prefixed link
// id and the length-prefixed password id. The link id is not in the token:
// the caller passes it, so a token for link A fails for link B.
type Signer struct {
	key []byte
}

// NewSigner returns a Signer with key. The key lives in /data. It panics
// when key is shorter than MinKeyLen: a short key is a startup bug, and
// signing with it would be weak.
func NewSigner(key []byte) *Signer {
	if len(key) < MinKeyLen {
		panic(fmt.Sprintf("access: HMAC key is %d bytes, need at least %d", len(key), MinKeyLen))
	}
	return &Signer{key: append([]byte(nil), key...)}
}

// Sign returns a location token for linkID that expires at exp.
func (s *Signer) Sign(linkID string, exp time.Time) string {
	return s.sign(purposeLocation, linkID, "", exp)
}

// Verify reports whether token is a location token for linkID that has
// not expired at now. It compares in constant time.
func (s *Signer) Verify(linkID, token string, now time.Time) bool {
	_, ok := s.verify(purposeLocation, linkID, token, now)
	return ok
}

// SignSession returns a session token for a guest who entered password
// passwordID of linkID. It expires at exp. The caller must also check that
// the password still exists on the link, so deleting it ends its sessions.
// It panics on an empty passwordID or one over 255 bytes; ids come from
// the app, so that is a bug.
func (s *Signer) SignSession(linkID, passwordID string, exp time.Time) string {
	if passwordID == "" || len(passwordID) > maxPasswordIDLen {
		panic(fmt.Sprintf("access: password id length %d out of range", len(passwordID)))
	}
	return s.sign(purposeSession, linkID, passwordID, exp)
}

// VerifySession checks a session token for linkID at now and returns the
// password id it was signed for. Location tokens never pass here, and
// session tokens never pass Verify.
func (s *Signer) VerifySession(linkID, token string, now time.Time) (passwordID string, ok bool) {
	return s.verify(purposeSession, linkID, token, now)
}

// SignNear returns a location token for linkID that says the guest was
// distanceM meters from home (accuracy included). It expires at exp.
func (s *Signer) SignNear(linkID string, distanceM uint32, exp time.Time) string {
	return s.encode(s.head(purposeNear, exp, func(b []byte) []byte {
		return binary.BigEndian.AppendUint32(b, distanceM)
	}), linkID, "")
}

// VerifyNear checks a token from SignNear for linkID at now and returns
// the distance it holds. Location tokens from Sign and session tokens
// never pass here, and near tokens never pass Verify or VerifySession.
func (s *Signer) VerifyNear(linkID, token string, now time.Time) (distanceM uint32, ok bool) {
	raw, ok := decodeToken(token)
	if !ok || len(raw) != headLen+4+sha256.Size || raw[0] != tokenVersion || raw[1] != purposeNear {
		return 0, false
	}
	body, sum := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	if !hmac.Equal(sum, s.mac(body, linkID, "")) || expired(body, now) {
		return 0, false
	}
	return binary.BigEndian.Uint32(body[headLen:]), true
}

func (s *Signer) sign(purpose byte, linkID, passwordID string, exp time.Time) string {
	return s.encode(s.head(purpose, exp, func(b []byte) []byte {
		if purpose == purposeSession {
			b = append(b, byte(len(passwordID)))
			b = append(b, passwordID...)
		}
		return b
	}), linkID, passwordID)
}

// head returns version, purpose and expiry, then what payload appends.
func (s *Signer) head(purpose byte, exp time.Time, payload func([]byte) []byte) []byte {
	body := []byte{tokenVersion, purpose}
	body = binary.BigEndian.AppendUint64(body, uint64(exp.Unix()))
	return payload(body)
}

// encode appends the MAC to body and encodes the token.
func (s *Signer) encode(body []byte, linkID, passwordID string) string {
	body = append(body, s.mac(body, linkID, passwordID)...)
	return base64.RawURLEncoding.EncodeToString(body)
}

// headLen is the length of version, purpose and expiry.
const headLen = 1 + 1 + 8

// decodeToken checks the alphabet and length and decodes token.
func decodeToken(token string) ([]byte, bool) {
	if len(token) == 0 || len(token) > maxTokenLen || !isBase64URL(token) {
		return nil, false
	}
	// Strict rejects non-zero padding bits. With the alphabet check above
	// (the decoder skips \r and \n), each token has exactly one encoding.
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return raw, err == nil
}

// expired reports whether the expiry in body has passed at now.
func expired(body []byte, now time.Time) bool {
	return now.Unix() >= int64(binary.BigEndian.Uint64(body[2:headLen]))
}

// verify parses token, checks the MAC first and the expiry after, and
// returns the password id (empty for location tokens).
func (s *Signer) verify(purpose byte, linkID, token string, now time.Time) (string, bool) {
	raw, ok := decodeToken(token)
	if !ok {
		return "", false
	}
	const head = headLen
	if len(raw) < head+sha256.Size || raw[0] != tokenVersion || raw[1] != purpose {
		return "", false
	}
	body, sum := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	var passwordID string
	if purpose == purposeSession {
		if len(body) < head+1 {
			return "", false
		}
		n := int(body[head])
		if n == 0 || len(body) != head+1+n {
			return "", false
		}
		passwordID = string(body[head+1:])
	} else if len(body) != head {
		return "", false
	}
	if !hmac.Equal(sum, s.mac(body, linkID, passwordID)) {
		return "", false
	}
	if expired(body, now) {
		return "", false
	}
	return passwordID, true
}

// mac is HMAC-SHA256 over the token body, then the link id and password
// id, each with a 4-byte length prefix so no two inputs share an encoding.
func (s *Signer) mac(body []byte, linkID, passwordID string) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write(body)
	var n [4]byte
	for _, f := range []string{linkID, passwordID} {
		binary.BigEndian.PutUint32(n[:], uint32(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return h.Sum(nil)
}

// isBase64URL reports whether s holds only base64url characters.
func isBase64URL(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
