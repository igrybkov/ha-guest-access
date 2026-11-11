package access

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

func TestHashPasswordRefusesEmpty(t *testing.T) {
	if _, err := HashPassword(""); !errors.Is(err, ErrEmptyPassword) {
		t.Fatalf("HashPassword(\"\") = %v", err)
	}
}

func TestNewPasswordGuardDefaults(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{PerIPFails: 3})
	want := GuardOptions{PerIPFails: 3, PerIPWindow: GuardPerIPWindow, PerLinkFails: GuardPerLinkFails, PerLinkWin: GuardPerLinkWin, Block: GuardBlock}
	if g.opts != want {
		t.Fatalf("opts = %+v, want %+v", g.opts, want)
	}
}

// The stubs fail closed until wave 1 fills them. Replace this test with
// real table tests when you implement them.
func TestPasswordStubsFailClosed(t *testing.T) {
	if _, err := HashPassword("5551234567"); err == nil {
		t.Error("stub hashes")
	}
	pws := []model.LinkPassword{{ID: "p1", Label: "Anna", Hash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA"}}
	if _, ok := MatchPassword(pws, "anything"); ok {
		t.Error("stub matches")
	}
	s := NewSigner([]byte("k"))
	now := time.Now()
	if _, ok := s.VerifySession("l1", s.SignSession("l1", "p1", now.Add(time.Hour)), now); ok {
		t.Error("stub session verifies")
	}
	g := NewPasswordGuard(GuardOptions{})
	if ok, _ := g.Allow("l1", netip.MustParseAddr("192.0.2.1"), now); ok {
		t.Error("stub guard allows")
	}
}
