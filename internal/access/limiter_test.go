package access

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiterBurst(t *testing.T) {
	l := NewLimiter(0.5, 3, time.Hour)
	for i := range 3 {
		if ok, _ := l.Allow("k", t0); !ok {
			t.Fatalf("call %d refused inside the burst", i)
		}
	}
	ok, retry := l.Allow("k", t0)
	if ok {
		t.Fatal("call over the burst allowed")
	}
	if retry != 2*time.Second {
		t.Errorf("retryAfter = %v, want 2s", retry)
	}
	// Other keys have their own bucket.
	if ok, _ := l.Allow("other", t0); !ok {
		t.Error("other key refused")
	}
}

func TestLimiterRefill(t *testing.T) {
	l := NewLimiter(1, 2, time.Hour)
	l.Allow("k", t0)
	l.Allow("k", t0)
	if ok, _ := l.Allow("k", t0.Add(500*time.Millisecond)); ok {
		t.Error("allowed before a full token came back")
	}
	if ok, _ := l.Allow("k", t0.Add(time.Second)); !ok {
		t.Error("refused after one token came back")
	}
	// A long pause refills to burst, never past it.
	at := t0.Add(time.Hour)
	for i := range 2 {
		if ok, _ := l.Allow("k", at); !ok {
			t.Fatalf("call %d after a pause refused", i)
		}
	}
	if ok, _ := l.Allow("k", at); ok {
		t.Error("bucket grew past burst")
	}
}

// Allow at now+retryAfter succeeds, and a moment earlier it does not.
func TestLimiterRetryAfterAccurate(t *testing.T) {
	for _, rate := range []float64{0.5, 1.0 / 3, 20.0 / 60, 7, 1000} {
		l := NewLimiter(rate, 1, time.Hour)
		now := t0
		l.Allow("k", now)
		for i := range 20 {
			ok, retry := l.Allow("k", now)
			if ok || retry <= 0 {
				t.Fatalf("rate %v step %d: ok=%v retry=%v", rate, i, ok, retry)
			}
			if ok, _ := l.Allow("k", now.Add(retry-time.Millisecond)); ok && retry > time.Millisecond {
				t.Fatalf("rate %v step %d: allowed 1ms before retryAfter %v", rate, i, retry)
			}
			now = now.Add(retry)
			if ok, _ := l.Allow("k", now); !ok {
				t.Fatalf("rate %v step %d: refused at retryAfter %v", rate, i, retry)
			}
		}
	}
}

func TestLimiterClockBack(t *testing.T) {
	l := NewLimiter(1, 1, time.Hour)
	l.Allow("k", t0)
	if ok, _ := l.Allow("k", t0.Add(-time.Hour)); ok {
		t.Error("a clock step back refilled the bucket")
	}
	if ok, _ := l.Allow("k", t0.Add(time.Second)); !ok {
		t.Error("refused after the clock came back")
	}
}

func TestLimiterOddSettings(t *testing.T) {
	l := NewLimiter(0, 0, 0)
	if ok, _ := l.Allow("k", t0); !ok {
		t.Error("burst 0 does not allow one call")
	}
	ok, retry := l.Allow("k", t0.Add(time.Minute))
	if ok || retry <= 0 {
		t.Errorf("rate 0 refilled: ok=%v retry=%v", ok, retry)
	}
	// A short idleTTL never hands out tokens before the refill time.
	l = NewLimiter(1.0/60, 1, time.Second)
	l.Allow("k", t0)
	for i := range 2000 {
		l.Allow(fmt.Sprint("filler", i), t0.Add(10*time.Second))
	}
	if ok, _ := l.Allow("k", t0.Add(30*time.Second)); ok {
		t.Error("idle drop refilled a key early")
	}
}

func TestLimiterBoundedMemory(t *testing.T) {
	l := NewLimiter(20.0/60, 15, time.Hour)
	now := t0
	// A flood of one-off keys, a few each second for a day.
	for i := range 200_000 {
		now = now.Add(500 * time.Millisecond)
		l.Allow(fmt.Sprint("ip", i), now)
	}
	// A key refills to full in 45 s, so it is dropped soon after. The map
	// holds at most what the last sweep left, doubled.
	if n := l.size(); n > 2*minSweepKeys {
		t.Errorf("limiter holds %d keys", n)
	}
	// A busy key survives sweeps: it keeps its empty bucket.
	for range 15 {
		l.Allow("busy", now)
	}
	for i := range 5000 {
		l.Allow(fmt.Sprint("x", i), now)
	}
	if ok, _ := l.Allow("busy", now); ok {
		t.Error("sweep dropped a busy key")
	}
}

func TestLimiterConcurrent(t *testing.T) {
	const burst = 50
	l := NewLimiter(0.001, burst, time.Hour)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				if ok, _ := l.Allow("shared", t0); ok {
					allowed.Add(1)
				}
				l.Allow(fmt.Sprint(g, "-", i), t0.Add(time.Duration(i)*time.Second))
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != burst {
		t.Errorf("%d calls allowed on one key, want %d", got, burst)
	}
}
