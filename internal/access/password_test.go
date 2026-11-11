package access

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// fakeDerive swaps deriveKey for a cheap stand-in for the test. It counts
// calls. The fake is not a KDF; it only keeps output tied to every input.
func fakeDerive(t testing.TB) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	orig := deriveKey
	deriveKey = func(pw, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
		calls.Add(1)
		out := make([]byte, keyLen)
		for i := range out {
			out[i] = byte(i) ^ byte(time) ^ byte(memory) ^ threads
			for j, c := range pw {
				out[i] ^= c + byte(j)
			}
			for _, c := range salt {
				out[i] += c
			}
		}
		return out
	}
	t.Cleanup(func() { deriveKey = orig })
	return &calls
}

func TestHashPasswordRefusesEmpty(t *testing.T) {
	if _, err := HashPassword(""); !errors.Is(err, ErrEmptyPassword) {
		t.Fatalf("HashPassword(\"\") = %v", err)
	}
}

func TestHashPasswordRefusesOversize(t *testing.T) {
	calls := fakeDerive(t)
	if _, err := HashPassword(strings.Repeat("x", MaxPasswordLen+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("oversize HashPassword = %v", err)
	}
	if _, err := HashPassword(strings.Repeat("x", MaxPasswordLen)); err != nil {
		t.Fatalf("max size HashPassword = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("deriveKey called %d times", calls.Load())
	}
}

// Real argon2id. Kept to one test: it is slow under -race.
func TestPasswordRoundTripArgon2(t *testing.T) {
	h1, err := HashPassword("+1 (555) 123-4567")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h1, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("hash %q has the wrong head", h1)
	}
	if p, err := parsePHC(h1); err != nil || len(p.salt) != 16 || len(p.key) != 32 {
		t.Errorf("parsePHC(own hash) = %+v, %v", p, err)
	}
	h2, _ := HashPassword("+1 (555) 123-4567")
	if h1 == h2 {
		t.Error("two hashes of one password are equal: salt not random")
	}
	pws := []model.LinkPassword{{ID: "p1", Label: "Anna", Hash: h1}}
	if id, ok := MatchPassword(pws, "+1 (555) 123-4567"); !ok || id != "p1" {
		t.Errorf("MatchPassword = %q, %v", id, ok)
	}
	if _, ok := MatchPassword(pws, "15551234567"); ok {
		t.Error("digits only match: input was normalized")
	}
}

func TestMatchPassword(t *testing.T) {
	fakeDerive(t)
	hash := func(s string) string {
		h, err := HashPassword(s)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	pws := []model.LinkPassword{
		{ID: "p1", Label: "Anna", Hash: hash("5551234567")},
		{ID: "p2", Label: "Bob", Hash: hash("Café")},
		{ID: "p3", Label: "Dup", Hash: hash("5551234567")},
	}
	for _, tc := range []struct {
		plain string
		id    string
	}{
		{"5551234567", "p1"}, // first of two matches wins
		{"Café", "p2"},
		{"555 123 4567", ""}, // no normalization of any kind
		{" 5551234567", ""},
		{"5551234567 ", ""},
		{"555123456", ""},
		{"55512345678", ""},
		{"café", ""},
		{"Café", ""}, // same text, other Unicode form
		{"", ""},
		{strings.Repeat("5", MaxPasswordLen+1), ""},
	} {
		id, ok := MatchPassword(pws, tc.plain)
		if id != tc.id || ok != (tc.id != "") {
			t.Errorf("MatchPassword(%q) = %q, %v; want %q", tc.plain, id, ok, tc.id)
		}
	}
	if _, ok := MatchPassword(nil, "5551234567"); ok {
		t.Error("empty list matches")
	}
}

func TestMatchPasswordChecksEveryEntry(t *testing.T) {
	calls := fakeDerive(t)
	var pws []model.LinkPassword
	for i, pw := range []string{"one", "two", "three", "four"} {
		h, _ := HashPassword(pw)
		pws = append(pws, model.LinkPassword{ID: string(rune('a' + i)), Hash: h})
	}
	for _, plain := range []string{"one", "four", "nope"} {
		calls.Store(0)
		MatchPassword(pws, plain)
		if n := calls.Load(); n != int64(len(pws)) {
			t.Errorf("MatchPassword(%q) derived %d keys, want %d", plain, n, len(pws))
		}
	}
	// Oversize and empty input stop before any work.
	calls.Store(0)
	MatchPassword(pws, strings.Repeat("x", MaxPasswordLen+1))
	MatchPassword(pws, "")
	if calls.Load() != 0 {
		t.Errorf("rejected input derived %d keys", calls.Load())
	}
}

func TestParsePHC(t *testing.T) {
	const salt = "c2FsdHNhbHRzYWx0c2FsdA"                     // 16 bytes
	const key = "a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U" // 32 bytes
	ok := "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$" + key
	if _, err := parsePHC(ok); err != nil {
		t.Fatalf("good hash: %v", err)
	}
	for name, s := range map[string]string{
		"empty":            "",
		"plain text":       "5551234567",
		"argon2i":          "$argon2i$v=19$m=19456,t=2,p=1$" + salt + "$" + key,
		"bcrypt":           "$2b$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234",
		"old version":      "$argon2id$v=16$m=19456,t=2,p=1$" + salt + "$" + key,
		"no version":       "$argon2id$m=19456,t=2,p=1$" + salt + "$" + key,
		"huge m":           "$argon2id$v=19$m=4294967295,t=2,p=1$" + salt + "$" + key,
		"m over cap":       "$argon2id$v=19$m=262145,t=2,p=1$" + salt + "$" + key,
		"m overflow":       "$argon2id$v=19$m=99999999999999999999,t=2,p=1$" + salt + "$" + key,
		"huge t":           "$argon2id$v=19$m=19456,t=11,p=1$" + salt + "$" + key,
		"huge p":           "$argon2id$v=19$m=19456,t=2,p=17$" + salt + "$" + key,
		"p overflows u8":   "$argon2id$v=19$m=19456,t=2,p=257$" + salt + "$" + key,
		"zero m":           "$argon2id$v=19$m=0,t=2,p=1$" + salt + "$" + key,
		"zero t":           "$argon2id$v=19$m=19456,t=0,p=1$" + salt + "$" + key,
		"zero p":           "$argon2id$v=19$m=19456,t=2,p=0$" + salt + "$" + key,
		"m below 8p":       "$argon2id$v=19$m=15,t=2,p=2$" + salt + "$" + key,
		"negative m":       "$argon2id$v=19$m=-1,t=2,p=1$" + salt + "$" + key,
		"plus sign":        "$argon2id$v=19$m=+19456,t=2,p=1$" + salt + "$" + key,
		"spaces":           "$argon2id$v=19$m= 19456,t=2,p=1$" + salt + "$" + key,
		"swapped order":    "$argon2id$v=19$t=2,m=19456,p=1$" + salt + "$" + key,
		"extra param":      "$argon2id$v=19$m=19456,t=2,p=1,k=3$" + salt + "$" + key,
		"missing param":    "$argon2id$v=19$m=19456,t=2$" + salt + "$" + key,
		"no salt":          "$argon2id$v=19$m=19456,t=2,p=1$$" + key,
		"short salt":       "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$" + key,
		"salt with pad":    "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "==$" + key,
		"salt not base64":  "$argon2id$v=19$m=19456,t=2,p=1$!!!!!!!!!!!!!!!!!!!!!!$" + key,
		"no key":           "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$",
		"short key":        "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$aGFzaA",
		"huge key":         "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$" + strings.Repeat("A", 1000),
		"extra part":       ok + "$x",
		"no leading $":     ok[1:],
		"url base64 salt":  "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2Fsd_-$" + key,
		"newline in m":     "$argon2id$v=19$m=19456\n,t=2,p=1$" + salt + "$" + key,
		"many dollars":     strings.Repeat("$", 10000),
		"huge digit run m": "$argon2id$v=19$m=" + strings.Repeat("0", 5000) + "1,t=2,p=1$" + salt + "$" + key,
	} {
		if _, err := parsePHC(s); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// Hostile and malformed hashes never match and never call argon2.
func TestMatchPasswordMalformed(t *testing.T) {
	calls := fakeDerive(t)
	pws := []model.LinkPassword{
		{ID: "a", Hash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA"},
		{ID: "b", Hash: "$argon2id$v=19$m=4294967295,t=4294967295,p=255$c2FsdHNhbHRzYWx0c2FsdA$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U"},
		{ID: "c", Hash: "5551234567"},
		{ID: "d", Hash: ""},
	}
	if _, ok := MatchPassword(pws, "5551234567"); ok {
		t.Error("a malformed hash matched")
	}
	if calls.Load() != 0 {
		t.Errorf("malformed hashes derived %d keys", calls.Load())
	}
}

// FuzzMatchPassword feeds random hash strings. MatchPassword must not
// panic, and a hash that parses must stay within the caps. argon2 is
// swapped for a fake so a fuzzed m=256 MiB costs nothing.
func FuzzMatchPassword(f *testing.F) {
	for _, s := range []string{
		"", "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U",
		"$argon2id$v=19$m=262144,t=10,p=16$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=4294967295,t=2,p=1$c2FsdA$aGFzaA", "$$$$$", "$argon2id$",
	} {
		f.Add(s, "pw")
	}
	fakeDerive(f)
	f.Fuzz(func(t *testing.T, hash, plain string) {
		if h, err := parsePHC(hash); err == nil {
			if h.memory > maxArgonMemoryKiB || h.time > maxArgonTime || h.threads > maxArgonThreads ||
				h.memory < 8*uint32(h.threads) || len(h.key) > maxArgonKeyLen || len(h.salt) > maxArgonSaltLen {
				t.Fatalf("parsed hash over the caps: %+v", h)
			}
		}
		MatchPassword([]model.LinkPassword{{ID: "x", Hash: hash}}, plain)
	})
}
