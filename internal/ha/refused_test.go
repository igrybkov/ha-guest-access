package ha

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"error result", &Error{Code: "service_validation_error", Message: "x"}, true},
		{"wrapped error result", fmt.Errorf("light.turn_on: %w", &Error{Code: "home_assistant_error"}), true},
		{"disconnected", fmt.Errorf("light.turn_on: %w", ErrDisconnected), false},
		{"timeout", fmt.Errorf("light.turn_on: %w", context.DeadlineExceeded), false},
		{"canceled", context.Canceled, false},
		{"plain error", errors.New("write: broken pipe"), false},
	} {
		if got := Refused(tc.err); got != tc.want {
			t.Errorf("%s: Refused = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// silentHA is an HA that authenticates and never answers a service
// call. With hangUp it drops the connection when the call arrives.
func silentHA(t *testing.T, hangUp bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"type":"auth_required"}`))
		if _, _, err := ws.Read(r.Context()); err != nil {
			return
		}
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"type":"auth_ok"}`))
		for {
			_, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			if strings.Contains(string(data), `"call_service"`) && hangUp {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// What the real client returns for each way a call can fail, and which
// of them Refused counts as a definite refusal.
func TestCallServiceFailureKinds(t *testing.T) {
	t.Run("error result", func(t *testing.T) {
		f := newFakeHA(t)
		c, _, _ := startClient(t, f, testToken)
		f.mu.Lock()
		f.serviceErr = &Error{Code: "service_validation_error", Message: "bad entity"}
		f.mu.Unlock()
		_, err := c.CallService(testCtx(t), "lock", "unlock", nil, nil, false)
		if err == nil || !Refused(err) {
			t.Errorf("err = %v, Refused = %v, want true", err, Refused(err))
		}
	})
	t.Run("timeout", func(t *testing.T) {
		c := New(Options{URL: silentHA(t, false), Token: "x", Logger: quietLog})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = c.Run(ctx) }()
		cctx, ccancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer ccancel()
		_, err := c.CallService(cctx, "lock", "unlock", nil, nil, false)
		if !errors.Is(err, context.DeadlineExceeded) || Refused(err) {
			t.Errorf("err = %v, Refused = %v, want a timeout that is not a refusal", err, Refused(err))
		}
	})
	t.Run("connection lost", func(t *testing.T) {
		c := New(Options{URL: silentHA(t, true), Token: "x", Logger: quietLog})
		c.minBackoff = time.Hour
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = c.Run(ctx) }()
		_, err := c.CallService(testCtx(t), "lock", "unlock", nil, nil, false)
		if !errors.Is(err, ErrDisconnected) || Refused(err) {
			t.Errorf("err = %v, Refused = %v, want a lost connection that is not a refusal", err, Refused(err))
		}
	})
	t.Run("canceled while waiting for a connection", func(t *testing.T) {
		c := New(Options{URL: "ws://127.0.0.1:1/api/websocket", Token: "x", Logger: quietLog})
		cctx, ccancel := context.WithCancel(context.Background())
		ccancel()
		_, err := c.CallService(cctx, "lock", "unlock", nil, nil, false)
		if !errors.Is(err, context.Canceled) || Refused(err) {
			t.Errorf("err = %v, Refused = %v", err, Refused(err))
		}
	})
}

// OnEventsLost fires when a session is up with its subscriptions in
// place, when it drops, and again after the reconnect.
func TestOnEventsLostOnReconnect(t *testing.T) {
	f := newFakeHA(t)
	c := New(Options{URL: f.url(), Token: testToken, Logger: quietLog})
	c.minBackoff = 10 * time.Millisecond
	var mu sync.Mutex
	calls := 0
	subsAtFirst := -1
	c.OnEventsLost(func() {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if subsAtFirst < 0 {
			f.mu.Lock()
			for _, m := range f.subs {
				subsAtFirst = len(m)
			}
			f.mu.Unlock()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.SubscribeEvents(ctx, "state_changed", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	go func() { _ = c.Run(ctx) }()
	n := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	waitFor(t, "first call", func() bool { return n() == 1 })
	mu.Lock()
	if subsAtFirst < 1 {
		t.Errorf("called before the subscriptions were in place: %d", subsAtFirst)
	}
	mu.Unlock()
	f.dropAll()
	waitFor(t, "drop and reconnect", func() bool { return n() >= 3 })
}

// A subscriber whose queue is full drops the event and reports it.
func TestOnEventsLostOnQueueOverflow(t *testing.T) {
	c := New(Options{URL: "ws://127.0.0.1:1/api/websocket", Token: "x", Logger: quietLog})
	lost := 0
	c.OnEventsLost(func() { lost++ })
	sub := &subscription{eventType: "state_changed", ctx: context.Background(), queue: make(chan Event, 1), log: quietLog, dropped: c.eventsLost}
	sub.deliver(Event{})
	if lost != 0 {
		t.Fatalf("lost after a queued event: %d", lost)
	}
	sub.deliver(Event{})
	if lost != 1 {
		t.Errorf("lost = %d after a dropped event", lost)
	}
}
