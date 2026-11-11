// Package demo holds a small made-up house for the demo server: a fake
// HA registry and states, two templates and one link. Seed puts the
// templates and the link in a running app through its admin API, so the
// demo uses the same code paths as a real setup.
//
// Nothing here talks to a real Home Assistant.
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// Home is the house location: a public city center, not a real home.
var Home = ha.Config{Latitude: 41.8781, Longitude: -87.6298, LocationName: "Demo house", TimeZone: "America/Chicago"}

// House returns a fake HA with a small house:
//   - areas: living room, kitchen, guest room, bedroom, entry;
//   - labels: guest_ok, private (the bedroom light has private);
//   - lights: living room (with color temp), kitchen, guest room lamp,
//     bedroom; a hidden accent light and a disabled garage light;
//   - a ceiling fan, a speaker, a front door lock, an alarm panel that
//     needs no code, and two scripts: "Let me in" and "I'm leaving";
//   - covers: a living room blind with position and tilt, and a garage
//     door that only opens, closes and stops.
func House() *hatest.Fake {
	return &hatest.Fake{
		AreaList: []ha.Area{
			{AreaID: "living_room", Name: "Living room", Labels: []string{"guest_ok"}},
			{AreaID: "kitchen", Name: "Kitchen", Labels: []string{"guest_ok"}},
			{AreaID: "guest_room", Name: "Guest room"},
			{AreaID: "bedroom", Name: "Bedroom"},
			{AreaID: "entry", Name: "Entry"},
		},
		LabelList: []ha.Label{
			{LabelID: "guest_ok", Name: "Guest OK"},
			{LabelID: "private", Name: "Private"},
		},
		DeviceList: []ha.Device{
			{ID: "dev_guest_lamp", Name: "Guest lamp", AreaID: "guest_room"},
			{ID: "dev_ceiling_fan", Name: "Ceiling fan", NameByUser: "Guest room fan", AreaID: "guest_room", Labels: []string{"guest_ok"}},
			{ID: "dev_speaker", Name: "Speaker One", NameByUser: "Living room speaker", AreaID: "living_room"},
			{ID: "dev_front_lock", Name: "Front door lock", AreaID: "entry"},
			{ID: "dev_alarm", Name: "Alarm panel", AreaID: "entry"},
		},
		EntityList: []ha.Entity{
			{EntityID: "light.living_room", AreaID: "living_room", Name: "Living room light"},
			{EntityID: "light.living_room_accent", AreaID: "living_room", Name: "Accent strip", HiddenBy: "user"},
			{EntityID: "light.kitchen", AreaID: "kitchen", Name: "Kitchen light"},
			{EntityID: "light.guest_room", DeviceID: "dev_guest_lamp", OriginalName: "Guest lamp"},
			{EntityID: "light.bedroom", AreaID: "bedroom", Name: "Bedroom light", Labels: []string{"private"}},
			{EntityID: "light.garage", AreaID: "entry", Name: "Garage light", DisabledBy: "user"},
			{EntityID: "fan.guest_room", DeviceID: "dev_ceiling_fan", Name: "Guest room fan"},
			{EntityID: "media_player.living_room", DeviceID: "dev_speaker", Name: "Living room speaker"},
			{EntityID: "sensor.speaker_battery", DeviceID: "dev_speaker", Name: "Speaker battery", EntityCategory: "diagnostic"},
			{EntityID: "lock.front_door", DeviceID: "dev_front_lock", Name: "Front door"},
			{EntityID: "alarm_control_panel.home", DeviceID: "dev_alarm", Name: "Home alarm"},
			{EntityID: "script.let_me_in", Name: "Let me in"},
			{EntityID: "script.im_leaving", Name: "I'm leaving"},
			{EntityID: "cover.living_room_blind", AreaID: "living_room", Name: "Living room blind"},
			{EntityID: "cover.garage_door", AreaID: "entry", Name: "Garage door"},
		},
		StateList: []ha.State{
			{EntityID: "light.living_room", State: "on", Attributes: map[string]any{
				"friendly_name": "Living room light", "brightness": 180.0, "color_mode": "color_temp",
				"supported_color_modes": []any{"color_temp"}, "color_temp_kelvin": 3000.0,
				"min_color_temp_kelvin": 2200.0, "max_color_temp_kelvin": 6500.0,
			}},
			{EntityID: "light.living_room_accent", State: "off", Attributes: map[string]any{"friendly_name": "Accent strip"}},
			{EntityID: "light.kitchen", State: "off", Attributes: map[string]any{
				"friendly_name": "Kitchen light", "supported_color_modes": []any{"brightness"}}},
			{EntityID: "light.guest_room", State: "off", Attributes: map[string]any{
				"friendly_name": "Guest lamp", "supported_color_modes": []any{"brightness"}}},
			{EntityID: "light.bedroom", State: "on", Attributes: map[string]any{"friendly_name": "Bedroom light", "brightness": 90.0}},
			{EntityID: "fan.guest_room", State: "off", Attributes: map[string]any{"friendly_name": "Guest room fan", "percentage": 0.0}},
			{EntityID: "media_player.living_room", State: "paused", Attributes: map[string]any{
				"friendly_name": "Living room speaker", "volume_level": 0.3, "is_volume_muted": false,
				"media_title": hatest.Titles[0], "media_artist": "Demo band",
			}},
			{EntityID: "sensor.speaker_battery", State: "87", Attributes: map[string]any{"unit_of_measurement": "%"}},
			{EntityID: "lock.front_door", State: "locked", Attributes: map[string]any{"friendly_name": "Front door"}},
			{EntityID: "alarm_control_panel.home", State: "disarmed", Attributes: map[string]any{
				"friendly_name": "Home alarm", "code_format": nil, "code_arm_required": false}},
			{EntityID: "script.let_me_in", State: "off", Attributes: map[string]any{"friendly_name": "Let me in"}},
			{EntityID: "script.im_leaving", State: "off", Attributes: map[string]any{"friendly_name": "I'm leaving"}},
			// 255 is open, close, stop and set position, for the blind and
			// for its tilt.
			{EntityID: "cover.living_room_blind", State: "open", Attributes: map[string]any{
				"friendly_name": "Living room blind", "device_class": "blind", "supported_features": 255.0,
				"current_position": 60.0, "current_tilt_position": 30.0,
			}},
			// 11 is open, close and stop.
			{EntityID: "cover.garage_door", State: "closed", Attributes: map[string]any{
				"friendly_name": "Garage door", "device_class": "garage", "supported_features": 11.0,
			}},
		},
		Core: Home,
	}
}

// Templates are the two seeded templates. "Guest stay" picks the shared
// rooms by label and the guest room by area, and leaves out anything
// labeled private. "Front door" has the lock, the alarm, the garage door
// and the scripts.
func Templates() []model.Template {
	return []model.Template{
		{ID: "guest_stay", Name: "Guest stay", Icon: "mdi:sofa", Rules: []model.Rule{
			{
				Select:  model.Selector{Areas: []string{"guest_room"}, Labels: []string{"guest_ok"}, Entities: []string{"media_player.living_*"}},
				Exclude: model.Selector{Labels: []string{"private"}},
				Actions: []string{
					"light.turn_on", "light.turn_off",
					"fan.turn_on", "fan.turn_off", "fan.set_percentage",
					"media_player.media_play_pause", "media_player.media_next_track",
					"media_player.media_previous_track", "media_player.volume_set", "media_player.volume_mute",
					"cover.open_cover", "cover.close_cover", "cover.stop_cover", "cover.set_cover_position",
					"cover.open_cover_tilt", "cover.close_cover_tilt", "cover.stop_cover_tilt", "cover.set_cover_tilt_position",
				},
			},
		}},
		{ID: "front_door", Name: "Front door", Icon: "mdi:door", Rules: []model.Rule{
			{Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.lock", "lock.unlock"}},
			{Select: model.Selector{Entities: []string{"alarm_control_panel.home"}},
				Actions: []string{"alarm_control_panel.alarm_disarm", "alarm_control_panel.alarm_arm_away"}},
			{Select: model.Selector{Entities: []string{"cover.garage_door"}},
				Actions: []string{"cover.open_cover", "cover.close_cover", "cover.stop_cover"}},
			{Select: model.Selector{Entities: []string{"script.let_me_in", "script.im_leaving"}},
				Actions:   []string{"script.run", "script.cancel"},
				Variables: map[string]any{"source": "demo"}},
		}},
	}
}

// Link is the seeded link: both templates, for a week from now.
func Link(now time.Time) model.Link {
	return model.Link{
		Name:        "Demo guest",
		Note:        "Made by the demo server.",
		ValidUntil:  now.Add(7 * 24 * time.Hour).Truncate(time.Minute),
		TemplateIDs: []string{"guest_stay", "front_door"},
	}
}

// Seeded is what Seed made.
type Seeded struct {
	LinkID string
	// Path is the guest path, /g/<slug>. URL is the full guest URL, or ""
	// when the app has no public base URL.
	Path, URL string
}

// Seed creates the templates and the link through the admin API at
// adminURL (such as http://127.0.0.1:8099). The admin port must accept
// this peer, as it does in dev mode.
func Seed(ctx context.Context, c *http.Client, adminURL string, now time.Time) (Seeded, error) {
	base := strings.TrimRight(adminURL, "/")
	for _, t := range Templates() {
		if err := postJSON(ctx, c, base+"/api/templates", t, nil); err != nil {
			return Seeded{}, fmt.Errorf("template %s: %w", t.ID, err)
		}
	}
	var v struct {
		ID   string `json:"id"`
		Path string `json:"path"`
		URL  string `json:"url"`
	}
	if err := postJSON(ctx, c, base+"/api/links", Link(now), &v); err != nil {
		return Seeded{}, fmt.Errorf("link: %w", err)
	}
	return Seeded{LinkID: v.ID, Path: v.Path, URL: v.URL}, nil
}

// postJSON POSTs body and decodes a 2xx answer into out (if not nil).
func postJSON(ctx context.Context, c *http.Client, url string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(data))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
