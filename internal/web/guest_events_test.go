package web

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// sseEvent is one server-sent event.
type sseEvent struct{ name, data string }

// openStream opens /events of l on a real listener and returns the
// events as they arrive. The channel closes when the stream ends.
func openStream(t *testing.T, h *harness, l model.Link, cookies ...*http.Cookie) (<-chan sseEvent, *http.Response) {
	t.Helper()
	srv := httptest.NewServer(h.guest)
	t.Cleanup(srv.Close)
	hr, err := http.NewRequest(http.MethodGet, srv.URL+"/g/"+l.Slug+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cookies {
		hr.AddCookie(c)
	}
	resp, err := srv.Client().Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	out := make(chan sseEvent, 100)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = strings.TrimPrefix(line, "data: ")
			case line == "" && ev.name != "":
				out <- ev
				ev = sseEvent{}
			}
		}
	}()
	return out, resp
}

func next(t *testing.T, ch <-chan sseEvent) (sseEvent, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(5 * time.Second):
		t.Fatal("no event in 5s")
	}
	return sseEvent{}, false
}

func TestGuestEventsFilter(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	ch, resp := openStream(t, h, l)
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" || resp.Header.Get("Content-Security-Policy") == "" ||
		resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers = %v", resp.Header)
	}
	ev, _ := next(t, ch)
	if ev.name != "state" || !strings.Contains(ev.data, "light.guest_room") || strings.Contains(ev.data, "secret") {
		t.Fatalf("first = %+v", ev)
	}
	if ev, _ := next(t, ch); ev.name != "ready" {
		t.Fatalf("second = %+v", ev)
	}
	if n := h.srv.streams.count(l.ID); n != 1 {
		t.Errorf("open streams = %d", n)
	}
	// Ungranted entities and unlisted attributes never show up.
	for i := 0; ; i++ {
		h.fake.EmitData("state_changed", map[string]any{"entity_id": "light.bedroom",
			"new_state": map[string]any{"entity_id": "light.bedroom", "state": "off", "attributes": map[string]any{}}})
		h.fake.EmitData("state_changed", map[string]any{"entity_id": "light.guest_room",
			"new_state": map[string]any{"entity_id": "light.guest_room", "state": "on",
				"attributes": map[string]any{"brightness": 200, "user_id": "secret-user"}}})
		ev, _ := next(t, ch)
		if strings.Contains(ev.data, "bedroom") || strings.Contains(ev.data, "secret") {
			t.Fatalf("leak: %+v", ev)
		}
		if ev.name == "state" && strings.Contains(ev.data, `"state":"on"`) && strings.Contains(ev.data, `"brightness":200`) {
			break
		}
		if i > 10 {
			t.Fatal("granted change never arrived")
		}
	}
	// A revoke ends the stream within a check.
	if err := h.links.Revoke(l.ID); err != nil {
		t.Fatal(err)
	}
	for {
		ev, ok := next(t, ch)
		if !ok {
			t.Fatal("stream closed without end")
		}
		if ev.name == "end" {
			break
		}
	}
	waitUntil(t, func() bool { return h.srv.streams.count(l.ID) == 0 })
}

// A template edit that changes what the link offers makes the page reload.
func TestGuestEventsReloadOnTemplateChange(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	ch, _ := openStream(t, h, l)
	for ev, _ := next(t, ch); ev.name != "ready"; ev, _ = next(t, ch) {
	}
	ts := testTemplates()
	ts[0].Rules[0].Actions = []string{"light.turn_on"}
	if err := h.tpl.Save(model.Templates{Version: 1, Templates: ts}); err != nil {
		t.Fatal(err)
	}
	if ev, _ := next(t, ch); ev.name != "reload" {
		t.Errorf("after template change: %+v", ev)
	}
}

func TestGuestEventsEndAtExpiry(t *testing.T) {
	h := newHarness(t)
	l, _ := h.links.Create(model.Link{Name: "Short", ValidUntil: t0.Add(50 * time.Millisecond), TemplateIDs: []string{"guest_room"}}, "")
	ch, _ := openStream(t, h, l)
	for {
		ev, ok := next(t, ch)
		if !ok {
			t.Fatal("closed without end")
		}
		if ev.name == "end" {
			return
		}
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
