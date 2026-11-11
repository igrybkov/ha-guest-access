package hatest

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/ha"
)

// A real WSClient against the Server sees the Fake's data and calls.
func TestServerOverTheWire(t *testing.T) {
	f := &Fake{
		LabelList: []ha.Label{{LabelID: "guest_music", Name: "Guest music"}},
		Core:      ha.Config{Latitude: 41.88, Longitude: -87.63},
	}
	srv := NewServer(f, "tok")
	defer srv.Close()
	c := ha.New(ha.Options{URL: srv.URL(), Token: "tok", Logger: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	labels, err := c.Labels(ctx)
	if err != nil || len(labels) != 1 || labels[0].LabelID != "guest_music" {
		t.Fatalf("Labels = %+v, %v", labels, err)
	}
	cfg, err := c.Config(ctx)
	if err != nil || cfg.Latitude != 41.88 {
		t.Fatalf("Config = %+v, %v", cfg, err)
	}
	if err := c.FireEvent(ctx, "guest_access_action", map[string]any{"link": "Anna"}); err != nil {
		t.Fatal(err)
	}
	if ev := f.FiredEvents(); len(ev) != 1 || ev[0].EventType != "guest_access_action" || ev[0].Data["link"] != "Anna" {
		t.Fatalf("fired %+v", ev)
	}
	if _, err := c.CallService(ctx, "lock", "lock", nil, &ha.Target{EntityID: []string{"lock.front_door"}}, false); err != nil {
		t.Fatal(err)
	}
	if calls := f.ServiceCalls(); len(calls) != 1 || calls[0].Target.EntityID[0] != "lock.front_door" {
		t.Fatalf("calls %+v", calls)
	}
}
