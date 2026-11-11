package web

import (
	"net/netip"
	"sync"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/config"
)

// missGlobalKey is the one key of the global slug miss limiter.
const missGlobalKey = "*"

// rateLimiter is access.Limiter, or noLimit when limits are off.
type rateLimiter interface {
	Allow(key string, now time.Time) (ok bool, retryAfter time.Duration)
}

// passwordGuard is access.PasswordGuard, or noLimit when limits are off.
type passwordGuard interface {
	Allow(linkID string, ip netip.Addr, now time.Time) (ok bool, retryAfter time.Duration, slowed bool)
	Fail(linkID string, ip netip.Addr, now time.Time) (ipBlocked, linkSlowed bool)
}

// noLimit lets everything through. It stands in for every limiter when
// the user turned rate limits off.
type noLimit struct{}

func (noLimit) Allow(string, time.Time) (bool, time.Duration) { return true, 0 }

type noGuard struct{}

func (noGuard) Allow(string, netip.Addr, time.Time) (bool, time.Duration, bool) {
	return true, 0, false
}
func (noGuard) Fail(string, netip.Addr, time.Time) (bool, bool) { return false, false }

// limitSet holds every limiter of the app. Every route has one.
//
//   - preauth: every guest request that is not from an authenticated
//     guest, per client IP. Static assets do not use it.
//   - assets: static assets, per client IP. They are public and never
//     change under one URL, so a page load costs one preauth token only.
//   - authed: state reads and discrete actions of an authenticated guest,
//     per link and client IP.
//   - continuous: continuous actions (sliders) of an authenticated guest,
//     per link, client IP and entity. They do not use authed.
//   - missIP and missAll: unknown slugs, per client IP and across all IPs.
//   - admin: the admin port, per peer.
//   - guard: wrong passwords. It blocks an IP, and slows a link down.
//   - streams and streamsPerIP: the caps on open event streams per link,
//     and per link and client inside that.
type limitSet struct {
	off          bool
	preauth      rateLimiter
	assets       rateLimiter
	authed       rateLimiter
	cont         rateLimiter
	missIP       rateLimiter
	missAll      rateLimiter
	admin        rateLimiter
	guard        passwordGuard
	streams      int // 0 = no cap
	streamsPerIP int // 0 = no cap
}

// newLimitSet builds the limiters from the app options. With limits off,
// every one of them is a no-op.
func newLimitSet(o config.Options) *limitSet {
	if !o.LimitsEnabled {
		return &limitSet{off: true, preauth: noLimit{}, assets: noLimit{}, authed: noLimit{}, cont: noLimit{}, missIP: noLimit{},
			missAll: noLimit{}, admin: noLimit{}, guard: noGuard{}}
	}
	rl := o.Settings.RateLimits
	perMin := func(n int) float64 { return float64(n) / 60 }
	perHour := func(n int) float64 { return float64(n) / 3600 }
	return &limitSet{
		preauth: access.NewLimiter(perMin(rl.GuestPerMin), rl.GuestBurst, time.Hour),
		assets:  access.NewLimiter(perMin(rl.AssetsPerMin), rl.AssetsBurst, time.Hour),
		authed:  access.NewLimiter(perMin(rl.ActionsPerMin), rl.ActionsBurst, time.Hour),
		cont:    access.NewLimiter(perMin(rl.ContinuousPerMin), rl.ContinuousBurst, time.Hour),
		missIP:  access.NewLimiter(perHour(rl.MissesPerHourPerIP), rl.MissesPerHourPerIP, time.Hour),
		missAll: access.NewLimiter(perHour(rl.MissesPerHourGlobal), rl.MissesPerHourGlobal, time.Hour),
		admin:   access.NewLimiter(perMin(rl.AdminPerMin), rl.AdminPerMin, time.Hour),
		guard: access.NewPasswordGuard(access.GuardOptions{
			PerIPFails:   rl.PasswordFailsPerIP,
			PerIPWindow:  o.PasswordWindowPerIP,
			PerLinkFails: rl.PasswordFailsPerLink,
			PerLinkWin:   o.PasswordWindowPerLink,
			Block:        time.Duration(rl.PasswordLockoutMin) * time.Minute,
			LinkDelay:    time.Duration(rl.PasswordLinkDelayS) * time.Second,
			IPv6Prefix:   rl.IPv6Prefix,
		}),
		streams:      rl.MaxStreamsPerLink,
		streamsPerIP: o.StreamMaxPerIP,
	}
}

// streamCounter counts open event streams per link, and per link and
// client. The per-client cap keeps one address from taking every slot of
// a link and shutting the other guests out of live updates.
type streamCounter struct {
	mu       sync.Mutex
	open     map[string]int
	byClient map[streamKey]int
}

type streamKey struct{ link, client string }

func newStreamCounter() streamCounter {
	return streamCounter{open: map[string]int{}, byClient: map[streamKey]int{}}
}

// acquire takes a stream slot of linkID for client (a limiter key) if
// fewer than perLink are open on the link and fewer than perClient for
// the client on it. A cap of 0 means none. Call release with the same
// arguments when the stream ends.
func (c *streamCounter) acquire(linkID, client string, perLink, perClient int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := streamKey{linkID, client}
	if (perLink > 0 && c.open[linkID] >= perLink) || (perClient > 0 && c.byClient[k] >= perClient) {
		return false
	}
	c.open[linkID]++
	c.byClient[k]++
	return true
}

func (c *streamCounter) release(linkID, client string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open[linkID]--; c.open[linkID] <= 0 {
		delete(c.open, linkID)
	}
	k := streamKey{linkID, client}
	if c.byClient[k]--; c.byClient[k] <= 0 {
		delete(c.byClient, k)
	}
}

func (c *streamCounter) count(linkID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open[linkID]
}
