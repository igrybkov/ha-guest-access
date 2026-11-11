package audit

import (
	"sync"
	"time"
)

// maxThrottleKeys bounds a Throttle's memory. Past it, idle keys go, and
// if that is not enough the Throttle starts over.
const maxThrottleKeys = 10_000

// Throttle lets at most one record per key through each period. It is
// for records a client can cause in bulk (slug misses, rate limits), so a
// flood does not push older records out of the rotated file. The record
// that gets through says how many were left out before it.
type Throttle struct {
	every time.Duration

	mu   sync.Mutex
	keys map[string]*throttleKey
}

type throttleKey struct {
	last    time.Time
	skipped int
}

// NewThrottle returns a Throttle that lets one record per key through
// each period every.
func NewThrottle(every time.Duration) *Throttle {
	return &Throttle{every: every, keys: map[string]*throttleKey{}}
}

// Allow reports whether a record for key may go out at now, and how many
// were left out since the last one that did.
func (t *Throttle) Allow(key string, now time.Time) (ok bool, skipped int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := t.keys[key]
	if k == nil {
		if len(t.keys) >= maxThrottleKeys {
			t.sweep(now)
		}
		t.keys[key] = &throttleKey{last: now}
		return true, 0
	}
	if now.Sub(k.last) < t.every {
		k.skipped++
		return false, 0
	}
	skipped, k.last, k.skipped = k.skipped, now, 0
	return true, skipped
}

// sweep drops idle keys, or every key if none is idle. It requires t.mu.
func (t *Throttle) sweep(now time.Time) {
	for key, k := range t.keys {
		if now.Sub(k.last) >= t.every {
			delete(t.keys, key)
		}
	}
	if len(t.keys) >= maxThrottleKeys {
		clear(t.keys)
	}
}
