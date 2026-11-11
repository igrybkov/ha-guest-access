package hatest

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"sync"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/ha"
)

// Sim makes a Fake act like HA for the services the app calls. A call
// changes the target entity's state in the Fake and sends a
// state_changed event, so pages that watch the state update live.
//
// Set Fake.Respond to Sim.Respond before the Fake is in use. Sim only
// covers the services in the app's catalog. Other services change
// nothing and succeed.
type Sim struct {
	fake *Fake
	emit func(eventType string, data any)
	now  func() time.Time
	// scriptRun is how long a script stays on after script.turn_on. 0
	// means it stays on until script.turn_off.
	scriptRun time.Duration

	mu     sync.Mutex
	timers map[string]*time.Timer
	log    []SimCall
	tracks map[string]int
}

// SimOptions configure a Sim. Zero values mean the defaults.
type SimOptions struct {
	// Emit sends one event. The default is Fake.EmitData, which reaches
	// in-process subscribers. With a Server, pass Server.Emit so the
	// event goes over the wire.
	Emit func(eventType string, data any)
	// Now is the clock for last_changed and the call log.
	Now func() time.Time
	// ScriptRun is how long a script stays on. 0 means until turned off.
	ScriptRun time.Duration
}

// SimCall is one service call as the Sim saw it.
type SimCall struct {
	At       time.Time      `json:"at"`
	Domain   string         `json:"domain"`
	Service  string         `json:"service"`
	EntityID string         `json:"entity_id"`
	Data     map[string]any `json:"data,omitempty"`
}

// Titles are the media titles that next and previous track step
// through.
var Titles = []string{"Morning light", "Slow river", "City at night", "Long way home"}

// NewSim returns a Sim that changes states in f.
func NewSim(f *Fake, o SimOptions) *Sim {
	s := &Sim{fake: f, emit: o.Emit, now: o.Now, scriptRun: o.ScriptRun,
		timers: map[string]*time.Timer{}, tracks: map[string]int{}}
	if s.emit == nil {
		s.emit = f.EmitData
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Respond applies call to each target entity. It fails like HA does for
// an entity that has no state. Use it as Fake.Respond.
func (s *Sim) Respond(call ServiceCall) (json.RawMessage, error) {
	if call.Target == nil {
		return nil, nil
	}
	for _, id := range call.Target.EntityID {
		s.record(call, id)
		old, ok := s.fake.State(id)
		if !ok {
			return nil, fmt.Errorf("entity %s not found", id)
		}
		next := s.apply(call, old)
		s.set(old, next)
		if call.Domain == "script" {
			s.scheduleScriptEnd(call.Service, id)
		}
	}
	return nil, nil
}

// Calls returns the calls seen so far, oldest first. It is never nil,
// so it encodes as a JSON list.
func (s *Sim) Calls() []SimCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SimCall{}, s.log...)
}

// Stop cancels pending script timers.
func (s *Sim) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.timers {
		t.Stop()
		delete(s.timers, id)
	}
}

func (s *Sim) record(call ServiceCall, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, SimCall{At: s.now(), Domain: call.Domain, Service: call.Service, EntityID: id, Data: maps.Clone(call.Data)})
}

// set stores next and sends state_changed. last_changed moves only when
// the state string changes, as in HA.
func (s *Sim) set(old, next ha.State) {
	if next.State != old.State || next.LastChanged.IsZero() {
		next.LastChanged = s.now()
	}
	s.fake.SetState(next)
	s.emit("state_changed", map[string]any{"entity_id": next.EntityID, "old_state": old, "new_state": next})
}

// apply returns the state after call. old is not changed.
func (s *Sim) apply(call ServiceCall, old ha.State) ha.State {
	st := old
	st.Attributes = maps.Clone(old.Attributes)
	if st.Attributes == nil {
		st.Attributes = map[string]any{}
	}
	a := st.Attributes
	switch call.Domain + "." + call.Service {
	case "light.turn_on":
		st.State = "on"
		if pct, ok := number(call.Data["brightness_pct"]); ok {
			a["brightness"] = math.Round(pct * 255 / 100)
		} else if _, ok := number(a["brightness"]); !ok {
			a["brightness"] = 255.0
		}
		if k, ok := number(call.Data["color_temp_kelvin"]); ok {
			a["color_temp_kelvin"] = k
		}
	case "light.turn_off":
		st.State = "off"
		a["brightness"] = nil
	case "light.toggle":
		next := "light.turn_on"
		if old.State == "on" {
			next = "light.turn_off"
		}
		return s.apply(ServiceCall{Domain: "light", Service: next[len("light."):]}, old)
	case "fan.turn_on":
		st.State = "on"
		if p, ok := number(a["percentage"]); !ok || p == 0 {
			a["percentage"] = 100.0
		}
	case "fan.turn_off":
		st.State, a["percentage"] = "off", 0.0
	case "fan.set_percentage":
		p, _ := number(call.Data["percentage"])
		a["percentage"] = p
		st.State = "off"
		if p > 0 {
			st.State = "on"
		}
	case "media_player.media_play_pause":
		st.State = "playing"
		if old.State == "playing" {
			st.State = "paused"
		}
	case "media_player.media_next_track", "media_player.media_previous_track":
		step := 1
		if call.Service == "media_previous_track" {
			step = len(Titles) - 1
		}
		s.mu.Lock()
		i := (s.tracks[old.EntityID] + step) % len(Titles)
		s.tracks[old.EntityID] = i
		s.mu.Unlock()
		a["media_title"] = Titles[i]
	case "media_player.volume_set":
		if v, ok := number(call.Data["volume_level"]); ok {
			a["volume_level"] = v
		}
	case "media_player.volume_mute":
		if m, ok := call.Data["is_volume_muted"].(bool); ok {
			a["is_volume_muted"] = m
		}
	case "lock.lock":
		st.State = "locked"
	case "lock.unlock":
		st.State = "unlocked"
	case "lock.open":
		st.State = "open"
	case "alarm_control_panel.alarm_disarm":
		st.State = "disarmed"
	case "alarm_control_panel.alarm_arm_home":
		st.State = "armed_home"
	case "alarm_control_panel.alarm_arm_away":
		st.State = "armed_away"
	case "alarm_control_panel.alarm_arm_night":
		st.State = "armed_night"
	case "switch.turn_on", "script.turn_on":
		st.State = "on"
	case "switch.turn_off", "script.turn_off":
		st.State = "off"
	}
	return st
}

// scheduleScriptEnd turns a running script off after scriptRun, as a
// real script does when it finishes. turn_off stops the timer.
func (s *Sim) scheduleScriptEnd(service, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.timers[id]; t != nil {
		t.Stop()
		delete(s.timers, id)
	}
	if service != "turn_on" || s.scriptRun <= 0 {
		return
	}
	s.timers[id] = time.AfterFunc(s.scriptRun, func() {
		s.mu.Lock()
		delete(s.timers, id)
		s.mu.Unlock()
		if old, ok := s.fake.State(id); ok && old.State == "on" {
			next := old
			next.State = "off"
			s.set(old, next)
		}
	})
}

// number reads a JSON number. Service data that came over the wire has
// float64; data built in Go may have int.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
