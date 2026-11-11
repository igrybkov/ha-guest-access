package web

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// The admin settings carry HA's time zone, so the admin page can show and
// read times in it.
func TestSettingsTimeZone(t *testing.T) {
	h := newHarness(t)
	if v := decode[settingsView](t, h.a(req{method: "GET", path: "/api/settings"})); v.TimeZone != "America/Chicago" {
		t.Errorf("time_zone = %q", v.TimeZone)
	}

	// A name this machine does not know is sent as "".
	h = newHarness(t)
	h.fake.Core.TimeZone = "Mars/Olympus_Mons"
	rec := h.a(req{method: "GET", path: "/api/settings"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"time_zone":""`) {
		t.Errorf("invalid zone: %d %s", rec.Code, rec.Body)
	}
}

// Settings still load when HA is away. The zone is then "".
func TestSettingsTimeZoneHAAway(t *testing.T) {
	h := newHarness(t, func(_ *config.Options, d *Deps) { d.Inventory = awayInventory{inv: d.Inventory} })
	rec := h.a(req{method: "GET", path: "/api/settings"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"time_zone":""`) {
		t.Errorf("HA away: %d %s", rec.Code, rec.Body)
	}
}

// awayInventory fails every snapshot, as when HA is not reachable.
type awayInventory struct{ inv Inventory }

func (a awayInventory) States(ctx context.Context, ids map[string]bool) (map[string]model.State, error) {
	return a.inv.States(ctx, ids)
}

func (a awayInventory) Watch(ctx context.Context, ids map[string]bool, fn func(model.State)) error {
	return a.inv.Watch(ctx, ids, fn)
}

func (awayInventory) Inventory(context.Context) (model.Inventory, error) {
	return model.Inventory{}, ha.ErrDisconnected
}

// offsetStamp is a timestamp with a UTC offset instead of "Z".
var offsetStamp = regexp.MustCompile(`\d{2}:\d{2}(:\d{2}(\.\d+)?)?[+-]\d{2}:?\d{2}`)

// HA's time zone hints at where the home is, so no guest response may
// carry it: no zone name and no UTC offset. Here HA, the clock, the
// stored link times and the entity states all use Pacific/Chatham
// (+13:45), and every guest route is called.
func TestGuestNeverSeesHATimeZone(t *testing.T) {
	const zone = "Pacific/Chatham"
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, withMA(stubMA{url: "https://ma.example/party?code=x"}))
	h.fake.Core.TimeZone = zone
	for i := range h.fake.StateList {
		h.fake.StateList[i].LastChanged = t0.Add(-time.Hour).In(loc)
	}
	h.now = t0.In(loc)
	l, err := h.links.Create(model.Link{
		Name: "Anna's visit", ValidFrom: t0.Add(-time.Hour).In(loc), ValidUntil: t0.Add(24 * time.Hour).In(loc),
		TemplateIDs: []string{"guest_room", "entry", "music"}, Approval: model.Approval{SelfService: true},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	pwLink := h.link("guest_room")
	hash, _ := access.HashPassword("+1 555 0100")
	if _, err := h.links.AddPassword(pwLink.ID, "Anna", hash); err != nil {
		t.Fatal(err)
	}

	check := func(route string, code int, header http.Header, body string) {
		t.Helper()
		var all strings.Builder
		all.WriteString(body)
		for k, vs := range header {
			all.WriteString("\n" + k + ": " + strings.Join(vs, ", "))
		}
		s := all.String()
		for _, bad := range []string{zone, "Chatham", "+13:45", "+1345", "CHAST", "CHADT"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s: response holds %q:\n%s", route, bad, s)
			}
		}
		if m := offsetStamp.FindString(s); m != "" {
			t.Errorf("%s: timestamp with an offset %q:\n%s", route, m, s)
		}
		if code >= 500 {
			t.Errorf("%s: status %d %s", route, code, body)
		}
	}

	// The first device of a self-service link is approved at once. The
	// second waits, so the first can list and decide it.
	owner, other := jar{}, jar{}
	for _, r := range []struct {
		j            jar
		method, path string
		body         any
	}{
		{owner, "GET", "", nil},
		{owner, "POST", "device/request", map[string]any{}},
		{owner, "GET", "device/status", nil},
		{other, "POST", "device/request", map[string]any{"label": "Bob"}},
		{other, "GET", "device/status", nil},
		{owner, "GET", "device/pending", nil},
		{owner, "GET", "state", nil},
		{owner, "POST", "act", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}},
		{owner, "POST", "where", map[string]any{"lat": home.Lat, "lon": home.Lon, "accuracy": 10.0}},
		{owner, "GET", "party", nil},
	} {
		rec := h.dev(r.j, l, r.method, r.path, r.body)
		check(r.method+" "+r.path, rec.Code, rec.Header(), rec.Body.String())
		if r.path == "device/pending" && !strings.Contains(rec.Body.String(), `"created_at"`) {
			t.Errorf("pending holds no request: %s", rec.Body)
		}
		if r.path == "state" && (!strings.Contains(rec.Body.String(), `"valid_until":"2026-11-12T12:00:00Z"`) ||
			!strings.Contains(rec.Body.String(), `"last_changed":"2026-11-11T11:00:00Z"`) ||
			strings.Contains(rec.Body.String(), "time_zone")) {
			t.Errorf("state: %s", rec.Body)
		}
		if r.path == "where" && !strings.Contains(rec.Body.String(), `"expires_at":"`) {
			t.Errorf("where: %s", rec.Body)
		}
	}

	// device/decide on the waiting request.
	pending := decode[struct {
		Requests []struct {
			ID string `json:"request_id"`
		} `json:"requests"`
	}](t, h.dev(owner, l, "GET", "device/pending", nil))
	if len(pending.Requests) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	rec := h.dev(owner, l, "POST", "device/decide", map[string]any{"request_id": pending.Requests[0].ID, "approve": true})
	check("POST device/decide", rec.Code, rec.Header(), rec.Body.String())

	// password, on a link that has one.
	rec = h.g(req{method: "POST", path: "/g/" + pwLink.Slug + "/password", body: map[string]any{"password": "+1 555 0100"}})
	check("POST password", rec.Code, rec.Header(), rec.Body.String())
	rec = h.g(req{method: "POST", path: "/g/" + pwLink.Slug + "/password", body: map[string]any{"password": "wrong"}})
	check("POST password (wrong)", rec.Code, rec.Header(), rec.Body.String())

	// events: the first states, then a change HA sends with a +13:45
	// offset.
	ch, resp := openStream(t, h, l, owner.list()...)
	check("GET events", resp.StatusCode, resp.Header, "")
	for {
		ev, ok := next(t, ch)
		if !ok {
			t.Fatal("stream ended")
		}
		check("event "+ev.name, 200, nil, ev.data)
		if ev.name == "ready" {
			break
		}
	}
	h.fake.EmitData("state_changed", map[string]any{"entity_id": "light.guest_room",
		"new_state": map[string]any{"entity_id": "light.guest_room", "state": "on",
			"last_changed": "2026-11-12T03:00:00+13:45", "attributes": map[string]any{}}})
	for {
		ev, ok := next(t, ch)
		if !ok {
			t.Fatal("stream ended")
		}
		check("event "+ev.name, 200, nil, ev.data)
		if ev.name == "state" && strings.Contains(ev.data, `"state":"on"`) {
			if !strings.Contains(ev.data, `"last_changed":"2026-11-11T13:15:00Z"`) {
				t.Errorf("changed state: %s", ev.data)
			}
			break
		}
	}
}
