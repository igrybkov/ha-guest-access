package hatest

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/ha"
)

// simHouse is a Fake with one entity of each kind the Sim knows.
func simHouse() *Fake {
	return &Fake{StateList: []ha.State{
		{EntityID: "light.lamp", State: "off", Attributes: map[string]any{"friendly_name": "Lamp"}},
		{EntityID: "fan.ceiling", State: "off", Attributes: map[string]any{"percentage": 0.0}},
		{EntityID: "media_player.speaker", State: "idle", Attributes: map[string]any{"volume_level": 0.2}},
		{EntityID: "lock.front", State: "locked"},
		{EntityID: "alarm_control_panel.home", State: "disarmed"},
		{EntityID: "switch.kettle", State: "off"},
		{EntityID: "script.let_me_in", State: "off"},
	}}
}

func call(domain, service, id string, data map[string]any) ServiceCall {
	return ServiceCall{Domain: domain, Service: service, Data: data, Target: &ha.Target{EntityID: []string{id}}}
}

func TestSimAppliesServices(t *testing.T) {
	tests := []struct {
		name  string
		calls []ServiceCall
		id    string
		state string
		attrs map[string]any // checked keys only
	}{
		{"light on, full by default", []ServiceCall{call("light", "turn_on", "light.lamp", nil)},
			"light.lamp", "on", map[string]any{"brightness": 255.0}},
		{"light brightness from pct", []ServiceCall{call("light", "turn_on", "light.lamp", map[string]any{"brightness_pct": 50.0})},
			"light.lamp", "on", map[string]any{"brightness": 128.0}},
		{"light color temp", []ServiceCall{call("light", "turn_on", "light.lamp", map[string]any{"color_temp_kelvin": 3000})},
			"light.lamp", "on", map[string]any{"color_temp_kelvin": 3000.0}},
		{"light off clears brightness", []ServiceCall{call("light", "turn_on", "light.lamp", nil), call("light", "turn_off", "light.lamp", nil)},
			"light.lamp", "off", map[string]any{"brightness": nil}},
		{"light toggle twice", []ServiceCall{call("light", "toggle", "light.lamp", nil), call("light", "toggle", "light.lamp", nil)},
			"light.lamp", "off", nil},
		{"fan on", []ServiceCall{call("fan", "turn_on", "fan.ceiling", nil)}, "fan.ceiling", "on", map[string]any{"percentage": 100.0}},
		{"fan percentage", []ServiceCall{call("fan", "set_percentage", "fan.ceiling", map[string]any{"percentage": 40.0})},
			"fan.ceiling", "on", map[string]any{"percentage": 40.0}},
		{"fan percentage 0 is off", []ServiceCall{call("fan", "set_percentage", "fan.ceiling", map[string]any{"percentage": 0.0})},
			"fan.ceiling", "off", nil},
		{"media play", []ServiceCall{call("media_player", "media_play_pause", "media_player.speaker", nil)}, "media_player.speaker", "playing", nil},
		{"media pause", []ServiceCall{call("media_player", "media_play_pause", "media_player.speaker", nil), call("media_player", "media_play_pause", "media_player.speaker", nil)},
			"media_player.speaker", "paused", nil},
		{"media volume", []ServiceCall{call("media_player", "volume_set", "media_player.speaker", map[string]any{"volume_level": 0.6})},
			"media_player.speaker", "idle", map[string]any{"volume_level": 0.6}},
		{"media mute", []ServiceCall{call("media_player", "volume_mute", "media_player.speaker", map[string]any{"is_volume_muted": true})},
			"media_player.speaker", "idle", map[string]any{"is_volume_muted": true}},
		{"media next", []ServiceCall{call("media_player", "media_next_track", "media_player.speaker", nil)},
			"media_player.speaker", "idle", map[string]any{"media_title": Titles[1]}},
		{"media previous wraps", []ServiceCall{call("media_player", "media_previous_track", "media_player.speaker", nil)},
			"media_player.speaker", "idle", map[string]any{"media_title": Titles[len(Titles)-1]}},
		{"unlock", []ServiceCall{call("lock", "unlock", "lock.front", nil)}, "lock.front", "unlocked", nil},
		{"open", []ServiceCall{call("lock", "open", "lock.front", nil)}, "lock.front", "open", nil},
		{"arm away", []ServiceCall{call("alarm_control_panel", "alarm_arm_away", "alarm_control_panel.home", nil)},
			"alarm_control_panel.home", "armed_away", nil},
		{"disarm", []ServiceCall{call("alarm_control_panel", "alarm_arm_home", "alarm_control_panel.home", nil), call("alarm_control_panel", "alarm_disarm", "alarm_control_panel.home", nil)},
			"alarm_control_panel.home", "disarmed", nil},
		{"switch", []ServiceCall{call("switch", "turn_on", "switch.kettle", nil)}, "switch.kettle", "on", nil},
		{"script stays on without a run time", []ServiceCall{call("script", "turn_on", "script.let_me_in", nil)}, "script.let_me_in", "on", nil},
		{"script cancel", []ServiceCall{call("script", "turn_on", "script.let_me_in", nil), call("script", "turn_off", "script.let_me_in", nil)},
			"script.let_me_in", "off", nil},
		{"unknown service changes nothing", []ServiceCall{call("lock", "explode", "lock.front", nil)}, "lock.front", "locked", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := simHouse()
			s := NewSim(f, SimOptions{})
			for _, c := range tt.calls {
				if _, err := s.Respond(c); err != nil {
					t.Fatal(err)
				}
			}
			st, _ := f.State(tt.id)
			if st.State != tt.state {
				t.Errorf("state = %q, want %q", st.State, tt.state)
			}
			for k, want := range tt.attrs {
				if got := st.Attributes[k]; got != want {
					t.Errorf("%s = %v (%T), want %v", k, got, got, want)
				}
			}
		})
	}
}

func TestSimSendsStateChanged(t *testing.T) {
	f := simHouse()
	t0 := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s := NewSim(f, SimOptions{Now: func() time.Time { return t0 }})
	var got []ha.Event
	_ = f.SubscribeEvents(context.Background(), "state_changed", func(ev ha.Event) { got = append(got, ev) })

	if _, err := s.Respond(call("lock", "unlock", "lock.front", nil)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events", len(got))
	}
	var data struct {
		EntityID string   `json:"entity_id"`
		Old      ha.State `json:"old_state"`
		New      ha.State `json:"new_state"`
	}
	if err := json.Unmarshal(got[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.EntityID != "lock.front" || data.Old.State != "locked" || data.New.State != "unlocked" || !data.New.LastChanged.Equal(t0) {
		t.Errorf("event = %+v", data)
	}
	if calls := s.Calls(); len(calls) != 1 || calls[0].EntityID != "lock.front" || calls[0].Service != "unlock" || !calls[0].At.Equal(t0) {
		t.Errorf("calls = %+v", calls)
	}
}

func TestSimUnknownEntityFails(t *testing.T) {
	f := simHouse()
	s := NewSim(f, SimOptions{})
	if _, err := s.Respond(call("light", "turn_on", "light.missing", nil)); err == nil {
		t.Fatal("want an error for an entity with no state")
	}
	if _, ok := f.State("light.missing"); ok {
		t.Error("a failed call made a state")
	}
}

func TestSimScriptEndsByItself(t *testing.T) {
	f := simHouse()
	s := NewSim(f, SimOptions{ScriptRun: 20 * time.Millisecond})
	defer s.Stop()
	if _, err := s.Respond(call("script", "turn_on", "script.let_me_in", nil)); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.State("script.let_me_in"); st.State != "on" {
		t.Fatalf("state = %q right after the call", st.State)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if st, _ := f.State("script.let_me_in"); st.State == "off" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("script still on")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Calls from many goroutines and get_states over the wire must not race.
// Run with -race.
func TestSimOverTheWireConcurrent(t *testing.T) {
	f := simHouse()
	srv := NewServer(f, "tok")
	defer srv.Close()
	s := NewSim(f, SimOptions{Emit: func(typ string, data any) { srv.Emit(typ, data) }})
	f.Respond = s.Respond

	c := ha.New(ha.Options{URL: srv.URL(), Token: "tok", Logger: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	changed := make(chan string, 100)
	if err := c.SubscribeEvents(ctx, "state_changed", func(ev ha.Event) {
		var d struct {
			EntityID string `json:"entity_id"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		changed <- d.EntityID
	}); err != nil {
		t.Fatal(err)
	}
	for srv.Subscribers("state_changed") == 0 {
		if ctx.Err() != nil {
			t.Fatal("no subscription")
		}
		time.Sleep(5 * time.Millisecond)
	}

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			pct := float64(i*10 + 1)
			if _, err := c.CallService(ctx, "light", "turn_on", map[string]any{"brightness_pct": pct}, &ha.Target{EntityID: []string{"light.lamp"}}, false); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if _, err := c.States(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for range 10 {
		select {
		case id := <-changed:
			if id != "light.lamp" {
				t.Errorf("event for %s", id)
			}
		case <-ctx.Done():
			t.Fatal("missing state_changed events")
		}
	}
	states, err := c.States(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.EntityID == "light.lamp" && st.State != "on" {
			t.Errorf("lamp = %q over the wire", st.State)
		}
	}
}

func TestFakeSetStateAddsAndReplaces(t *testing.T) {
	f := &Fake{}
	f.SetState(ha.State{EntityID: "light.a", State: "on"})
	f.SetState(ha.State{EntityID: "light.a", State: "off"})
	f.SetState(ha.State{EntityID: "light.b", State: "on"})
	got, _ := f.States(context.Background())
	if len(got) != 2 || got[0].State != "off" || got[1].EntityID != "light.b" {
		t.Errorf("states = %+v", got)
	}
	got[0].State = "changed"
	if st, _ := f.State("light.a"); st.State != "off" {
		t.Error("States did not return a copy")
	}
}
