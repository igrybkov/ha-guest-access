package access

import (
	"net/netip"
	"sync"
	"time"
)

// Password guard defaults.
const (
	GuardPerIPFails   = 5                // fails per link + client IP ...
	GuardPerIPWindow  = 15 * time.Minute // ... in this window
	GuardPerLinkFails = 30               // fails per link, from any IP ...
	GuardPerLinkWin   = time.Hour        // ... in this window
	GuardBlock        = time.Hour        // per-IP block once over the limit
	GuardLinkDelay    = 10 * time.Second // least time between tries on a slowed link
)

// GuardOptions set the PasswordGuard limits. Zero fields take the Guard*
// defaults.
type GuardOptions struct {
	PerIPFails   int
	PerIPWindow  time.Duration
	PerLinkFails int
	PerLinkWin   time.Duration
	// Block is how long an IP stays blocked on a link.
	Block time.Duration
	// LinkDelay is the least time between two tries on a slowed link.
	LinkDelay time.Duration
	// IPv6Prefix is the prefix length an IPv6 client is keyed on (see
	// KeyAddr). Zero means DefaultIPv6Prefix.
	IPv6Prefix int
}

// PasswordGuard locks the password form after too many wrong tries.
//
//   - Per link + client IP (an IPv6 client by its IPv6Prefix, see
//     KeyAddr): PerIPFails fails within PerIPWindow block that
//     IP on that link for Block. Fail counts of that key reset when the
//     block starts.
//   - Per link: PerLinkFails fails from any IPs within PerLinkWin slow the
//     link down. A slowed link takes one try per LinkDelay, from any IP.
//     The delay runs from the last try that was let through, so refused
//     requests can't push it back and keep a real guest out. A guess
//     flood across many IPs gets about one try per LinkDelay; a guest
//     waits at most LinkDelay. The link speeds up again once its fails
//     age out of PerLinkWin.
//
// Safe for concurrent use.
//
// Allow holds a slot in both scopes until Fail turns it into a fail, or
// slotTTL passes (a right password never calls Fail). Allow refuses while
// fails plus slots reach the per-IP limit, and treats the link as slowed
// once fails plus slots reach the per-link limit. So parallel guesses,
// which all pass Allow before argon2 lets any Fail land, still get at
// most the limit. Memory stays bounded: each key keeps at most its limit
// of fail times and open slots, and a lazy sweep drops keys with no
// recent fails, no block and no recent try.
type PasswordGuard struct {
	opts GuardOptions

	mu        sync.Mutex
	perIP     map[guardIPKey]*failLog
	perLink   map[string]*failLog
	lastSweep time.Time
	sweepAt   int // sweep when the maps hold this many keys
}

type guardIPKey struct {
	link string
	ip   netip.Addr
}

// slotTTL is how long an Allow holds its slot. It covers one argon2 check
// with room to spare, and frees the slot of a right password or a request
// that died.
const slotTTL = 10 * time.Second

// failLog is the recent fails of one key and its open slots. A per-IP key
// uses blockedUntil; a per-link key uses lastTry and slowed.
type failLog struct {
	fails        []time.Time // oldest first, all within the window
	slots        []time.Time // when each open slot was taken, oldest first
	blockedUntil time.Time
	lastTry      time.Time // the last try let through
	slowed       bool      // fails reached the limit; reported once
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
	if o.LinkDelay == 0 {
		o.LinkDelay = GuardLinkDelay
	}
	return &PasswordGuard{
		opts:    o,
		perIP:   map[guardIPKey]*failLog{},
		perLink: map[string]*failLog{},
		sweepAt: minSweepKeys,
	}
}

// Allow reports whether ip may try a password on linkID at now. When not,
// retryAfter says how long the wait is, and slowed tells why: true when
// the link is slowed down, false when the IP is blocked or has no free
// slot.
//
// A true answer holds a slot (see PasswordGuard), so call Allow only right
// before a real password check, never just to render the form.
func (g *PasswordGuard) Allow(linkID string, ip netip.Addr, now time.Time) (ok bool, retryAfter time.Duration, slowed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.maybeSweep(now)
	link, byIP := g.logs(linkID, ip)
	if wait := byIP.wait(now, g.opts.PerIPWindow, g.opts.PerIPFails); wait > 0 {
		return false, wait, false
	}
	if wait := link.slowWait(now, g.opts.PerLinkWin, g.opts.PerLinkFails, g.opts.LinkDelay); wait > 0 {
		return false, wait, true
	}
	link.slots = append(link.slots, now)
	link.lastTry = now
	byIP.slots = append(byIP.slots, now)
	return true, 0, false
}

// logs returns the fail logs of linkID and of linkID + ip, made if new.
// The caller holds g.mu.
func (g *PasswordGuard) logs(linkID string, ip netip.Addr) (link, byIP *failLog) {
	key := guardIPKey{linkID, KeyAddr(ip, g.opts.IPv6Prefix)}
	link, byIP = g.perLink[linkID], g.perIP[key]
	if link == nil {
		link = &failLog{}
		g.perLink[linkID] = link
	}
	if byIP == nil {
		byIP = &failLog{}
		g.perIP[key] = byIP
	}
	return link, byIP
}

// Fail records a wrong password. ipBlocked is true when this fail starts
// a block of ip on linkID. linkSlowed is true when this fail slows the
// link down; it is true once per slow spell, not on each fail during it.
// The caller fires guest_access_lockout on either. A fail while ip is
// blocked counts for nothing; Allow should have refused it.
func (g *PasswordGuard) Fail(linkID string, ip netip.Addr, now time.Time) (ipBlocked, linkSlowed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.maybeSweep(now)

	link, byIP := g.logs(linkID, ip)
	link.release()
	byIP.release()
	if now.Before(byIP.blockedUntil) {
		return false, false
	}
	linkSlowed = link.addLinkFail(now, g.opts.PerLinkWin, g.opts.PerLinkFails)
	ipBlocked = byIP.add(now, g.opts.PerIPWindow, g.opts.PerIPFails, g.opts.Block)
	return ipBlocked, linkSlowed
}

// slowWait returns how long a try on a link must wait at now: zero while
// fails plus slots are under limit, else the rest of delay since the last
// try let through. A clock that stepped back restarts the delay from now.
func (f *failLog) slowWait(now time.Time, window time.Duration, limit int, delay time.Duration) time.Duration {
	f.prune(now, window)
	if len(f.fails) < limit {
		f.slowed = false
	}
	if len(f.fails)+len(f.slots) < limit || f.lastTry.IsZero() {
		return 0
	}
	if now.Before(f.lastTry) {
		f.lastTry = now
	}
	return max(0, f.lastTry.Add(delay).Sub(now))
}

// addLinkFail records a fail on a link at now. It keeps the newest limit
// fails, which is all the slow check needs. It reports whether this fail
// started a slow spell.
func (f *failLog) addLinkFail(now time.Time, window time.Duration, limit int) bool {
	f.prune(now, window)
	if len(f.fails) < limit {
		f.slowed = false
	}
	f.fails = append(f.fails, now)
	if over := len(f.fails) - limit; over > 0 {
		f.fails = f.fails[over:]
	}
	if len(f.fails) < limit || f.slowed {
		return false
	}
	f.slowed = true
	return true
}

// wait returns how long until f has room for one more try at now: the
// rest of a block, or until the oldest fail or slot runs out. Zero means
// there is room now.
func (f *failLog) wait(now time.Time, window time.Duration, limit int) time.Duration {
	if now.Before(f.blockedUntil) {
		return f.blockedUntil.Sub(now)
	}
	f.prune(now, window)
	if len(f.fails)+len(f.slots) < limit {
		return 0
	}
	var free time.Time
	if len(f.fails) > 0 {
		free = f.fails[0].Add(window)
	}
	if len(f.slots) > 0 {
		if s := f.slots[0].Add(slotTTL); free.IsZero() || s.Before(free) {
			free = s
		}
	}
	return max(free.Sub(now), time.Nanosecond)
}

// release frees the oldest open slot, if any.
func (f *failLog) release() {
	if len(f.slots) > 0 {
		f.slots = f.slots[1:]
	}
}

// add records a fail at now and reports whether it reached limit, which
// starts a block.
func (f *failLog) add(now time.Time, window time.Duration, limit int, block time.Duration) bool {
	f.prune(now, window)
	f.fails = append(f.fails, now)
	if len(f.fails) < limit {
		return false
	}
	f.fails = nil
	f.slots = nil
	f.blockedUntil = now.Add(block)
	return true
}

// prune drops fails older than window and slots older than slotTTL.
func (f *failLog) prune(now time.Time, window time.Duration) {
	i := 0
	for i < len(f.fails) && now.Sub(f.fails[i]) >= window {
		i++
	}
	f.fails = f.fails[i:]
	i = 0
	for i < len(f.slots) && now.Sub(f.slots[i]) >= slotTTL {
		i++
	}
	f.slots = f.slots[i:]
}

// idle reports whether f holds nothing: no block, no slot, no fail in
// window and no try within delay.
func (f *failLog) idle(now time.Time, window, delay time.Duration) bool {
	f.prune(now, window)
	return len(f.fails) == 0 && len(f.slots) == 0 && !now.Before(f.blockedUntil) && now.Sub(f.lastTry) >= delay
}

// maybeSweep drops idle keys once per the shorter window, or when the maps
// have doubled. The caller holds g.mu.
func (g *PasswordGuard) maybeSweep(now time.Time) {
	n := len(g.perIP) + len(g.perLink)
	if n < g.sweepAt && now.Sub(g.lastSweep) < min(g.opts.PerIPWindow, g.opts.PerLinkWin) {
		return
	}
	for k, f := range g.perIP {
		if f.idle(now, g.opts.PerIPWindow, 0) {
			delete(g.perIP, k)
		}
	}
	for k, f := range g.perLink {
		if f.idle(now, g.opts.PerLinkWin, g.opts.LinkDelay) {
			delete(g.perLink, k)
		}
	}
	g.lastSweep = now
	g.sweepAt = max(minSweepKeys, 2*(len(g.perIP)+len(g.perLink)))
}

// size returns the number of keys held. Tests use it.
func (g *PasswordGuard) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.perIP) + len(g.perLink)
}
