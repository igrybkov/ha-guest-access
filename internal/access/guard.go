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

// PasswordGuard locks the password form after too many wrong tries.
//
//   - Per link + client IP: PerIPFails fails within PerIPWindow block that
//     IP on that link for Block.
//   - Per link: PerLinkFails fails from any IPs within PerLinkWin block the
//     link for everyone for Block.
//
// The fail that reaches a limit starts the block. Fail counts reset when a
// block starts. Safe for concurrent use. Memory stays bounded: each key
// keeps at most its limit of fail times, and a lazy sweep drops keys with
// no recent fails and no block.
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

// failLog is the recent fails of one key and its block, if any.
type failLog struct {
	fails        []time.Time // oldest first, all within the window
	blockedUntil time.Time
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
	return &PasswordGuard{
		opts:    o,
		perIP:   map[guardIPKey]*failLog{},
		perLink: map[string]*failLog{},
		sweepAt: minSweepKeys,
	}
}

// Allow reports whether ip may try a password on linkID at now. When not,
// retryAfter says how long the block still lasts.
func (g *PasswordGuard) Allow(linkID string, ip netip.Addr, now time.Time) (ok bool, retryAfter time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.maybeSweep(now)
	var wait time.Duration
	if l := g.perLink[linkID]; l != nil && now.Before(l.blockedUntil) {
		wait = l.blockedUntil.Sub(now)
	}
	if l := g.perIP[guardIPKey{linkID, normalize(ip)}]; l != nil && now.Before(l.blockedUntil) {
		wait = max(wait, l.blockedUntil.Sub(now))
	}
	return wait == 0, wait
}

// Fail records a wrong password. lockedNow is true when this fail starts a
// block, so the caller fires guest_access_lockout once per lockout. A fail
// while already blocked counts for nothing; Allow should have refused it.
func (g *PasswordGuard) Fail(linkID string, ip netip.Addr, now time.Time) (lockedNow bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.maybeSweep(now)

	key := guardIPKey{linkID, normalize(ip)}
	link, byIP := g.perLink[linkID], g.perIP[key]
	if link == nil {
		link = &failLog{}
		g.perLink[linkID] = link
	}
	if byIP == nil {
		byIP = &failLog{}
		g.perIP[key] = byIP
	}
	if now.Before(link.blockedUntil) || now.Before(byIP.blockedUntil) {
		return false
	}
	if link.add(now, g.opts.PerLinkWin, g.opts.PerLinkFails, g.opts.Block) {
		lockedNow = true
	}
	if byIP.add(now, g.opts.PerIPWindow, g.opts.PerIPFails, g.opts.Block) {
		lockedNow = true
	}
	return lockedNow
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
	f.blockedUntil = now.Add(block)
	return true
}

// prune drops fails older than window.
func (f *failLog) prune(now time.Time, window time.Duration) {
	i := 0
	for i < len(f.fails) && now.Sub(f.fails[i]) >= window {
		i++
	}
	f.fails = f.fails[i:]
}

// idle reports whether f holds nothing: no block and no fail in window.
func (f *failLog) idle(now time.Time, window time.Duration) bool {
	f.prune(now, window)
	return len(f.fails) == 0 && !now.Before(f.blockedUntil)
}

// maybeSweep drops idle keys once per the shorter window, or when the maps
// have doubled. The caller holds g.mu.
func (g *PasswordGuard) maybeSweep(now time.Time) {
	n := len(g.perIP) + len(g.perLink)
	if n < g.sweepAt && now.Sub(g.lastSweep) < min(g.opts.PerIPWindow, g.opts.PerLinkWin) {
		return
	}
	for k, f := range g.perIP {
		if f.idle(now, g.opts.PerIPWindow) {
			delete(g.perIP, k)
		}
	}
	for k, f := range g.perLink {
		if f.idle(now, g.opts.PerLinkWin) {
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
