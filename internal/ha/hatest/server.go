package hatest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"github.com/igrybkov/ha-guest-access/internal/ha"
)

// Server is an in-process HA websocket server backed by a Fake. It speaks the
// auth handshake, serves the Fake's registry lists, states and core config, and
// forwards call_service to Fake.CallService, so calls made over the wire are
// asserted with Fake.ServiceCalls. Every subscribe_events is accepted; Emit
// sends an event to the connections subscribed to its type.
//
// Use it to test code that owns a real ha.WSClient (the wired app). Package
// tests that take an ha.Client should use the Fake directly.
type Server struct {
	Fake  *Fake
	Token string

	srv *httptest.Server

	mu   sync.Mutex
	subs map[*websocket.Conn]map[string]int // conn → event type → subscription id
}

// NewServer starts a Server. Clients must authenticate with token. Call Close
// when done.
func NewServer(f *Fake, token string) *Server {
	s := &Server{Fake: f, Token: token, subs: map[*websocket.Conn]map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// URL is the websocket URL of the server (ws://127.0.0.1:port/api/websocket).
func (s *Server) URL() string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/api/websocket"
}

// HTTPURL is the base URL, as a dev-mode HA_URL would be given.
func (s *Server) HTTPURL() string { return s.srv.URL }

// Close stops the server and drops every connection.
func (s *Server) Close() {
	s.srv.CloseClientConnections()
	s.srv.Close()
}

// Subscribers returns how many connections are subscribed to eventType.
func (s *Server) Subscribers(eventType string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, subs := range s.subs {
		if _, ok := subs[eventType]; ok {
			n++
		}
	}
	return n
}

// Emit sends an event to every connection subscribed to eventType and
// reports how many got it.
func (s *Server) Emit(eventType string, data any) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for ws, subs := range s.subs {
		id, ok := subs[eventType]
		if !ok {
			continue
		}
		raw, err := json.Marshal(map[string]any{"id": id, "type": "event", "event": map[string]any{
			"event_type": eventType, "data": data, "origin": "LOCAL",
		}})
		if err != nil {
			panic(err)
		}
		if ws.Write(context.Background(), websocket.MessageText, raw) == nil {
			n++
		}
	}
	return n
}

// command is one client message after auth.
type command struct {
	ID             int            `json:"id"`
	Type           string         `json:"type"`
	EventType      string         `json:"event_type"`
	Domain         string         `json:"domain"`
	Service        string         `json:"service"`
	ServiceData    map[string]any `json:"service_data"`
	Target         *ha.Target     `json:"target"`
	ReturnResponse bool           `json:"return_response"`
	EventData      map[string]any `json:"event_data"`
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(-1)
	ctx := r.Context()

	// Conn.Write is safe for concurrent use, so Emit may write at any time.
	write := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		_ = ws.Write(ctx, websocket.MessageText, data)
	}
	write(map[string]any{"type": "auth_required", "ha_version": "2026.11.0"})
	_, data, err := ws.Read(ctx)
	if err != nil {
		return
	}
	var auth struct {
		Type        string `json:"type"`
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(data, &auth)
	if auth.Type != "auth" || auth.AccessToken != s.Token {
		write(map[string]any{"type": "auth_invalid", "message": "Invalid access token"})
		return
	}
	write(map[string]any{"type": "auth_ok", "ha_version": "2026.11.0"})

	s.mu.Lock()
	s.subs[ws] = map[string]int{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ws)
		s.mu.Unlock()
	}()

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var cmd command
		if err := json.Unmarshal(data, &cmd); err != nil {
			return
		}
		ok := func(result any) {
			write(map[string]any{"id": cmd.ID, "type": "result", "success": true, "result": result})
		}
		fail := func(code, msg string) {
			write(map[string]any{"id": cmd.ID, "type": "result", "success": false, "error": map[string]string{"code": code, "message": msg}})
		}
		f := s.Fake
		switch cmd.Type {
		case "ping":
			write(map[string]any{"id": cmd.ID, "type": "pong"})
		case "subscribe_events":
			// Answer under s.mu, as Emit writes under it: the result goes out
			// before the first event of the subscription.
			s.mu.Lock()
			s.subs[ws][cmd.EventType] = cmd.ID
			ok(nil)
			s.mu.Unlock()
		case "unsubscribe_events":
			ok(nil)
		case "config/area_registry/list":
			ok(orEmpty(f.AreaList))
		case "config/device_registry/list":
			ok(orEmpty(f.DeviceList))
		case "config/entity_registry/list":
			ok(orEmpty(f.EntityList))
		case "get_states":
			ok(orEmpty(f.StateList))
		case "config/label_registry/list":
			ok(orEmpty(f.LabelList))
		case "get_config":
			ok(f.Core)
		case "fire_event":
			if err := f.FireEvent(ctx, cmd.EventType, cmd.EventData); err != nil {
				fail("home_assistant_error", err.Error())
				continue
			}
			ok(map[string]any{"context": map[string]any{"id": "ctx"}})
		case "call_service":
			resp, err := f.CallService(ctx, cmd.Domain, cmd.Service, cmd.ServiceData, cmd.Target, cmd.ReturnResponse)
			if err != nil {
				fail("home_assistant_error", err.Error())
				continue
			}
			result := map[string]any{"context": map[string]any{"id": "ctx"}}
			if cmd.ReturnResponse {
				result["response"] = resp
			}
			ok(result)
		default:
			fail("unknown_command", "Unknown command.")
		}
	}
}

func orEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
