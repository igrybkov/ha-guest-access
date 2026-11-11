package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/demo"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// The demo house seeds through the admin API, and guest actions change
// the fake HA's states, which reach the guest's event stream.
func TestE2EDemoHouse(t *testing.T) {
	a := startApp(t, appOpts{fake: demo.House(), sim: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seeded, err := demo.Seed(ctx, testClient, a.admin, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(seeded.URL, "https://guest.example.com/g/") || seeded.LinkID == "" {
		t.Fatalf("seeded = %+v", seeded)
	}
	l := model.Link{Slug: strings.TrimPrefix(seeded.Path, "/g/")}

	st := a.state(l)
	if st.code != http.StatusOK {
		t.Fatalf("state: %d %s", st.code, st.body)
	}
	for _, leak := range []string{"light.bedroom", "light.living_room_accent", "light.garage", "sensor.speaker_battery"} {
		if st.has(leak) {
			t.Errorf("state shows %s", leak)
		}
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", a.guest+seeded.Path+"/events", nil)
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	states := make(chan model.State, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var s model.State
				if json.Unmarshal([]byte(data), &s) == nil && s.EntityID != "" {
					states <- s
				}
			}
		}
		close(states)
	}()
	// The stream subscribes before its first event.
	waitFor(t, "stream subscription", func() bool { return a.ha.Subscribers("state_changed") > 0 })

	expect := func(id, state string, check func(map[string]any) bool) {
		t.Helper()
		for {
			select {
			case s, ok := <-states:
				if !ok {
					t.Fatal("stream closed")
				}
				if s.EntityID == id && s.State == state && (check == nil || check(s.Attributes)) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("no %s %s on the stream", id, state)
			}
		}
	}

	if r := a.act(l, "light.kitchen", "light.turn_on", map[string]any{"brightness_pct": 40}); r.code != http.StatusOK {
		t.Fatalf("act: %d %s", r.code, r.body)
	}
	expect("light.kitchen", "on", func(at map[string]any) bool { return at["brightness"] == 102.0 })

	if r := a.act(l, "script.let_me_in", "script.run", nil); r.code != http.StatusOK {
		t.Fatalf("run: %d %s", r.code, r.body)
	}
	expect("script.let_me_in", "on", nil)
	if r := a.act(l, "script.let_me_in", "script.cancel", nil); r.code != http.StatusOK {
		t.Fatalf("cancel: %d %s", r.code, r.body)
	}
	expect("script.let_me_in", "off", nil)

	if r := a.act(l, "alarm_control_panel.home", "alarm_control_panel.alarm_arm_away", nil); r.code != http.StatusOK {
		t.Fatalf("arm: %d %s", r.code, r.body)
	}
	expect("alarm_control_panel.home", "armed_away", nil)
}
