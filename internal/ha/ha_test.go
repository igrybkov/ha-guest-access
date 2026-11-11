package ha

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testToken = "test-token"

var quietLog = slog.New(slog.DiscardHandler)

// fakeHA is an in-process HA websocket server. It speaks the auth handshake,
// answers registry lists and service calls, and records every command.
type fakeHA struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	conns      []*websocket.Conn
	commands   []map[string]any                   // every command, in arrival order
	subs       map[*websocket.Conn]map[string]int // conn → event type → subscription id
	lists      map[string]int                     // registry list type → times served
	serviceErr *Error
	accepted   int            // connections that passed auth
	subFail    map[string]int // event type → subscribe_events left to refuse
}

func newFakeHA(t *testing.T) *fakeHA {
	f := &fakeHA{t: t, subs: map[*websocket.Conn]map[string]int{}, lists: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHA) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/api/websocket" }

func (f *fakeHA) handle(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := r.Context()
	write := func(v any) {
		data, _ := json.Marshal(v)
		_ = ws.Write(ctx, websocket.MessageText, data)
	}
	write(map[string]any{"type": "auth_required", "ha_version": "2026.11.0"})
	_, data, err := ws.Read(ctx)
	if err != nil {
		return
	}
	var auth map[string]any
	_ = json.Unmarshal(data, &auth)
	if auth["type"] != "auth" || auth["access_token"] != testToken {
		write(map[string]any{"type": "auth_invalid", "message": "Invalid access token"})
		return
	}
	write(map[string]any{"type": "auth_ok"})

	f.mu.Lock()
	f.conns = append(f.conns, ws)
	f.subs[ws] = map[string]int{}
	f.accepted++
	f.mu.Unlock()

	lastID := 0
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var cmd map[string]any
		if err := json.Unmarshal(data, &cmd); err != nil {
			f.t.Errorf("bad command: %v", err)
			return
		}
		id := int(cmd["id"].(float64))
		if id <= lastID {
			f.t.Errorf("id %d does not increase (last %d)", id, lastID)
		}
		lastID = id
		f.mu.Lock()
		f.commands = append(f.commands, cmd)
		f.mu.Unlock()

		ok := func(result any) {
			write(map[string]any{"id": id, "type": "result", "success": true, "result": result})
		}
		switch cmd["type"] {
		case "ping":
			write(map[string]any{"id": id, "type": "pong"})
		case "subscribe_events":
			typ := cmd["event_type"].(string)
			f.mu.Lock()
			refuse := f.subFail[typ] > 0
			if refuse {
				f.subFail[typ]--
			} else {
				f.subs[ws][typ] = id
			}
			f.mu.Unlock()
			if refuse {
				write(map[string]any{"id": id, "type": "result", "success": false, "error": map[string]any{"code": "unauthorized", "message": "Unauthorized"}})
				continue
			}
			ok(nil)
		case "unsubscribe_events":
			ok(nil)
		case "config/area_registry/list":
			f.countList("areas")
			ok([]map[string]any{{"area_id": "office", "name": "Office", "floor_id": nil, "labels": []string{"private"}}})
		case "config/device_registry/list":
			f.countList("devices")
			ok([]map[string]any{{"id": "d1", "name": "Reader", "model": nil, "area_id": "office", "labels": []string{}, "disabled_by": nil, "config_entries": []string{"e1"}, "identifiers": [][]string{{"esphome", "aa"}}}})
		case "config/entity_registry/list":
			f.countList("entities")
			ok([]map[string]any{{"entity_id": "media_player.office", "device_id": "d2", "area_id": nil, "platform": "sonos", "config_entry_id": "c1", "disabled_by": nil, "hidden_by": "user", "entity_category": nil, "labels": []string{"guest_music"}}})
		case "get_states":
			ok([]map[string]any{{"entity_id": "media_player.office", "state": "idle", "attributes": map[string]any{"volume_level": 0.2}, "last_changed": "2026-11-10T10:00:00+00:00"}})
		case "config/label_registry/list":
			f.countList("labels")
			ok([]map[string]any{{"label_id": "guest_music", "name": "Guest music", "color": nil}})
		case "get_config":
			ok(map[string]any{"latitude": 41.88, "longitude": -87.63, "location_name": "Home", "time_zone": "America/Chicago", "elevation": 180})
		case "fire_event":
			ok(map[string]any{"context": map[string]any{"id": "x"}})
		case "call_service":
			f.mu.Lock()
			serr := f.serviceErr
			f.mu.Unlock()
			if serr != nil {
				write(map[string]any{"id": id, "type": "result", "success": false, "error": serr})
				continue
			}
			if cmd["return_response"] == true {
				ok(map[string]any{"context": map[string]any{"id": "x"}, "response": map[string]any{"albums": []any{}}})
			} else {
				ok(map[string]any{"context": map[string]any{"id": "x"}})
			}
		default:
			write(map[string]any{"id": id, "type": "result", "success": false, "error": map[string]any{"code": "unknown_command", "message": "Unknown command."}})
		}
	}
}

func (f *fakeHA) countList(name string) {
	f.mu.Lock()
	f.lists[name]++
	f.mu.Unlock()
}

func (f *fakeHA) listCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists[name]
}

// emit sends an event to every connection subscribed to its type. It reports
// how many connections got it.
func (f *fakeHA) emit(eventType string, data any) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for ws, subs := range f.subs {
		id, ok := subs[eventType]
		if !ok {
			continue
		}
		raw, _ := json.Marshal(map[string]any{"id": id, "type": "event", "event": map[string]any{
			"event_type": eventType, "data": data, "origin": "LOCAL", "time_fired": "2026-11-10T10:00:00+00:00",
		}})
		if ws.Write(context.Background(), websocket.MessageText, raw) == nil {
			n++
		}
	}
	return n
}

// dropAll closes every live connection, as an HA restart would.
func (f *fakeHA) dropAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ws := range f.conns {
		ws.CloseNow()
	}
	f.conns = nil
	f.subs = map[*websocket.Conn]map[string]int{}
}

func (f *fakeHA) connCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns)
}

func (f *fakeHA) lastCommand(typ string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.commands) - 1; i >= 0; i-- {
		if f.commands[i]["type"] == typ {
			return f.commands[i]
		}
	}
	return nil
}

func startClient(t *testing.T, f *fakeHA, token string) (*WSClient, context.CancelFunc, <-chan error) {
	t.Helper()
	c := New(Options{URL: f.url(), Token: token, Logger: quietLog})
	c.minBackoff, c.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	})
	return c, cancel, done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestRegistriesAndStates(t *testing.T) {
	f := newFakeHA(t)
	c, _, _ := startClient(t, f, testToken)
	ctx := testCtx(t)

	areas, err := c.Areas(ctx)
	if err != nil || len(areas) != 1 || areas[0].AreaID != "office" || areas[0].Name != "Office" || areas[0].Labels[0] != "private" {
		t.Fatalf("Areas = %+v, %v", areas, err)
	}
	devices, err := c.Devices(ctx)
	if err != nil || len(devices) != 1 || devices[0].AreaID != "office" || devices[0].Identifiers[0][0] != "esphome" {
		t.Fatalf("Devices = %+v, %v", devices, err)
	}
	entities, err := c.Entities(ctx)
	if err != nil || len(entities) != 1 || entities[0].Platform != "sonos" || entities[0].AreaID != "" ||
		entities[0].HiddenBy != "user" || entities[0].EntityCategory != "" || entities[0].Labels[0] != "guest_music" {
		t.Fatalf("Entities = %+v, %v", entities, err)
	}
	states, err := c.States(ctx)
	if err != nil || len(states) != 1 || states[0].State != "idle" {
		t.Fatalf("States = %+v, %v", states, err)
	}
	labels, err := c.Labels(ctx)
	if err != nil || len(labels) != 1 || labels[0].LabelID != "guest_music" || labels[0].Name != "Guest music" {
		t.Fatalf("Labels = %+v, %v", labels, err)
	}
	cfg, err := c.Config(ctx)
	if err != nil || cfg.Latitude != 41.88 || cfg.Longitude != -87.63 || cfg.TimeZone != "America/Chicago" {
		t.Fatalf("Config = %+v, %v", cfg, err)
	}
}

func TestFireEvent(t *testing.T) {
	f := newFakeHA(t)
	c, _, _ := startClient(t, f, testToken)
	ctx := testCtx(t)

	if err := c.FireEvent(ctx, "guest_access_action", map[string]any{"link": "Anna"}); err != nil {
		t.Fatal(err)
	}
	sent := f.lastCommand("fire_event")
	data, _ := sent["event_data"].(map[string]any)
	if sent["event_type"] != "guest_access_action" || data["link"] != "Anna" {
		t.Fatalf("sent %v", sent)
	}
}

func TestCallService(t *testing.T) {
	f := newFakeHA(t)
	c, _, _ := startClient(t, f, testToken)
	ctx := testCtx(t)

	resp, err := c.CallService(ctx, "media_player", "volume_set", map[string]any{"volume_level": 0.15},
		&Target{EntityID: []string{"media_player.office"}}, false)
	if err != nil || resp != nil {
		t.Fatalf("CallService = %s, %v", resp, err)
	}
	cmd := f.lastCommand("call_service")
	if cmd["domain"] != "media_player" || cmd["service"] != "volume_set" {
		t.Errorf("cmd = %v", cmd)
	}
	if got := cmd["service_data"].(map[string]any)["volume_level"]; got != 0.15 {
		t.Errorf("volume_level = %v", got)
	}
	if got := cmd["target"].(map[string]any)["entity_id"].([]any)[0]; got != "media_player.office" {
		t.Errorf("target = %v", cmd["target"])
	}
	if _, ok := cmd["return_response"]; ok {
		t.Errorf("return_response sent without being asked")
	}

	resp, err = c.CallService(ctx, "music_assistant", "search", map[string]any{"name": "x"}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp) != `{"albums":[]}` {
		t.Errorf("response = %s, want the inner response object", resp)
	}
	if cmd := f.lastCommand("call_service"); cmd["return_response"] != true {
		t.Errorf("return_response = %v", cmd["return_response"])
	}

	f.mu.Lock()
	f.serviceErr = &Error{Code: "service_validation_error", Message: "bad entity"}
	f.mu.Unlock()
	_, err = c.CallService(ctx, "media_player", "media_stop", nil, nil, false)
	var herr *Error
	if !errors.As(err, &herr) || herr.Code != "service_validation_error" {
		t.Errorf("err = %v, want *Error service_validation_error", err)
	}
}

func TestRequestIDsIncreaseUnderConcurrency(t *testing.T) {
	f := newFakeHA(t)
	c, _, _ := startClient(t, f, testToken)
	ctx := testCtx(t)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.States(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// The server checks the order; it fails the test on a non-increasing id.
}

func TestEventsAndReconnect(t *testing.T) {
	f := newFakeHA(t)
	c, _, _ := startClient(t, f, testToken)
	ctx := testCtx(t)

	got := make(chan Event, 10)
	if err := c.SubscribeEvents(ctx, "tag_scanned", func(ev Event) { got <- ev }); err != nil {
		t.Fatal(err)
	}
	// Subscribing before the connection is up is fine too; wait for it.
	waitFor(t, "subscription", func() bool { return f.emit("tag_scanned", map[string]any{"tag_id": "a"}) == 1 })
	ev := <-got
	if ev.EventType != "tag_scanned" || !strings.Contains(string(ev.Data), `"tag_id":"a"`) || ev.Origin != "LOCAL" {
		t.Fatalf("event = %+v", ev)
	}

	f.dropAll()
	waitFor(t, "reconnect and re-subscribe", func() bool {
		return f.connCount() == 1 && f.emit("tag_scanned", map[string]any{"tag_id": "b"}) == 1
	})
	select {
	case ev := <-got:
		if !strings.Contains(string(ev.Data), `"tag_id":"b"`) {
			t.Fatalf("event after reconnect = %s", ev.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event after reconnect")
	}

	// Requests work on the new connection.
	if _, err := c.Areas(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestResubscribeFailureReconnects(t *testing.T) {
	f := newFakeHA(t)
	c, _, _ := startClient(t, f, testToken)
	ctx := testCtx(t)
	got := make(chan Event, 10)
	if err := c.SubscribeEvents(ctx, "tag_scanned", func(ev Event) { got <- ev }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "subscription", func() bool { return f.emit("tag_scanned", map[string]any{"tag_id": "a"}) == 1 })

	// HA refuses the resubscribe on the next connection only. The client
	// must drop that connection and subscribe again on a new one.
	f.mu.Lock()
	f.subFail = map[string]int{"tag_scanned": 1}
	f.mu.Unlock()
	f.dropAll()
	waitFor(t, "subscription on a third connection", func() bool {
		return f.emit("tag_scanned", map[string]any{"tag_id": "b"}) == 1
	})
	f.mu.Lock()
	accepted, left := f.accepted, f.subFail["tag_scanned"]
	f.mu.Unlock()
	if left != 0 || accepted != 3 {
		t.Errorf("connections = %d, refusals left = %d; want 3 and 0", accepted, left)
	}
}

func TestSlowSubscriberDoesNotBlockRequests(t *testing.T) {
	f := newFakeHA(t)
	c, _, _ := startClient(t, f, testToken)
	ctx := testCtx(t)

	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{}, 1)
	err := c.SubscribeEvents(ctx, "tag_scanned", func(Event) {
		// A callback that calls back into HA must not deadlock the read loop.
		if _, err := c.States(ctx); err != nil {
			t.Error(err)
		}
		entered <- struct{}{}
		<-release
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "subscription", func() bool { return f.emit("tag_scanned", nil) == 1 })
	<-entered
	if _, err := c.Labels(ctx); err != nil {
		t.Fatalf("request while a callback blocks: %v", err)
	}
}

func TestRegistryCacheRefreshOnUpdate(t *testing.T) {
	f := newFakeHA(t)
	c, _, _ := startClient(t, f, testToken)
	ctx := testCtx(t)

	for range 3 {
		if _, err := c.Entities(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.listCount("entities"); n != 1 {
		t.Fatalf("entity list fetched %d times, want 1 (cached)", n)
	}
	waitFor(t, "registry subscription", func() bool {
		return f.emit("entity_registry_updated", map[string]any{"action": "update", "entity_id": "media_player.office"}) == 1
	})
	waitFor(t, "refetch after update", func() bool {
		_, _ = c.Entities(ctx)
		return f.listCount("entities") == 2
	})
	// Other caches stay.
	_, _ = c.Areas(ctx)
	_, _ = c.Areas(ctx)
	if n := f.listCount("areas"); n != 1 {
		t.Errorf("area list fetched %d times, want 1", n)
	}
	waitFor(t, "area subscription", func() bool {
		return f.emit("area_registry_updated", map[string]any{"action": "update", "area_id": "office"}) == 1
	})
	waitFor(t, "area refetch", func() bool {
		_, _ = c.Areas(ctx)
		return f.listCount("areas") == 2
	})
	_, _ = c.Labels(ctx)
	_, _ = c.Labels(ctx)
	if n := f.listCount("labels"); n != 1 {
		t.Errorf("label list fetched %d times, want 1", n)
	}
	waitFor(t, "label subscription", func() bool {
		return f.emit("label_registry_updated", map[string]any{"action": "update", "label_id": "guest_music"}) == 1
	})
	waitFor(t, "label refetch", func() bool {
		_, _ = c.Labels(ctx)
		return f.listCount("labels") == 2
	})
}

func TestUnsubscribeOnCancel(t *testing.T) {
	f := newFakeHA(t)
	c, _, _ := startClient(t, f, testToken)
	ctx := testCtx(t)

	got := make(chan Event, 10)
	sctx, cancel := context.WithCancel(ctx)
	if err := c.SubscribeEvents(sctx, "state_changed", func(ev Event) { got <- ev }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "subscription", func() bool { return f.emit("state_changed", nil) == 1 })
	<-got
	cancel()
	waitFor(t, "unsubscribe", func() bool { return f.lastCommand("unsubscribe_events") != nil })
	f.emit("state_changed", nil)
	select {
	case <-got:
		t.Fatal("event delivered after cancel")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAuthInvalidRetries(t *testing.T) {
	f := newFakeHA(t)
	c, _, _ := startClient(t, f, "wrong")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := c.Areas(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Areas with a bad token: err = %v, want deadline exceeded", err)
	}
	if f.connCount() != 0 {
		t.Fatal("connection accepted with a bad token")
	}
}

func TestPingKeepsConnection(t *testing.T) {
	f := newFakeHA(t)
	c := New(Options{URL: f.url(), Token: testToken, Logger: quietLog})
	c.pingInterval, c.pingTimeout = 10*time.Millisecond, time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	waitFor(t, "pings", func() bool { return f.lastCommand("ping") != nil })
	if _, err := c.Areas(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	if n := f.connCount(); n != 1 {
		t.Errorf("connections = %d, want 1", n)
	}
}

func TestPendingRequestFailsOnDisconnect(t *testing.T) {
	// A server that authenticates and then never answers.
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
		// Read until the service call, then hang up without answering.
		for {
			_, data, err := ws.Read(r.Context())
			if err != nil || strings.Contains(string(data), `"call_service"`) {
				return
			}
		}
	}))
	defer srv.Close()
	c := New(Options{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Token: "x", Logger: quietLog})
	c.minBackoff = time.Hour // stay disconnected after the drop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	_, err := c.CallService(testCtx(t), "media_player", "media_stop", nil, nil, false)
	if !errors.Is(err, ErrDisconnected) {
		t.Fatalf("err = %v, want ErrDisconnected", err)
	}
}

func TestWebsocketURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://hass.example", "wss://hass.example/api/websocket"},
		{"https://hass.example/", "wss://hass.example/api/websocket"},
		{"http://192.0.2.10:8123", "ws://192.0.2.10:8123/api/websocket"},
		{"http://host/sub/path/", "ws://host/sub/path/api/websocket"},
		{"wss://hass.example/api/websocket", "wss://hass.example/api/websocket"},
	}
	for _, tt := range tests {
		got, err := WebsocketURL(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("WebsocketURL(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "hass.example", "ftp://hass.example"} {
		if _, err := WebsocketURL(bad); err == nil {
			t.Errorf("WebsocketURL(%q): want error", bad)
		}
	}
}

func TestDeviceIdentifiersTolerant(t *testing.T) {
	var d Device
	err := json.Unmarshal([]byte(`{"id":"d1","model":null,"area_id":"office","identifiers":[["esphome","aa"],["zha",12345]]}`), &d)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "d1" || d.AreaID != "office" || d.Identifiers[0][1] != "aa" || d.Identifiers[1][1] != "12345" {
		t.Errorf("device = %+v", d)
	}
}
