package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

const (
	// streamBuffer holds state changes waiting to be written. When it
	// overflows, the stream ends with "reload" and the page reconnects.
	streamBuffer = 64
	// heartbeat keeps proxies from closing an idle stream.
	heartbeat = 25 * time.Second
	// streamRetry is the Retry-After for a link at its stream cap.
	streamRetry = 30 * time.Second
)

// guestEvents streams state changes of the entities the guest may see
// now, as server-sent events:
//
//   - "state": one filtered model.State. The current states come first.
//   - "ready": the current states are sent.
//   - "reload": what the link offers changed (templates, access, a
//     one-time use, an expired location). The page reloads /state. The
//     stream ends.
//   - "end": the link ended, was revoked, or its slug or password went
//     away. The stream ends.
//
// Every few seconds the stream checks its link again, so a revoke, a
// rotated slug or a removed password ends it quickly.
func (s *Server) guestEvents(g *guestReq) {
	ctx, cancel := context.WithCancel(g.r.Context())
	defer cancel()
	p, err := s.plan(ctx, g)
	if err != nil {
		s.log.Warn("guest events: inventory", "err", err)
		writeError(g.w, http.StatusServiceUnavailable, msgHAFailed)
		return
	}
	if !s.streams.acquire(g.link.ID, g.lim.streams) {
		tooMany(g.w, streamRetry)
		return
	}
	defer s.streams.release(g.link.ID)

	h := g.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no") // nginx (NPM) buffers otherwise
	g.w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(g.w)
	send := func(event string, v any) bool {
		raw, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(g.w, "event: %s\ndata: %s\n\n", event, raw); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	allowed := p.passing
	fp := fingerprint(p.view)
	changes := make(chan model.State, streamBuffer)
	var overflow atomic.Bool
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- s.deps.Inventory.Watch(ctx, allowed, func(st model.State) {
			select {
			case changes <- st:
			default:
				overflow.Store(true)
			}
		})
	}()

	if len(allowed) > 0 {
		states, err := s.deps.Inventory.States(ctx, allowed)
		if err != nil {
			s.log.Debug("guest events: states", "err", err)
		}
		for _, id := range sortedKeys(states) {
			if !send("state", catalog.FilterState(states[id])) {
				return
			}
		}
	}
	if !send("ready", struct{}{}) {
		return
	}

	check := time.NewTicker(s.deps.StreamCheck)
	defer check.Stop()
	beat := time.NewTicker(heartbeat)
	defer beat.Stop()
	expiry := time.NewTimer(max(0, g.link.ValidUntil.Sub(s.now())))
	defer expiry.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-watchDone:
			send("end", struct{}{})
			return
		case st := <-changes:
			if overflow.Load() {
				send("reload", struct{}{})
				return
			}
			if allowed[st.EntityID] && !send("state", catalog.FilterState(st)) {
				return
			}
		case <-beat.C:
			if _, err := fmt.Fprint(g.w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-expiry.C:
			send("end", struct{}{})
			return
		case <-check.C:
			switch s.recheck(ctx, g, fp) {
			case streamEnd:
				send("end", struct{}{})
				return
			case streamReload:
				send("reload", struct{}{})
				return
			}
		}
	}
}

type streamVerdict int

const (
	streamKeep streamVerdict = iota
	streamReload
	streamEnd
)

// recheck loads the link again and works out the plan at the current
// time. The link must still be active under the same slug, and the
// password session must still name a password of the link.
//
// The device gate is not run again here: it writes a response, which a
// stream can't take. The device approval work should add a check that
// the device still exists.
func (s *Server) recheck(ctx context.Context, g *guestReq, fp string) streamVerdict {
	l, err := s.deps.Links.Get(g.link.ID)
	g.now = s.now()
	if err != nil || l.Slug != g.slug || !l.Active(g.now) {
		return streamEnd
	}
	g.link = l
	if !s.session(g) {
		return streamEnd
	}
	p, err := s.plan(ctx, g)
	if err != nil {
		return streamKeep // HA is away; keep the stream and try again
	}
	if fingerprint(p.view) != fp {
		return streamReload
	}
	return streamKeep
}

// fingerprint sums up what a view offers, states aside.
func fingerprint(v stateView) string {
	v.States = nil
	raw, _ := json.Marshal(v)
	return string(raw)
}
