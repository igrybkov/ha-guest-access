package access

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func ipN(i int) netip.Addr {
	return netip.AddrFrom4([4]byte{198, 51, byte(i >> 8), byte(i)})
}

func TestNewPasswordGuardDefaults(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{PerIPFails: 3})
	want := GuardOptions{PerIPFails: 3, PerIPWindow: GuardPerIPWindow, PerLinkFails: GuardPerLinkFails, PerLinkWin: GuardPerLinkWin, Block: GuardBlock}
	if g.opts != want {
		t.Fatalf("opts = %+v, want %+v", g.opts, want)
	}
}

func TestGuardPerIPLimit(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	ip := ipN(1)
	now := t0
	for i := range GuardPerIPFails {
		if ok, _ := g.Allow("l1", ip, now); !ok {
			t.Fatalf("try %d refused", i)
		}
		locked := g.Fail("l1", ip, now)
		if locked != (i == GuardPerIPFails-1) {
			t.Fatalf("fail %d: lockedNow = %v", i, locked)
		}
		now = now.Add(time.Minute)
	}
	ok, retry := g.Allow("l1", ip, now)
	if ok || retry != GuardBlock-time.Minute {
		t.Errorf("after lockout: ok=%v retry=%v, want %v", ok, retry, GuardBlock-time.Minute)
	}
	// Other IPs and other links are not blocked.
	if ok, _ := g.Allow("l1", ipN(2), now); !ok {
		t.Error("another IP is blocked")
	}
	if ok, _ := g.Allow("l2", ip, now); !ok {
		t.Error("another link is blocked")
	}
	// 4in6 is the same IP.
	if ok, _ := g.Allow("l1", netip.AddrFrom16(ip.As16()), now); ok {
		t.Error("4in6 form escapes the block")
	}
}

func TestGuardWindowSlides(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	ip := ipN(1)
	// Four fails, then the window passes; four more never lock.
	for i := range GuardPerIPFails - 1 {
		g.Fail("l1", ip, t0.Add(time.Duration(i)*time.Second))
	}
	later := t0.Add(GuardPerIPWindow + time.Minute)
	for i := range GuardPerIPFails - 1 {
		if g.Fail("l1", ip, later.Add(time.Duration(i)*time.Second)) {
			t.Fatal("old fails still count")
		}
	}
	if ok, _ := g.Allow("l1", ip, later.Add(time.Minute)); !ok {
		t.Error("blocked under the limit")
	}
}

func TestGuardPerLinkLimitAcrossIPs(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	locks := 0
	for i := range GuardPerLinkFails {
		if ok, _ := g.Allow("l1", ipN(i), t0); !ok {
			t.Fatalf("IP %d refused before the link limit", i)
		}
		if g.Fail("l1", ipN(i), t0.Add(time.Duration(i)*time.Second)) {
			locks++
		}
	}
	if locks != 1 {
		t.Errorf("lockedNow fired %d times, want 1", locks)
	}
	now := t0.Add(time.Minute)
	// A brand-new IP is blocked too.
	if ok, retry := g.Allow("l1", ipN(999), now); ok || retry <= 0 {
		t.Errorf("new IP after link lockout: ok=%v retry=%v", ok, retry)
	}
	if ok, _ := g.Allow("l2", ipN(999), now); !ok {
		t.Error("another link is blocked")
	}
}

func TestGuardBlockExpires(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{PerIPFails: 2, Block: 10 * time.Minute})
	ip := ipN(1)
	g.Fail("l1", ip, t0)
	if !g.Fail("l1", ip, t0) {
		t.Fatal("no lockout")
	}
	if ok, retry := g.Allow("l1", ip, t0.Add(10*time.Minute-time.Second)); ok || retry != time.Second {
		t.Errorf("just before the end: ok=%v retry=%v", ok, retry)
	}
	if ok, _ := g.Allow("l1", ip, t0.Add(10*time.Minute)); !ok {
		t.Error("still blocked after Block")
	}
	// Counts start fresh after a block: one fail does not lock again.
	if g.Fail("l1", ip, t0.Add(10*time.Minute)) {
		t.Error("one fail after a block locked again")
	}
	if !g.Fail("l1", ip, t0.Add(11*time.Minute)) {
		t.Error("second lockout did not fire")
	}
}

func TestGuardFailWhileBlocked(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{PerIPFails: 1})
	ip := ipN(1)
	if !g.Fail("l1", ip, t0) {
		t.Fatal("no lockout")
	}
	for i := range 10 {
		if g.Fail("l1", ip, t0.Add(time.Duration(i)*time.Second)) {
			t.Fatal("lockedNow fired again while blocked")
		}
	}
	// Fails while blocked do not extend the block.
	if ok, _ := g.Allow("l1", ip, t0.Add(GuardBlock)); !ok {
		t.Error("block was extended")
	}
}

func TestGuardConcurrentLockoutOnce(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	var locks atomic.Int64
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Allow("l1", ipN(i%3), t0)
			if g.Fail("l1", ipN(i%3), t0) {
				locks.Add(1)
			}
		}()
	}
	wg.Wait()
	// Each of three IPs locks at its 5th fail. Later fails are refused, so
	// the link counts 15 and never locks. Three lockouts, one per IP.
	if n := locks.Load(); n != 3 {
		t.Errorf("lockedNow fired %d times, want 3", n)
	}

	// One IP, many goroutines: exactly one lockout.
	g = NewPasswordGuard(GuardOptions{})
	locks.Store(0)
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.Fail("l1", ipN(7), t0) {
				locks.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := locks.Load(); n != 1 {
		t.Errorf("lockedNow fired %d times on one IP, want 1", n)
	}
}

func TestGuardBoundedMemory(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	now := t0
	for i := range 100_000 {
		now = now.Add(time.Second)
		g.Fail("l1", ipN(i%60000), now) // also locks l1 now and then
		g.Fail(string(rune(i)), ipN(i%60000), now)
	}
	// Keys live at most a window plus a block, a couple of hours of
	// one-a-second traffic, doubled by the sweep threshold.
	if n := g.size(); n > 4*(int((GuardPerLinkWin+GuardBlock)/time.Second)) {
		t.Errorf("guard holds %d keys", n)
	}
	later := now.Add(3 * time.Hour)
	g.Allow("x", ipN(1), later)
	if n := g.size(); n != 0 {
		t.Errorf("guard holds %d keys after everything expired", n)
	}
}
