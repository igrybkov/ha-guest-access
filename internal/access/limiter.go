package access

import (
	"math"
	"sync"
	"time"
)

// minSweepKeys is the map size under which the Limiter does not sweep on
// size alone. Small maps cost nothing to keep.
const minSweepKeys = 1024

// Limiter is a keyed token bucket. Each key (a client IP for slug misses,
// link id plus client IP for actions, and so on) has its own bucket that
// refills at rate tokens per second, up to burst. Safe for concurrent use.
//
// Memory stays bounded by lazy sweeps inside Allow. A sweep drops a key
// that has been idle for idleTTL, and a key whose bucket has refilled to
// full (dropping that one loses nothing). It runs once per idleTTL, or
// when the map has doubled since the last sweep.
type Limiter struct {
	rate    float64
	burst   float64
	idleTTL time.Duration

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
	sweepAt   int // sweep when len(buckets) reaches this
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter returns a Limiter. rate is tokens per second (30 a minute is
// 0.5), burst is the bucket size, idleTTL is how long an unused key is
// kept. A burst under 1 counts as 1. A rate of 0 or less never refills,
// apart from the idle drop.
//
// idleTTL is raised to the time an empty bucket takes to refill, so the
// idle drop never hands out tokens early. A zero or negative idleTTL is
// raised to an hour.
func NewLimiter(rate float64, burst int, idleTTL time.Duration) *Limiter {
	b := float64(max(burst, 1))
	if idleTTL <= 0 {
		idleTTL = time.Hour
	}
	if rate > 0 {
		idleTTL = max(idleTTL, time.Duration(math.Ceil(b/rate*float64(time.Second))))
	}
	return &Limiter{
		rate:    rate,
		burst:   b,
		idleTTL: idleTTL,
		buckets: map[string]*bucket{},
		sweepAt: minSweepKeys,
	}
}

// Allow takes one token from key's bucket at now. When the bucket is empty,
// ok is false and retryAfter says when the next token arrives: Allow at
// now+retryAfter succeeds, if nothing else took the token first.
func (l *Limiter) Allow(key string, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maybeSweep(now)

	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = l.tokensAt(b, now)
	if now.After(b.last) {
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, l.wait(1 - b.tokens)
}

// tokensAt returns b's tokens at now. A clock that steps back adds none.
func (l *Limiter) tokensAt(b *bucket, now time.Time) float64 {
	dt := now.Sub(b.last).Seconds()
	if dt <= 0 || l.rate <= 0 {
		return b.tokens
	}
	return min(l.burst, b.tokens+dt*l.rate)
}

// wait returns how long need tokens take to arrive, rounded up.
func (l *Limiter) wait(need float64) time.Duration {
	if l.rate <= 0 {
		return max(l.idleTTL, time.Second)
	}
	return time.Duration(math.Ceil(need / l.rate * float64(time.Second)))
}

// maybeSweep drops idle and full keys once per idleTTL, or when the map
// has doubled. The caller holds l.mu.
func (l *Limiter) maybeSweep(now time.Time) {
	if len(l.buckets) < l.sweepAt && now.Sub(l.lastSweep) < l.idleTTL {
		return
	}
	for k, b := range l.buckets {
		if now.Sub(b.last) >= l.idleTTL || l.tokensAt(b, now) >= l.burst {
			delete(l.buckets, k)
		}
	}
	l.lastSweep = now
	l.sweepAt = max(minSweepKeys, 2*len(l.buckets))
}

// size returns the number of keys held. Tests use it.
func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
