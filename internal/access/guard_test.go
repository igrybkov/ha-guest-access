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

// ipFail calls Fail and returns only whether it blocked the IP.
func ipFail(g *PasswordGuard, link string, ip netip.Addr, now time.Time) bool {
	blocked, _ := g.Fail(link, ip, now)
	return blocked
}

func TestNewPasswordGuardDefaults(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{PerIPFails: 3})
	want := GuardOptions{PerIPFails: 3, PerIPWindow: GuardPerIPWindow, PerLinkFails: GuardPerLinkFails, PerLinkWin: GuardPerLinkWin,
		Block: GuardBlock, LinkDelay: GuardLinkDelay}
	if g.opts != want {
		t.Fatalf("opts = %+v, want %+v", g.opts, want)
	}
}

func TestGuardPerIPLimit(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	ip := ipN(1)
	now := t0
	for i := range GuardPerIPFails {
		if ok, _, _ := g.Allow("l1", ip, now); !ok {
			t.Fatalf("try %d refused", i)
		}
		locked, _ := g.Fail("l1", ip, now)
		if locked != (i == GuardPerIPFails-1) {
			t.Fatalf("fail %d: lockedNow = %v", i, locked)
		}
		now = now.Add(time.Minute)
	}
	ok, retry, slowed := g.Allow("l1", ip, now)
	if ok || slowed || retry != GuardBlock-time.Minute {
		t.Errorf("after lockout: ok=%v retry=%v, want %v", ok, retry, GuardBlock-time.Minute)
	}
	// Other IPs and other links are not blocked.
	if ok, _, _ := g.Allow("l1", ipN(2), now); !ok {
		t.Error("another IP is blocked")
	}
	if ok, _, _ := g.Allow("l2", ip, now); !ok {
		t.Error("another link is blocked")
	}
	// 4in6 is the same IP.
	if ok, _, _ := g.Allow("l1", netip.AddrFrom16(ip.As16()), now); ok {
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
		if ipFail(g, "l1", ip, later.Add(time.Duration(i)*time.Second)) {
			t.Fatal("old fails still count")
		}
	}
	if ok, _, _ := g.Allow("l1", ip, later.Add(time.Minute)); !ok {
		t.Error("blocked under the limit")
	}
}

// slowLink drives link l1 into slow mode with one fail from each of
// GuardPerLinkFails IPs, a second apart from t0. It returns the time of
// the last fail.
func slowLink(t *testing.T, g *PasswordGuard) time.Time {
	t.Helper()
	slowed := 0
	var now time.Time
	for i := range GuardPerLinkFails {
		now = t0.Add(time.Duration(i) * time.Second)
		if ok, _, _ := g.Allow("l1", ipN(i), now); !ok {
			t.Fatalf("IP %d refused before the link limit", i)
		}
		if _, s := g.Fail("l1", ipN(i), now); s {
			slowed++
		}
	}
	if slowed != 1 {
		t.Fatalf("linkSlowed fired %d times, want 1", slowed)
	}
	return now
}

// Past the per-link limit the link slows down for every IP, but a guest
// from a fresh IP still gets in once the delay has passed.
func TestGuardPerLinkSlowsAcrossIPs(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	last := slowLink(t, g)

	// A fresh IP right after the last try waits the rest of the delay.
	now := last.Add(3 * time.Second)
	ok, retry, slowed := g.Allow("l1", ipN(999), now)
	if ok || !slowed || retry != GuardLinkDelay-3*time.Second {
		t.Fatalf("fresh IP inside the delay: ok=%v slowed=%v retry=%v", ok, slowed, retry)
	}
	// Refused tries do not push the delay back.
	for range 5 {
		g.Allow("l1", ipN(998), now)
	}
	if ok, _, _ := g.Allow("l1", ipN(999), last.Add(GuardLinkDelay)); !ok {
		t.Fatal("fresh IP refused after the delay")
	}
	// The next try waits a full delay from that one.
	if ok, retry, slowed := g.Allow("l1", ipN(997), last.Add(GuardLinkDelay+time.Second)); ok || !slowed || retry != GuardLinkDelay-time.Second {
		t.Errorf("second try: ok=%v slowed=%v retry=%v", ok, slowed, retry)
	}
	// Other links are not slowed.
	if ok, _, _ := g.Allow("l2", ipN(999), now); !ok {
		t.Error("another link is slowed")
	}
}

// A slowed link speeds up again once its fails age out of the window.
func TestGuardSlowEnds(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	last := slowLink(t, g)
	// Just before the last fail ages out, the link is still slow.
	g.Allow("l1", ipN(500), last.Add(GuardPerLinkWin-time.Minute))
	if ok, _, slowed := g.Allow("l1", ipN(501), last.Add(GuardPerLinkWin-time.Minute)); ok || !slowed {
		t.Fatalf("inside the window: ok=%v slowed=%v", ok, slowed)
	}
	// Once every fail is out of the window, tries go at full speed.
	end := last.Add(GuardPerLinkWin)
	for i := range 3 {
		if ok, _, slowed := g.Allow("l1", ipN(600+i), end); !ok || slowed {
			t.Fatalf("try %d after the window: ok=%v slowed=%v", i, ok, slowed)
		}
	}
	// A new slow spell reports again.
	g = NewPasswordGuard(GuardOptions{})
	slowLink(t, g)
	later := t0.Add(2 * GuardPerLinkWin)
	n := 0
	for i := range GuardPerLinkFails {
		if _, s := g.Fail("l1", ipN(i), later.Add(time.Duration(i)*time.Second)); s {
			n++
		}
	}
	if n != 1 {
		t.Errorf("second spell: linkSlowed fired %d times, want 1", n)
	}
}

// A long guessing run across many IPs gets about one try per delay, and
// reports the slow spell once.
func TestGuardSlowCapsBruteForce(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	start := slowLink(t, g)
	allowed, spells := 0, 0
	// Every IP tries once a second for an hour; each is new, so the per-IP
	// limit never helps.
	for i := range 3600 {
		now := start.Add(time.Duration(i+1) * time.Second)
		ip := ipN(1000 + i)
		ok, _, _ := g.Allow("l1", ip, now)
		if !ok {
			continue
		}
		allowed++
		if _, s := g.Fail("l1", ip, now); s {
			spells++
		}
	}
	want := int(time.Hour / GuardLinkDelay)
	if allowed > want+1 || allowed < want-1 {
		t.Errorf("%d tries in an hour, want about %d", allowed, want)
	}
	if spells != 0 {
		t.Errorf("linkSlowed fired %d more times during the run", spells)
	}
}

// Parallel tries on a slowed link at the same instant: exactly one gets
// through.
func TestGuardSlowParallel(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	last := slowLink(t, g)
	now := last.Add(GuardLinkDelay)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _, _ := g.Allow("l1", ipN(2000+i), now); ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := allowed.Load(); n != 1 {
		t.Errorf("%d parallel tries allowed on a slowed link, want 1", n)
	}
}

// The per-IP block still applies on a slowed link, and wins: a blocked IP
// is refused even when the delay has passed.
func TestGuardSlowKeepsIPBlock(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	last := slowLink(t, g)
	ip := ipN(3000)
	now := last
	for i := range GuardPerIPFails {
		now = now.Add(GuardLinkDelay)
		if ok, _, _ := g.Allow("l1", ip, now); !ok {
			t.Fatalf("try %d refused", i)
		}
		blocked, _ := g.Fail("l1", ip, now)
		if blocked != (i == GuardPerIPFails-1) {
			t.Fatalf("fail %d: ipBlocked = %v", i, blocked)
		}
	}
	ok, retry, slowed := g.Allow("l1", ip, now.Add(GuardLinkDelay))
	if ok || slowed || retry != GuardBlock-GuardLinkDelay {
		t.Errorf("blocked IP: ok=%v slowed=%v retry=%v", ok, slowed, retry)
	}
	if ok, _, _ := g.Allow("l1", ipN(3001), now.Add(GuardLinkDelay)); !ok {
		t.Error("another IP refused after the delay")
	}
}

func TestGuardBlockExpires(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{PerIPFails: 2, Block: 10 * time.Minute})
	ip := ipN(1)
	g.Fail("l1", ip, t0)
	if !ipFail(g, "l1", ip, t0) {
		t.Fatal("no lockout")
	}
	if ok, retry, _ := g.Allow("l1", ip, t0.Add(10*time.Minute-time.Second)); ok || retry != time.Second {
		t.Errorf("just before the end: ok=%v retry=%v", ok, retry)
	}
	if ok, _, _ := g.Allow("l1", ip, t0.Add(10*time.Minute)); !ok {
		t.Error("still blocked after Block")
	}
	// Counts start fresh after a block: one fail does not lock again.
	if ipFail(g, "l1", ip, t0.Add(10*time.Minute)) {
		t.Error("one fail after a block locked again")
	}
	if !ipFail(g, "l1", ip, t0.Add(11*time.Minute)) {
		t.Error("second lockout did not fire")
	}
}

func TestGuardFailWhileBlocked(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{PerIPFails: 1})
	ip := ipN(1)
	if !ipFail(g, "l1", ip, t0) {
		t.Fatal("no lockout")
	}
	for i := range 10 {
		if ipFail(g, "l1", ip, t0.Add(time.Duration(i)*time.Second)) {
			t.Fatal("lockedNow fired again while blocked")
		}
	}
	// Fails while blocked do not extend the block.
	if ok, _, _ := g.Allow("l1", ip, t0.Add(GuardBlock)); !ok {
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
			if ipFail(g, "l1", ipN(i%3), t0) {
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
			if ipFail(g, "l1", ipN(7), t0) {
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
	// Only the two keys of that last Allow (link, link + IP) are left.
	if n := g.size(); n != 2 {
		t.Errorf("guard holds %d keys after everything expired, want 2", n)
	}
}

// Parallel guesses can't outrun the guard. Every request passes Allow
// before any Fail lands, as when argon2 runs between them. Allow holds a
// slot, so at most the limit get through.
func TestGuardParallelGuesses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ips   int
		limit int
	}{
		{"one IP", 1, GuardPerIPFails},
		{"many IPs", 100, GuardPerLinkFails},
	} {
		g := NewPasswordGuard(GuardOptions{})
		var allowed atomic.Int64
		var ready, wg sync.WaitGroup
		start := make(chan struct{})
		for i := range 200 {
			ready.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				ip := ipN(i % tc.ips)
				ok, _, _ := g.Allow("l1", ip, t0)
				if ok {
					allowed.Add(1)
				}
				ready.Done()
				<-start // every Allow is in before any Fail
				if ok {
					g.Fail("l1", ip, t0.Add(time.Second))
				}
			}()
		}
		ready.Wait()
		close(start)
		wg.Wait()
		if n := allowed.Load(); n > int64(tc.limit) {
			t.Errorf("%s: %d guesses allowed in parallel, limit %d", tc.name, n, tc.limit)
		}
	}
}

// A right password never calls Fail. Its slot frees after slotTTL.
func TestGuardSlotsExpire(t *testing.T) {
	g := NewPasswordGuard(GuardOptions{})
	ip := ipN(1)
	for i := range GuardPerIPFails {
		if ok, _, _ := g.Allow("l1", ip, t0); !ok {
			t.Fatalf("Allow %d refused", i)
		}
	}
	ok, retry, _ := g.Allow("l1", ip, t0)
	if ok || retry != slotTTL {
		t.Fatalf("all slots held: ok=%v retry=%v, want %v", ok, retry, slotTTL)
	}
	if ok, _, _ := g.Allow("l1", ip, t0.Add(slotTTL)); !ok {
		t.Error("slots did not expire")
	}
	// Fail frees a slot: the next Allow has room again, minus the fail.
	g = NewPasswordGuard(GuardOptions{})
	for range GuardPerIPFails - 1 {
		g.Allow("l1", ip, t0)
	}
	g.Fail("l1", ip, t0)
	if ok, _, _ := g.Allow("l1", ip, t0); !ok {
		t.Error("Fail did not free its slot")
	}
	if ok, _, _ := g.Allow("l1", ip, t0); ok {
		t.Error("room past the limit")
	}
}
