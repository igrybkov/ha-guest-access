// Package ha is the Home Assistant websocket client: auth, events, service
// calls and registry lookups over one connection.
package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Client is the part of the HA websocket API the app uses.
type Client interface {
	SubscribeEvents(ctx context.Context, eventType string, fn func(Event)) error
	CallService(ctx context.Context, domain, service string, data map[string]any, target *Target, returnResponse bool) (json.RawMessage, error)
	Areas(ctx context.Context) ([]Area, error)
	Devices(ctx context.Context) ([]Device, error)
	Entities(ctx context.Context) ([]Entity, error)
	Labels(ctx context.Context) ([]Label, error)
	States(ctx context.Context) ([]State, error)
	Config(ctx context.Context) (Config, error)
	FireEvent(ctx context.Context, eventType string, data map[string]any) error
	// OnEventsLost calls fn whenever events may have been lost: the
	// connection to HA dropped, a new one is up with every subscription
	// in place again, or a subscriber's queue was full and an event was
	// dropped. A cache kept current by events must start over then. fn
	// must be quick and must not call the client.
	OnEventsLost(fn func())
}

// Event is an HA bus event.
type Event struct {
	EventType string          `json:"event_type"`
	Data      json.RawMessage `json:"data"`
	Origin    string          `json:"origin"`
	TimeFired time.Time       `json:"time_fired"`
}

// Target is the service call target.
type Target struct {
	EntityID []string `json:"entity_id,omitempty"`
	DeviceID []string `json:"device_id,omitempty"`
	AreaID   []string `json:"area_id,omitempty"`
}

// Area is an area registry entry ("config/area_registry/list").
type Area struct {
	AreaID string   `json:"area_id"`
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
}

// Label is a label registry entry ("config/label_registry/list").
type Label struct {
	LabelID string `json:"label_id"`
	Name    string `json:"name"`
}

// Device is a device registry entry ("config/device_registry/list").
type Device struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	NameByUser    string     `json:"name_by_user"`
	Manufacturer  string     `json:"manufacturer"`
	Model         string     `json:"model"`
	AreaID        string     `json:"area_id"`
	Labels        []string   `json:"labels"`
	DisabledBy    string     `json:"disabled_by"`
	ConfigEntries []string   `json:"config_entries"`
	Identifiers   [][]string `json:"identifiers"` // [domain, id] pairs
}

// UnmarshalJSON accepts non-string identifier parts (some integrations
// store numbers) so one odd device can't break the whole registry list.
func (d *Device) UnmarshalJSON(data []byte) error {
	type plain Device
	var raw struct {
		plain
		Identifiers [][]any `json:"identifiers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*d = Device(raw.plain)
	d.Identifiers = make([][]string, len(raw.Identifiers))
	for i, pair := range raw.Identifiers {
		for _, v := range pair {
			if s, ok := v.(string); ok {
				d.Identifiers[i] = append(d.Identifiers[i], s)
			} else {
				d.Identifiers[i] = append(d.Identifiers[i], fmt.Sprint(v))
			}
		}
	}
	return nil
}

// Entity is an entity registry entry ("config/entity_registry/list").
// Platform is the integration domain (esphome, sonos, music_assistant).
type Entity struct {
	EntityID      string `json:"entity_id"`
	DeviceID      string `json:"device_id"`
	AreaID        string `json:"area_id"` // entity override; empty = device area
	Platform      string `json:"platform"`
	ConfigEntryID string `json:"config_entry_id"`
	Name          string `json:"name"`
	OriginalName  string `json:"original_name"`
	DisabledBy    string `json:"disabled_by"`
	// HiddenBy is set when a user or integration hid the entity.
	HiddenBy string `json:"hidden_by"`
	// EntityCategory is "config", "diagnostic" or empty.
	EntityCategory string   `json:"entity_category"`
	Labels         []string `json:"labels"`
}

// State is an entity state ("get_states").
type State struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged time.Time      `json:"last_changed"`
}

// Config is the part of HA's core config ("get_config") the app uses.
type Config struct {
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	LocationName string  `json:"location_name"`
	TimeZone     string  `json:"time_zone"`
}

// SupervisorURL is the Core websocket as seen from an app under Supervisor.
// Authenticate with the SUPERVISOR_TOKEN env var.
const SupervisorURL = "ws://supervisor/core/websocket"

// WebsocketURL turns an HA base URL (https://hass.example) into its websocket
// API URL (wss://hass.example/api/websocket). Used in dev mode.
func WebsocketURL(haURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(haURL))
	if err != nil {
		return "", fmt.Errorf("HA URL %q: %w", haURL, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("HA URL %q: scheme must be http or https", haURL)
	}
	if u.Host == "" {
		return "", fmt.Errorf("HA URL %q: no host", haURL)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/api/websocket") {
		u.Path += "/api/websocket"
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// Error is an error result from HA: HA got the request and answered that
// it failed. It is the only error that says a request did not run; see
// Refused.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return "home assistant: " + e.Code + ": " + e.Message }

// ErrDisconnected means the connection dropped before HA answered. The
// request may or may not have run.
var ErrDisconnected = errors.New("home assistant: disconnected")

// Refused reports whether err holds an error result from HA (*Error).
// Only then is it sure that a request did not run. Every other failure
// leaves it open:
//
//   - ErrDisconnected: the connection dropped. The request may have gone
//     out before it did.
//   - context.DeadlineExceeded or context.Canceled: the caller stopped
//     waiting, while HA may still run the request or already has.
//   - A write error on the socket: part of the request may have gone out.
//
// A caller that undoes its own bookkeeping on failure (such as giving a
// one-time use back) must do so only when Refused is true.
func Refused(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Options configures a websocket client.
type Options struct {
	URL    string // ws:// or wss:// URL of /api/websocket
	Token  string
	Logger *slog.Logger // nil = slog.Default()
}

const (
	maxMessageBytes = 64 << 20 // registry lists of a big install are several MB
	authTimeout     = 15 * time.Second
	eventQueueSize  = 256
)

// WSClient is the websocket implementation of Client.
//
// Requests made while disconnected wait for the next connection (or ctx).
// Event callbacks run on one goroutine per subscription, in event order, so a
// slow callback never blocks the read loop. Registries are cached until HA
// sends the matching *_registry_updated event or the connection drops.
type WSClient struct {
	opts Options
	log  *slog.Logger

	minBackoff, maxBackoff    time.Duration
	pingInterval, pingTimeout time.Duration

	mu    sync.Mutex
	sess  *session      // nil while disconnected
	ready chan struct{} // closed while sess != nil
	subs  map[*subscription]struct{}
	lost  []func() // OnEventsLost callbacks

	areas    cached[Area]
	devices  cached[Device]
	entities cached[Entity]
	labels   cached[Label]
}

var _ Client = (*WSClient)(nil)

// New returns a client. It does not connect; call Run.
func New(opts Options) *WSClient {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	c := &WSClient{
		opts:         opts,
		log:          log.With("component", "ha"),
		minBackoff:   time.Second,
		maxBackoff:   30 * time.Second,
		pingInterval: 30 * time.Second,
		pingTimeout:  10 * time.Second,
		ready:        make(chan struct{}),
		subs:         make(map[*subscription]struct{}),
	}
	// Registry caches drop on change. These run inline in the read loop.
	for _, ev := range []struct {
		typ string
		fn  func()
	}{
		{"area_registry_updated", c.areas.invalidate},
		{"device_registry_updated", c.devices.invalidate},
		{"entity_registry_updated", c.entities.invalidate},
		{"label_registry_updated", c.labels.invalidate},
	} {
		fn := ev.fn
		c.subs[&subscription{eventType: ev.typ, inline: func(Event) { fn() }}] = struct{}{}
	}
	return c
}

// Run connects, authenticates and keeps the connection alive with reconnects
// until ctx is done. It returns ctx.Err().
func (c *WSClient) Run(ctx context.Context) error {
	backoff := c.minBackoff
	for {
		connected, err := c.runSession(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected {
			backoff = c.minBackoff
		}
		c.log.Warn("websocket disconnected, reconnecting", "err", err, "in", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, c.maxBackoff)
	}
}

// runSession runs one connection until it fails. connected reports whether
// auth and resubscribe succeeded, so Run can reset the backoff.
func (c *WSClient) runSession(ctx context.Context) (connected bool, err error) {
	// The timeout bounds the TCP connect and the handshake only; Dial does
	// not tie the returned connection to its context.
	dctx, dcancel := context.WithTimeout(ctx, authTimeout)
	ws, _, err := websocket.Dial(dctx, c.opts.URL, nil)
	dcancel()
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxMessageBytes)
	if err := c.auth(ctx, ws); err != nil {
		return false, err
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := newSession(ws, c.log)
	readErr := make(chan error, 1)
	go func() {
		err := s.readLoop(sctx)
		s.close() // unblock requests waiting on this connection
		readErr <- err
	}()

	// Registry events may have been missed while disconnected. Drop the
	// caches before any request can use the new session.
	c.areas.invalidate()
	c.devices.invalidate()
	c.entities.invalidate()
	c.labels.invalidate()

	// Publish the session and take the subscription snapshot in one step:
	// a subscription added after this subscribes itself.
	c.mu.Lock()
	c.sess = s
	subs := make([]*subscription, 0, len(c.subs))
	for sub := range c.subs {
		subs = append(subs, sub)
	}
	close(c.ready)
	c.mu.Unlock()
	c.log.Info("connected to home assistant")

	defer func() {
		c.mu.Lock()
		c.sess = nil
		c.ready = make(chan struct{})
		c.mu.Unlock()
		s.close()
		c.eventsLost()
	}()

	// Ping first: it also guards the resubscribe below against a stalled HA.
	go c.ping(sctx, s)
	// A refused subscription would leave scans unheard on a connection that
	// looks healthy, so drop it and let Run retry. Report connected=false so
	// the backoff grows instead of retrying every second. ErrDisconnected
	// needs nothing here: the read loop has already ended.
	for _, sub := range subs {
		if err := s.subscribe(sctx, sub); err != nil {
			c.log.Error("subscribe failed", "event_type", sub.eventType, "err", err)
			if !errors.Is(err, ErrDisconnected) {
				return false, fmt.Errorf("resubscribe %s: %w", sub.eventType, err)
			}
			break
		}
	}
	if !s.isClosed() {
		c.eventsLost()
	}

	select {
	case err = <-readErr:
	case <-ctx.Done():
		err = ctx.Err()
	}
	return true, err
}

func (c *WSClient) auth(ctx context.Context, ws *websocket.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	var msg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	read := func() error {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return fmt.Errorf("auth: read: %w", err)
		}
		return json.Unmarshal(data, &msg)
	}
	if err := read(); err != nil {
		return err
	}
	if msg.Type != "auth_required" {
		return fmt.Errorf("auth: expected auth_required, got %q", msg.Type)
	}
	data, _ := json.Marshal(map[string]string{"type": "auth", "access_token": c.opts.Token})
	if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("auth: write: %w", err)
	}
	if err := read(); err != nil {
		return err
	}
	switch msg.Type {
	case "auth_ok":
		return nil
	case "auth_invalid":
		return fmt.Errorf("auth: invalid: %s", msg.Message)
	default:
		return fmt.Errorf("auth: unexpected %q", msg.Type)
	}
}

// ping closes a silent connection so Run reconnects.
func (c *WSClient) ping(ctx context.Context, s *session) {
	t := time.NewTicker(c.pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, c.pingTimeout)
		_, err := s.request(pctx, map[string]any{"type": "ping"}, nil)
		cancel()
		if err != nil && ctx.Err() == nil {
			c.log.Warn("ping failed, closing connection", "err", err)
			s.ws.CloseNow()
			return
		}
	}
}

// session waits for a live connection.
func (c *WSClient) session(ctx context.Context) (*session, error) {
	for {
		c.mu.Lock()
		s, ready := c.sess, c.ready
		c.mu.Unlock()
		if s != nil {
			return s, nil
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c *WSClient) request(ctx context.Context, msg map[string]any) (json.RawMessage, error) {
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	return s.request(ctx, msg, nil)
}

// SubscribeEvents calls fn for every event of eventType until ctx is done.
// The subscription survives reconnects. If connected, it returns the error of
// the subscribe request; if not, it subscribes on the next connection.
func (c *WSClient) SubscribeEvents(ctx context.Context, eventType string, fn func(Event)) error {
	sctx, cancel := context.WithCancel(ctx)
	sub := &subscription{eventType: eventType, fn: fn, ctx: sctx, queue: make(chan Event, eventQueueSize), log: c.log,
		dropped: c.eventsLost}

	c.mu.Lock()
	c.subs[sub] = struct{}{}
	s := c.sess
	c.mu.Unlock()

	if s != nil {
		if err := s.subscribe(ctx, sub); err != nil && !errors.Is(err, ErrDisconnected) {
			c.removeSub(sub)
			cancel()
			return fmt.Errorf("subscribe %s: %w", eventType, err)
		}
	}
	go sub.dispatch()
	go func() {
		<-sctx.Done()
		cancel()
		c.removeSub(sub)
	}()
	return nil
}

func (c *WSClient) removeSub(sub *subscription) {
	c.mu.Lock()
	delete(c.subs, sub)
	s := c.sess
	c.mu.Unlock()
	if s == nil {
		return
	}
	if id, ok := s.dropSub(sub); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.request(ctx, map[string]any{"type": "unsubscribe_events", "subscription": id}, nil)
	}
}

// OnEventsLost registers fn. See Client.
func (c *WSClient) OnEventsLost(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lost = append(c.lost, fn)
}

// eventsLost calls every OnEventsLost callback.
func (c *WSClient) eventsLost() {
	c.mu.Lock()
	fns := slices.Clone(c.lost)
	c.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// CallService calls a service. With returnResponse it returns the service
// response (the "response" field of the result), else nil. On failure,
// Refused tells whether HA refused the call or the outcome is unknown.
func (c *WSClient) CallService(ctx context.Context, domain, service string, data map[string]any, target *Target, returnResponse bool) (json.RawMessage, error) {
	msg := map[string]any{"type": "call_service", "domain": domain, "service": service}
	if data != nil {
		msg["service_data"] = data
	}
	if target != nil {
		msg["target"] = target
	}
	if returnResponse {
		msg["return_response"] = true
	}
	raw, err := c.request(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %w", domain, service, err)
	}
	if !returnResponse {
		return nil, nil
	}
	var res struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("%s.%s: decode response: %w", domain, service, err)
	}
	return res.Response, nil
}

func (c *WSClient) Areas(ctx context.Context) ([]Area, error) {
	return cachedList(ctx, c, &c.areas, "config/area_registry/list")
}

func (c *WSClient) Devices(ctx context.Context) ([]Device, error) {
	return cachedList(ctx, c, &c.devices, "config/device_registry/list")
}

func (c *WSClient) Entities(ctx context.Context) ([]Entity, error) {
	return cachedList(ctx, c, &c.entities, "config/entity_registry/list")
}

func (c *WSClient) States(ctx context.Context) ([]State, error) {
	return list[State](ctx, c, "get_states")
}

func (c *WSClient) Labels(ctx context.Context) ([]Label, error) {
	return cachedList(ctx, c, &c.labels, "config/label_registry/list")
}

// Config returns HA's core config ("get_config"). It is not cached: the
// home location rarely changes, and callers fetch it rarely.
func (c *WSClient) Config(ctx context.Context) (Config, error) {
	raw, err := c.request(ctx, map[string]any{"type": "get_config"})
	if err != nil {
		return Config{}, fmt.Errorf("get_config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("get_config: decode: %w", err)
	}
	return cfg, nil
}

// FireEvent fires an event on the HA bus ("fire_event").
func (c *WSClient) FireEvent(ctx context.Context, eventType string, data map[string]any) error {
	msg := map[string]any{"type": "fire_event", "event_type": eventType}
	if data != nil {
		msg["event_data"] = data
	}
	if _, err := c.request(ctx, msg); err != nil {
		return fmt.Errorf("fire_event %s: %w", eventType, err)
	}
	return nil
}

// cachedList serves a registry from cache. It waits for a connection first:
// the caches are dropped on connect, so a fetch started earlier would be
// thrown away.
func cachedList[T any](ctx context.Context, c *WSClient, cache *cached[T], typ string) ([]T, error) {
	if _, err := c.session(ctx); err != nil {
		return nil, fmt.Errorf("%s: %w", typ, err)
	}
	return cache.get(ctx, func(ctx context.Context) ([]T, error) { return list[T](ctx, c, typ) })
}

func list[T any](ctx context.Context, c *WSClient, typ string) ([]T, error) {
	raw, err := c.request(ctx, map[string]any{"type": typ})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", typ, err)
	}
	var out []T
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", typ, err)
	}
	return out, nil
}

// cached is one registry list. gen makes sure a fetch that raced with an
// invalidation does not store stale data. The lock is never held during a
// fetch: invalidate runs in the read loop, which the fetch waits on.
type cached[T any] struct {
	mu  sync.Mutex
	val []T
	ok  bool
	gen uint64
}

func (c *cached[T]) get(ctx context.Context, fetch func(context.Context) ([]T, error)) ([]T, error) {
	c.mu.Lock()
	if c.ok {
		v := slices.Clone(c.val)
		c.mu.Unlock()
		return v, nil
	}
	gen := c.gen
	c.mu.Unlock()

	v, err := fetch(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.gen == gen {
		c.val, c.ok = v, true
	}
	c.mu.Unlock()
	return slices.Clone(v), nil
}

func (c *cached[T]) invalidate() {
	c.mu.Lock()
	c.gen++
	c.ok, c.val = false, nil
	c.mu.Unlock()
}

// subscription is one SubscribeEvents call. Internal subscriptions set
// inline and run in the read loop; they must be fast.
type subscription struct {
	eventType string
	inline    func(Event)

	fn    func(Event)
	ctx   context.Context
	queue chan Event
	log   *slog.Logger
	// dropped runs when the queue is full and an event is dropped.
	dropped func()
}

func (s *subscription) deliver(ev Event) {
	if s.inline != nil {
		s.inline(ev)
		return
	}
	select {
	case s.queue <- ev:
	case <-s.ctx.Done():
	default:
		s.log.Warn("event queue full, dropping event", "event_type", s.eventType)
		if s.dropped != nil {
			s.dropped()
		}
	}
}

func (s *subscription) dispatch() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case ev := <-s.queue:
			s.fn(ev)
		}
	}
}

// session is one authenticated connection.
type session struct {
	ws  *websocket.Conn
	log *slog.Logger

	// writeMu orders id allocation and writes: HA rejects ids that don't increase.
	writeMu sync.Mutex
	nextID  int

	mu      sync.Mutex
	pending map[int]chan result
	subIDs  map[int]*subscription // HA subscription id (the subscribe request id)
	closed  bool
}

type result struct {
	raw json.RawMessage
	err error
}

func newSession(ws *websocket.Conn, log *slog.Logger) *session {
	return &session{
		ws:      ws,
		log:     log,
		pending: make(map[int]chan result),
		subIDs:  make(map[int]*subscription),
	}
}

// request sends msg with a fresh id and waits for its result. If sub is set,
// the id is mapped to it before the write, so no event can arrive unmapped.
func (s *session) request(ctx context.Context, msg map[string]any, sub *subscription) (json.RawMessage, error) {
	ch := make(chan result, 1)

	s.writeMu.Lock()
	s.nextID++
	id := s.nextID
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.writeMu.Unlock()
		return nil, ErrDisconnected
	}
	s.pending[id] = ch
	if sub != nil {
		s.subIDs[id] = sub
	}
	s.mu.Unlock()
	msg["id"] = id
	data, err := json.Marshal(msg)
	if err == nil {
		err = s.ws.Write(ctx, websocket.MessageText, data)
	}
	s.writeMu.Unlock()

	forget := func() {
		s.mu.Lock()
		delete(s.pending, id)
		if sub != nil {
			delete(s.subIDs, id)
		}
		s.mu.Unlock()
	}
	if err != nil {
		forget()
		if s.isClosed() {
			return nil, ErrDisconnected
		}
		return nil, err
	}
	select {
	case r := <-ch:
		if r.err != nil && sub != nil {
			forget()
		}
		return r.raw, r.err
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

func (s *session) subscribe(ctx context.Context, sub *subscription) error {
	_, err := s.request(ctx, map[string]any{"type": "subscribe_events", "event_type": sub.eventType}, sub)
	return err
}

// dropSub unmaps sub and returns its HA subscription id.
func (s *session) dropSub(sub *subscription) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range s.subIDs {
		if v == sub {
			delete(s.subIDs, id)
			return id, true
		}
	}
	return 0, false
}

func (s *session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// close fails every pending request with ErrDisconnected.
func (s *session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for id, ch := range s.pending {
		ch <- result{err: ErrDisconnected}
		delete(s.pending, id)
	}
}

type incoming struct {
	ID      int             `json:"id"`
	Type    string          `json:"type"`
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   *Error          `json:"error"`
	Event   json.RawMessage `json:"event"`
}

func (s *session) readLoop(ctx context.Context) error {
	for {
		_, data, err := s.ws.Read(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		var msg incoming
		if err := json.Unmarshal(data, &msg); err != nil {
			s.log.Warn("bad message from home assistant", "err", err)
			continue
		}
		switch msg.Type {
		case "result", "pong":
			s.resolve(msg)
		case "event":
			s.event(msg)
		}
	}
}

func (s *session) resolve(msg incoming) {
	s.mu.Lock()
	ch, ok := s.pending[msg.ID]
	delete(s.pending, msg.ID)
	s.mu.Unlock()
	if !ok {
		return
	}
	switch {
	case msg.Type == "pong" || msg.Success:
		ch <- result{raw: msg.Result}
	case msg.Error != nil:
		ch <- result{err: msg.Error}
	default:
		ch <- result{err: &Error{Code: "unknown_error", Message: "request failed"}}
	}
}

func (s *session) event(msg incoming) {
	s.mu.Lock()
	sub, ok := s.subIDs[msg.ID]
	s.mu.Unlock()
	if !ok {
		return
	}
	var ev Event
	if err := json.Unmarshal(msg.Event, &ev); err != nil {
		s.log.Warn("bad event from home assistant", "err", err)
		return
	}
	sub.deliver(ev)
}
