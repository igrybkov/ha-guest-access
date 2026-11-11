package web

import (
	"net/http"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// slider sends one brightness value for entity.
func (h *harness) slider(l model.Link, entity string, pct int) int {
	return h.act(l, entity, "light.turn_on", map[string]any{"brightness_pct": pct}).Code
}

// twoLights is a link over the guest room plus the bedroom light.
func (h *harness) twoLights() model.Link {
	h.t.Helper()
	l, err := h.links.Create(model.Link{
		Name: "Anna's visit", ValidFrom: t0.Add(-time.Hour), ValidUntil: t0.Add(24 * time.Hour), TemplateIDs: []string{"guest_room"},
		Extra: []model.Rule{{Select: model.Selector{Entities: []string{"light.bedroom"}}, Actions: []string{"light.turn_on"}}},
	}, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return l
}

// 200 slider values in a minute on one light pass. That is more than
// the discrete limit (burst 40 + 120 per minute) would allow.
func TestContinuousOneLightPasses(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	for i := range 200 {
		if c := h.slider(l, "light.guest_room", 1+i%100); c != 200 {
			t.Fatalf("value %d: %d", i, c)
		}
		h.advance(300 * time.Millisecond)
	}
}

// Two lights dragged at once each have their own bucket.
func TestContinuousTwoLightsPass(t *testing.T) {
	h := newHarness(t)
	l := h.twoLights()
	for i := range 200 {
		for _, e := range []string{"light.guest_room", "light.bedroom"} {
			if c := h.slider(l, e, 1+i%100); c != 200 {
				t.Fatalf("%s value %d: %d", e, i, c)
			}
		}
		h.advance(300 * time.Millisecond)
	}
}

// A flood past the continuous burst gets 429, and the discrete limit is
// not used up by it.
func TestContinuousFloodLimited(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	got429 := 0
	for i := range 130 {
		if c := h.slider(l, "light.guest_room", 1+i%100); c == http.StatusTooManyRequests {
			got429++
		}
	}
	if got429 != 10 {
		t.Errorf("%d of 130 limited, want 10 past the burst of 120", got429)
	}
	if c := h.act(l, "light.guest_room", "light.turn_off", nil).Code; c != 200 {
		t.Errorf("discrete action after the flood: %d", c)
	}
}

// Discrete actions keep their own limit, and do not use the continuous
// one.
func TestDiscreteKeepsOwnLimit(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	got429 := 0
	for range 45 {
		if c := h.act(l, "light.guest_room", "light.turn_off", nil).Code; c == http.StatusTooManyRequests {
			got429++
		}
	}
	if got429 != 5 {
		t.Errorf("%d of 45 limited, want 5 past the burst of 40", got429)
	}
	if c := h.slider(l, "light.guest_room", 50); c != 200 {
		t.Errorf("slider after discrete limit: %d", c)
	}
}
