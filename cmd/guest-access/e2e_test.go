package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
	"github.com/igrybkov/ha-guest-access/internal/ma"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

const testToken = "e2e-token"

// home is HA's home location.
var home = struct{ lat, lon float64 }{41.8781, -87.6298}

// houseHA is a fake HA: a guest room lamp, a bedroom light the guest may
// not touch, a front door lock, an office switch and a speaker.
func houseHA() *hatest.Fake {
	return &hatest.Fake{
		AreaList:   []ha.Area{{AreaID: "guest_room", Name: "Guest room"}, {AreaID: "bedroom", Name: "Bedroom"}},
		DeviceList: []ha.Device{{ID: "dev-lamp", Name: "Guest lamp", AreaID: "guest_room"}},
		EntityList: []ha.Entity{
			{EntityID: "light.guest_room", DeviceID: "dev-lamp", Name: "Guest lamp"},
			{EntityID: "lock.guest_room_window", AreaID: "guest_room"},
			{EntityID: "light.bedroom", AreaID: "bedroom"},
			{EntityID: "lock.front_door", Name: "Front door"},
			{EntityID: "switch.office", Name: "Office"},
			{EntityID: "media_player.living_room", Name: "Living room"},
		},
		StateList: []ha.State{
			{EntityID: "light.guest_room", State: "off", Attributes: map[string]any{
				"friendly_name": "Guest lamp", "user_id": "secret-user", "entity_picture": "/secret-pic"}},
			{EntityID: "light.bedroom", State: "on"},
			{EntityID: "lock.front_door", State: "locked", Attributes: map[string]any{"changed_by": "secret-user"}},
			{EntityID: "switch.office", State: "off"},
			{EntityID: "media_player.living_room", State: "idle"},
		},
		Core: ha.Config{Latitude: home.lat, Longitude: home.lon},
	}
}

// templatesYAML is the templates.yaml the app starts with, as a git pull
// would bring it.
var templatesYAML = model.Templates{Version: 1, Templates: []model.Template{
	{ID: "guest_room", Name: "Guest room", Rules: []model.Rule{{
		Select: model.Selector{Areas: []string{"guest_room"}}, Actions: []string{"light.turn_on", "light.turn_off"}}}},
	{ID: "entry", Name: "Entry", Access: &model.Access{NearHomeM: 300}, Rules: []model.Rule{{
		Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock"}}}},
	{ID: "office", Name: "Office", Access: &model.Access{Network: model.NetworkLocal}, Rules: []model.Rule{{
		Select: model.Selector{Entities: []string{"switch.office"}}, Actions: []string{"switch.turn_on"}}}},
	{ID: "music", Name: "Music", Party: true, Rules: []model.Rule{{
		Select: model.Selector{Entities: []string{"media_player.living_room"}}, Actions: []string{"media_player.volume_set"}}}},
}}

// app is a running run() with both base URLs.
type app struct {
	t       *testing.T
	admin   string
	guest   string
	ha      *hatest.Server
	fake    *hatest.Fake
	failHA  *atomic.Bool
	now     *atomic.Pointer[time.Time]
	done    chan error
	stop    context.CancelFunc
	stopped bool
	dataDir string
}

type appOpts struct {
	options    func(*config.Options)
	devAnyPeer *bool
	ma         ma.Client
	logger     *slog.Logger // nil discards
	// fake replaces houseHA. With sim, a hatest.Sim answers service
	// calls, so they change states and send state_changed.
	fake *hatest.Fake
	sim  bool
	// sslDir is the ssl folder. "" = an empty temp dir.
	sslDir string
}

// testClient sends no cookies of its own and does not pool connections,
// so Shutdown never waits on an idle one. It takes any certificate, for a
// guest port with ssl on; the TLS tests check the certificate themselves.
var testClient = &http.Client{Transport: &http.Transport{
	DisableKeepAlives: true,
	TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
}, Timeout: 30 * time.Second}

// t0 is the app clock's start.
var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func startApp(t *testing.T, o appOpts) *app {
	t.Helper()
	fake := o.fake
	if fake == nil {
		fake = houseHA()
	}
	failHA := &atomic.Bool{}
	// Set once, before start: Respond is read on the server's goroutines.
	fake.Respond = func(hatest.ServiceCall) (json.RawMessage, error) {
		if failHA.Load() {
			return nil, errors.New("entity unavailable")
		}
		return nil, nil
	}
	srv := hatest.NewServer(fake, testToken)
	t.Cleanup(srv.Close)
	if o.sim {
		// Set before run starts: Respond is read on the server's goroutines.
		sim := hatest.NewSim(fake, hatest.SimOptions{Emit: func(typ string, data any) { srv.Emit(typ, data) }})
		t.Cleanup(sim.Stop)
		fake.Respond = sim.Respond
	}

	cfgDir, dataDir := t.TempDir(), t.TempDir()
	raw, _ := yaml.Marshal(templatesYAML)
	if err := os.WriteFile(filepath.Join(cfgDir, "templates.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	opts := config.DefaultOptions()
	opts.PublicBaseURL = "https://guest.example.com"
	opts.StreamCheck = 20 * time.Millisecond
	if o.options != nil {
		o.options(&opts)
	}
	anyPeer := true
	if o.devAnyPeer != nil {
		anyPeer = *o.devAnyPeer
	}
	sslDir := o.sslDir
	if sslDir == "" {
		sslDir = t.TempDir()
	}
	cfg := config.Config{LogLevel: "debug", ConfigDir: cfgDir, DataDir: dataDir, SSLDir: sslDir, HAURL: srv.URL(), HAToken: testToken,
		DevAdminAnyPeer: anyPeer, Options: opts}

	adminLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	guestLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	now := &atomic.Pointer[time.Time]{}
	start := t0
	now.Store(&start)
	ctx, cancel := context.WithCancel(context.Background())
	guestScheme := "http://"
	if opts.TLS.Enabled {
		guestScheme = "https://"
	}
	a := &app{t: t, admin: "http://" + adminLn.Addr().String(), guest: guestScheme + guestLn.Addr().String(),
		ha: srv, fake: fake, failHA: failHA, now: now, done: make(chan error, 1), stop: cancel, dataDir: dataDir}
	ro := options{
		Logger: cmp.Or(o.logger, slog.New(slog.DiscardHandler)),
		Now:    func() time.Time { return *now.Load() },
	}
	if o.ma != nil {
		c := o.ma
		ro.NewMA = func(string, string, time.Duration) (ma.Client, error) { return c, nil }
	}
	go func() { a.done <- run(ctx, cfg, adminLn, guestLn, ro) }()
	t.Cleanup(func() { a.shutdown() })
	waitFor(t, "state_changed subscription", func() bool { return srv.Subscribers("state_changed") > 0 })
	return a
}

func (a *app) shutdown() {
	a.t.Helper()
	if a.stopped {
		return
	}
	a.stopped = true
	a.stop()
	select {
	case err := <-a.done:
		if err != nil {
			a.t.Errorf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		a.t.Fatal("run did not stop within 5s of cancel")
	}
}

func (a *app) advance(d time.Duration) {
	n := a.now.Load().Add(d)
	a.now.Store(&n)
}

// call is one HTTP request. Guest POSTs get the guest Origin unless
// origin is set ("-" sends none).
type call struct {
	method, url string
	body        any
	ctype       string
	origin      string
	headers     map[string]string
	cookies     []*http.Cookie
}

type reply struct {
	code    int
	header  http.Header
	body    []byte
	cookies []*http.Cookie
}

func (r reply) has(s string) bool { return bytes.Contains(r.body, []byte(s)) }

func (a *app) do(c call) reply {
	a.t.Helper()
	var body io.Reader
	switch b := c.body.(type) {
	case nil:
	case string:
		body = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(c.method, c.url, body)
	if err != nil {
		a.t.Fatal(err)
	}
	if c.body != nil || (c.method != "GET" && c.method != "HEAD") {
		ct := c.ctype
		if ct == "" {
			ct = "application/json"
		}
		if ct != "-" {
			req.Header.Set("Content-Type", ct)
		}
	}
	if strings.HasPrefix(c.url, a.guest) && c.method == "POST" {
		switch c.origin {
		case "":
			req.Header.Set("Origin", a.guest)
		case "-":
		default:
			req.Header.Set("Origin", c.origin)
		}
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	resp, err := testClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", c.method, c.url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return reply{code: resp.StatusCode, header: resp.Header, body: raw, cookies: resp.Cookies()}
}

func (a *app) adminJSON(method, path string, body any) reply {
	return a.do(call{method: method, url: a.admin + path, body: body})
}

// newLink makes a link through the admin API.
func (a *app) newLink(draft map[string]any) model.Link {
	a.t.Helper()
	if _, ok := draft["valid_until"]; !ok {
		draft["valid_until"] = t0.Add(24 * time.Hour)
	}
	if _, ok := draft["name"]; !ok {
		draft["name"] = "Anna's visit"
	}
	r := a.adminJSON("POST", "/api/links", draft)
	if r.code != http.StatusCreated {
		a.t.Fatalf("create link: %d %s", r.code, r.body)
	}
	var l model.Link
	_ = json.Unmarshal(r.body, &l)
	return l
}

func (a *app) act(l model.Link, entity, action string, data map[string]any, cookies ...*http.Cookie) reply {
	return a.do(call{method: "POST", url: a.guest + "/g/" + l.Slug + "/act", cookies: cookies,
		body: map[string]any{"entity_id": entity, "action": action, "data": data}})
}

func (a *app) state(l model.Link, cookies ...*http.Cookie) reply {
	return a.do(call{method: "GET", url: a.guest + "/g/" + l.Slug + "/state", cookies: cookies})
}

// named returns the cookie of name base for any link: the real name is
// base + "_" + a link id.
func named(cs []*http.Cookie, base string) *http.Cookie {
	for _, c := range cs {
		if id, ok := strings.CutPrefix(c.Name, base+"_"); ok && !strings.Contains(id, "_") {
			return c
		}
	}
	return nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func boolPtr(b bool) *bool { return &b }

// flipChar returns s with the character at i swapped for another base64url
// character, so the result always differs.
func flipChar(s string, i int) string {
	c := byte('A')
	if s[i] == 'A' {
		c = 'B'
	}
	return s[:i] + string(c) + s[i+1:]
}

// The whole guest path: password, state, an action that reaches HA with
// one entity, and the event with the guest's name.
func TestE2EGuestFlow(t *testing.T) {
	a := startApp(t, appOpts{})
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	if r := a.adminJSON("POST", "/api/links/"+l.ID+"/passwords", map[string]any{"label": "Anna", "password": "+1 555 0100"}); r.code != 201 {
		t.Fatalf("add password: %d %s", r.code, r.body)
	}
	if r := a.do(call{method: "GET", url: a.guest + "/g/" + l.Slug}); r.code != 200 || !r.has("<script type=\"module\"") {
		t.Fatalf("page: %d %s", r.code, r.body)
	}
	if r := a.state(l); r.code != 401 || !r.has(`"need":"password"`) {
		t.Fatalf("state without session: %d %s", r.code, r.body)
	}
	pw := func(p string) reply {
		return a.do(call{method: "POST", url: a.guest + "/g/" + l.Slug + "/password", body: map[string]any{"password": p}})
	}
	if r := pw("+15550100"); r.code != 403 {
		t.Errorf("wrong password: %d", r.code)
	}
	r := pw("+1 555 0100")
	session := named(r.cookies, "ga_session")
	if r.code != 200 || session == nil || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("login: %d %+v", r.code, session)
	}
	if r := a.state(l, session); r.code != 200 {
		t.Fatalf("state: %d %s", r.code, r.body)
	}
	if r := a.act(l, "light.guest_room", "light.turn_on", map[string]any{"brightness_pct": 30}, session); r.code != 200 {
		t.Fatalf("act: %d %s", r.code, r.body)
	}
	waitFor(t, "service call", func() bool { return len(a.fake.ServiceCalls()) == 1 })
	c := a.fake.ServiceCalls()[0]
	if c.Domain != "light" || c.Service != "turn_on" || !reflect.DeepEqual(c.Target, &ha.Target{EntityID: []string{"light.guest_room"}}) ||
		c.Data["brightness_pct"] != float64(30) {
		t.Errorf("call = %+v", c)
	}
	waitFor(t, "action event", func() bool { return len(a.fake.FiredEvents()) == 1 })
	ev := a.fake.FiredEvents()[0]
	if ev.EventType != "guest_access_action" || ev.Data["guest"] != "Anna" || ev.Data["link_id"] != l.ID ||
		ev.Data["link_name"] != "Anna's visit" || ev.Data["entity_id"] != "light.guest_room" || ev.Data["action"] != "light.turn_on" || ev.Data["ip"] != "127.0.0.1" {
		t.Errorf("event = %+v", ev)
	}

	// Forged and missing cookies.
	for name, ck := range map[string]*http.Cookie{
		"forged":   {Name: "ga_session_" + l.ID, Value: "AQIAAAAAZ2ZnZ2ZnZ2ZnZ2Zn"},
		"tampered": {Name: "ga_session_" + l.ID, Value: flipChar(session.Value, 20)},
	} {
		if r := a.act(l, "light.guest_room", "light.turn_on", nil, ck); r.code != 401 {
			t.Errorf("%s cookie: %d", name, r.code)
		}
	}
	if r := a.act(l, "light.guest_room", "light.turn_on", nil); r.code != 401 {
		t.Errorf("no cookie: %d", r.code)
	}
	// A session of link A does not open link B with the same password.
	b := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	a.adminJSON("POST", "/api/links/"+b.ID+"/passwords", map[string]any{"label": "Anna", "password": "+1 555 0100"})
	if r := a.state(b, &http.Cookie{Name: "ga_session_" + b.ID, Value: session.Value}); r.code != 401 {
		t.Errorf("cookie of A on B: %d", r.code)
	}
	if n := len(a.fake.ServiceCalls()); n != 1 {
		t.Errorf("%d HA calls, want 1", n)
	}
}

// Every escape row from authz, attempted over HTTP.
func TestE2EEscapes(t *testing.T) {
	a := startApp(t, appOpts{})
	l := a.newLink(map[string]any{"templates": []string{"guest_room", "entry", "office"}})
	for _, tc := range []struct {
		name string
		body any
		want int
	}{
		{"another entity", map[string]any{"entity_id": "light.bedroom", "action": "light.turn_on"}, 403},
		{"entity in the area, wrong domain", map[string]any{"entity_id": "lock.guest_room_window", "action": "lock.unlock"}, 403},
		{"extra data key", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "data": map[string]any{"rgb_color": []int{255, 0, 0}}}, 400},
		{"area target", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "data": map[string]any{"area_id": "bedroom"}}, 400},
		{"device target", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "data": map[string]any{"device_id": "dev-lamp"}}, 400},
		{"entity list", map[string]any{"entity_id": []string{"light.guest_room", "light.bedroom"}, "action": "light.turn_on"}, 400},
		{"target object", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on", "target": map[string]any{"entity_id": "light.bedroom"}}, 400},
		{"case trick", map[string]any{"entity_id": "LIGHT.GUEST_ROOM", "action": "light.turn_on"}, 403},
		{"whitespace trick", map[string]any{"entity_id": "light.guest_room ", "action": "light.turn_on"}, 403},
		{"action case trick", map[string]any{"entity_id": "light.guest_room", "action": "Light.Turn_On"}, 403},
		{"wrong network", map[string]any{"entity_id": "switch.office", "action": "switch.turn_on"}, 403},
		{"needs location", map[string]any{"entity_id": "lock.front_door", "action": "lock.unlock"}, 403},
		{"action not granted", map[string]any{"entity_id": "light.guest_room", "action": "light.toggle"}, 403},
	} {
		r := a.do(call{method: "POST", url: a.guest + "/g/" + l.Slug + "/act", body: tc.body})
		if r.code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, r.code, r.body, tc.want)
		}
	}
	if calls := a.fake.ServiceCalls(); len(calls) != 0 {
		t.Errorf("escapes reached HA: %+v", calls)
	}
}

func TestE2ELinkWindow(t *testing.T) {
	a := startApp(t, appOpts{})
	expiring := a.newLink(map[string]any{"templates": []string{"guest_room"}, "valid_until": t0.Add(time.Hour)})
	off := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	scheduled := a.newLink(map[string]any{"templates": []string{"guest_room"}, "valid_from": t0.Add(time.Hour), "valid_until": t0.Add(2 * time.Hour)})
	if r := a.act(expiring, "light.guest_room", "light.turn_on", nil); r.code != 200 {
		t.Fatalf("before expiry: %d", r.code)
	}
	if r := a.adminJSON("POST", "/api/links/"+off.ID+"/deactivate", nil); r.code != 200 {
		t.Fatalf("deactivate: %d %s", r.code, r.body)
	}
	if r := a.act(off, "light.guest_room", "light.turn_on", nil); r.code != http.StatusGone {
		t.Errorf("inactive: %d", r.code)
	}
	if r := a.act(scheduled, "light.guest_room", "light.turn_on", nil); r.code != http.StatusGone {
		t.Errorf("scheduled: %d", r.code)
	}
	a.advance(time.Hour)
	if r := a.act(expiring, "light.guest_room", "light.turn_on", nil); r.code != http.StatusGone {
		t.Errorf("expired: %d", r.code)
	}
	if r := a.act(scheduled, "light.guest_room", "light.turn_on", nil); r.code != 200 {
		t.Errorf("scheduled, now active: %d", r.code)
	}
	// An expired link needs a new end before it can be activated.
	if r := a.adminJSON("POST", "/api/links/"+expiring.ID+"/activate", map[string]any{}); r.code != http.StatusConflict {
		t.Errorf("activate with a past end: %d %s", r.code, r.body)
	}
	if r := a.adminJSON("POST", "/api/links/"+expiring.ID+"/activate", map[string]any{"valid_until": t0.Add(3 * time.Hour)}); r.code != 200 {
		t.Fatalf("activate with a new end: %d %s", r.code, r.body)
	}
	if r := a.act(expiring, "light.guest_room", "light.turn_on", nil); r.code != 200 {
		t.Errorf("activated again: %d", r.code)
	}
	if n := len(a.fake.ServiceCalls()); n != 3 {
		t.Errorf("%d HA calls, want 3", n)
	}
}

// One-time use under parallel HTTP requests: exactly one reaches HA.
func TestE2EOneTimeConcurrent(t *testing.T) {
	a := startApp(t, appOpts{})
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}, "one_time": true})
	const n = 20 // under the action burst of 40
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { codes <- a.act(l, "light.guest_room", "light.turn_on", nil).code })
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		switch c {
		case 200:
			ok++
		case http.StatusConflict:
		default:
			t.Errorf("status %d", c)
		}
	}
	if calls := a.fake.ServiceCalls(); ok != 1 || len(calls) != 1 {
		t.Errorf("%d successes, %d HA calls", ok, len(calls))
	}
}

func TestE2ERefundOnHAError(t *testing.T) {
	a := startApp(t, appOpts{})
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}, "one_time": true})
	a.failHA.Store(true)
	if r := a.act(l, "light.guest_room", "light.turn_on", nil); r.code != http.StatusBadGateway || r.has("unavailable") {
		t.Fatalf("HA error: %d %s", r.code, r.body)
	}
	a.failHA.Store(false)
	if r := a.act(l, "light.guest_room", "light.turn_on", nil); r.code != 200 {
		t.Errorf("after refund: %d", r.code)
	}
	if r := a.act(l, "light.guest_room", "light.turn_on", nil); r.code != http.StatusConflict {
		t.Errorf("used: %d", r.code)
	}
	if n := len(a.fake.FiredEvents()); n != 1 {
		t.Errorf("%d action events, want 1", n)
	}
}

func TestE2ENetworkLocal(t *testing.T) {
	office := map[string]any{"templates": []string{"office"}}
	try := func(a *app, l model.Link, headers map[string]string) int {
		return a.do(call{method: "POST", url: a.guest + "/g/" + l.Slug + "/act", headers: headers,
			body: map[string]any{"entity_id": "switch.office", "action": "switch.turn_on"}}).code
	}
	lan := func(o *config.Options) {
		o.Settings.LocalAccess.Enabled = true
		o.Settings.LocalAccess.Networks = []string{"127.0.0.0/8"}
	}

	// Local access off: the stored local rule denies, even from the LAN.
	off := startApp(t, appOpts{options: func(o *config.Options) { o.Settings.LocalAccess.Networks = []string{"127.0.0.0/8"} }})
	if c := try(off, off.newLink(office), nil); c != 403 {
		t.Errorf("local off: %d", c)
	}
	// On, but the peer is a trusted proxy that sent no client: never local.
	proxy := startApp(t, appOpts{options: func(o *config.Options) { lan(o); o.Settings.TrustedProxies = []string{"127.0.0.1"} }})
	pl := proxy.newLink(office)
	if c := try(proxy, pl, nil); c != 403 {
		t.Errorf("unresolved proxy client: %d", c)
	}
	if c := try(proxy, pl, map[string]string{"X-Forwarded-For": "203.0.113.9"}); c != 403 {
		t.Errorf("internet client via proxy: %d", c)
	}
	if c := try(proxy, pl, map[string]string{"X-Forwarded-For": "127.0.0.9"}); c != 200 {
		t.Errorf("LAN client via proxy: %d", c)
	}
	// On, and the peer itself is on the LAN.
	on := startApp(t, appOpts{options: lan})
	if c := try(on, on.newLink(office), nil); c != 200 {
		t.Errorf("LAN peer: %d", c)
	}
}

func TestE2ENearHome(t *testing.T) {
	a := startApp(t, appOpts{})
	l := a.newLink(map[string]any{"templates": []string{"entry"}})
	unlock := func(c ...*http.Cookie) int { return a.act(l, "lock.front_door", "lock.unlock", nil, c...).code }
	where := func(lat, lon, acc float64) reply {
		return a.do(call{method: "POST", url: a.guest + "/g/" + l.Slug + "/where", body: map[string]any{"lat": lat, "lon": lon, "accuracy": acc}})
	}
	if c := unlock(); c != 403 {
		t.Errorf("no cookie: %d", c)
	}
	if c := unlock(&http.Cookie{Name: "ga_near_" + l.ID, Value: "AQMAAAAAaUoAAAAAAAAAAQ"}); c != 403 {
		t.Errorf("forged cookie: %d", c)
	}
	far := where(home.lat+0.01, home.lon, 5) // about 1.1 km
	if far.code != 200 || far.has("distance") || far.has(fmt.Sprint(home.lat)) {
		t.Fatalf("far where: %d %s", far.code, far.body)
	}
	if c := unlock(named(far.cookies, "ga_near")); c != 403 {
		t.Errorf("valid cookie, too far: %d", c)
	}
	if r := where(home.lat, home.lon, 1500); r.code != 400 || named(r.cookies, "ga_near") != nil {
		t.Errorf("accuracy too large: %d", r.code)
	}
	near := where(home.lat+0.001, home.lon, 15)
	if c := unlock(named(near.cookies, "ga_near")); c != 200 {
		t.Errorf("near: %d", c)
	}
}

func TestE2ERateLimits(t *testing.T) {
	a := startApp(t, appOpts{options: func(o *config.Options) {
		o.Settings.TrustedProxies = []string{"127.0.0.1"}
		o.Settings.RateLimits.MissesPerHourPerIP = 2
		o.Settings.RateLimits.MissesPerHourGlobal = 5
	}})
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	from := func(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }
	miss := func(ip string) reply {
		return a.do(call{method: "GET", url: a.guest + "/g/not-a-real-link/state", headers: from(ip)})
	}
	for range 2 {
		if r := miss("203.0.113.1"); r.code != 404 {
			t.Fatalf("miss: %d", r.code)
		}
	}
	if r := miss("203.0.113.1"); r.code != 429 || r.header.Get("Retry-After") == "" {
		t.Errorf("per-IP miss cap: %d %q", r.code, r.header.Get("Retry-After"))
	}
	for _, ip := range []string{"203.0.113.2", "203.0.113.2", "203.0.113.3"} {
		miss(ip)
	}
	if r := miss("203.0.113.4"); r.code != 429 {
		t.Errorf("global miss cap: %d", r.code)
	}
	if r := a.do(call{method: "GET", url: a.guest + "/g/" + l.Slug + "/state", headers: from("203.0.113.5")}); r.code != 200 {
		t.Errorf("valid slug under the global cap: %d", r.code)
	}
	// The pre-auth limiter: burst 15 per IP.
	var last reply
	for range 16 {
		last = a.do(call{method: "GET", url: a.guest + "/g/" + l.Slug, headers: from("198.51.100.1")})
	}
	if last.code != 429 || last.header.Get("Retry-After") == "" {
		t.Errorf("pre-auth limit: %d", last.code)
	}
}

func TestE2ELimitsOff(t *testing.T) {
	a := startApp(t, appOpts{options: func(o *config.Options) {
		o.LimitsEnabled = false
		o.Settings.RateLimits.MissesPerHourPerIP = 1
		o.Settings.RateLimits.GuestBurst = 1
	}})
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	for range 5 {
		for _, u := range []string{"/g/not-a-real-link/state", "/g/" + l.Slug} {
			if r := a.do(call{method: "GET", url: a.guest + u}); r.code == 429 {
				t.Fatalf("%s: 429 with limits off", u)
			}
		}
	}
	var s struct {
		LimitsDisabled bool `json:"limits_disabled"`
	}
	_ = json.Unmarshal(a.adminJSON("GET", "/api/settings", nil).body, &s)
	if !s.LimitsDisabled {
		t.Error("settings do not report limits_disabled")
	}
}

func TestE2EPortSeparation(t *testing.T) {
	a := startApp(t, appOpts{})
	for _, p := range []string{"/api/links", "/api/settings", "/api/diagnostics", "/healthz"} {
		if r := a.do(call{method: "GET", url: a.guest + p}); r.code != 404 {
			t.Errorf("guest %s: %d", p, r.code)
		}
	}
	// An admin write without a JSON content type is refused.
	if r := a.do(call{method: "POST", url: a.admin + "/api/links", body: `{"name":"x"}`, ctype: "text/plain"}); r.code != http.StatusUnsupportedMediaType {
		t.Errorf("admin write as text/plain: %d", r.code)
	}
	// Loopback is not the ingress proxy.
	strict := startApp(t, appOpts{devAnyPeer: boolPtr(false)})
	if r := strict.do(call{method: "GET", url: strict.admin + "/api/links"}); r.code != 403 {
		t.Errorf("non-ingress peer: %d", r.code)
	}
	if err := checkHealthURL(strict.admin); err != nil {
		t.Errorf("health check: %v", err)
	}
}

func checkHealthURL(base string) error {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(base, "http://"))
	return checkHealth(":" + port)
}

func TestE2EOriginAndHeaders(t *testing.T) {
	a := startApp(t, appOpts{})
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	body := map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}
	for _, o := range []string{"https://evil.example", "-", "null"} {
		r := a.do(call{method: "POST", url: a.guest + "/g/" + l.Slug + "/act", body: body, origin: o})
		if r.code != 403 {
			t.Errorf("origin %q: %d", o, r.code)
		}
	}
	if n := len(a.fake.ServiceCalls()); n != 0 {
		t.Errorf("%d calls with a bad origin", n)
	}
	// Every guest response has the headers.
	var assetPath string
	for _, c := range []call{
		{method: "GET", url: a.guest + "/g/" + l.Slug},
		{method: "GET", url: a.guest + "/g/" + l.Slug + "/state"},
		{method: "GET", url: a.guest + "/g/not-a-real-link"},
		{method: "GET", url: a.guest + "/nothing"},
		{method: "POST", url: a.guest + "/g/" + l.Slug + "/act", body: body, origin: "https://evil.example"},
	} {
		r := a.do(c)
		csp := r.header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe-inline") ||
			r.header.Get("Referrer-Policy") != "no-referrer" || r.header.Get("Cache-Control") != "no-store" ||
			r.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s %s: headers %v", c.method, c.url, r.header)
		}
		if i := bytes.Index(r.body, []byte(`src="../assets/`)); i >= 0 {
			rest := r.body[i+len(`src="..`):]
			assetPath = string(rest[:bytes.IndexByte(rest, '"')])
		}
	}
	if assetPath == "" {
		t.Fatal("page names no asset")
	}
	r := a.do(call{method: "GET", url: a.guest + assetPath})
	if r.code != 200 || !strings.Contains(r.header.Get("Cache-Control"), "immutable") || r.header.Get("Content-Security-Policy") == "" {
		t.Errorf("asset %s: %d %v", assetPath, r.code, r.header)
	}
}

// /state and /events show no ungranted entity and no unlisted attribute.
func TestE2ENoLeaks(t *testing.T) {
	a := startApp(t, appOpts{})
	l := a.newLink(map[string]any{"templates": []string{"guest_room", "entry"}})
	st := a.state(l)
	for _, leak := range []string{"light.bedroom", "secret-user", "secret-pic", "user_id", "changed_by", "lock.guest_room_window"} {
		if st.has(leak) {
			t.Errorf("state leaks %q", leak)
		}
	}
	if !st.has(`"light.guest_room"`) || !st.has(`"needs_location":true`) {
		t.Errorf("state = %s", st.body)
	}

	req, _ := http.NewRequest("GET", a.guest+"/g/"+l.Slug+"/events", nil)
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	deadline := time.After(10 * time.Second)
	for {
		a.ha.Emit("state_changed", map[string]any{"entity_id": "light.bedroom",
			"new_state": map[string]any{"entity_id": "light.bedroom", "state": "off", "attributes": map[string]any{}}})
		a.ha.Emit("state_changed", map[string]any{"entity_id": "lock.front_door",
			"new_state": map[string]any{"entity_id": "lock.front_door", "state": "unlocked", "attributes": map[string]any{}}})
		a.ha.Emit("state_changed", map[string]any{"entity_id": "light.guest_room",
			"new_state": map[string]any{"entity_id": "light.guest_room", "state": "on",
				"attributes": map[string]any{"brightness": 99, "user_id": "secret-user"}}})
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stream closed")
			}
			if strings.Contains(line, "bedroom") || strings.Contains(line, "secret") || strings.Contains(line, "front_door") {
				t.Fatalf("stream leaks: %s", line)
			}
			if strings.Contains(line, `"state":"on"`) && strings.Contains(line, `"brightness":99`) {
				return
			}
		case <-deadline:
			t.Fatal("granted change never arrived")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestE2EParty(t *testing.T) {
	party := func(a *app, l model.Link) (bool, int) {
		var v struct {
			Party bool `json:"party"`
		}
		_ = json.Unmarshal(a.state(l).body, &v)
		return v.Party, a.do(call{method: "GET", url: a.guest + "/g/" + l.Slug + "/party"}).code
	}
	withMA := func(c ma.Client) appOpts {
		return appOpts{ma: c, options: func(o *config.Options) {
			o.Settings.MusicAssistant.URL = "http://ma:8095"
			o.MusicAssistantToken = "tok"
			o.MATimeout = 100 * time.Millisecond
		}}
	}
	// No MA configured: no party, and no MA call (the stub would fail the test).
	none := startApp(t, appOpts{ma: failMA{t}})
	if p, c := party(none, none.newLink(map[string]any{"templates": []string{"music"}})); p || c != 404 {
		t.Errorf("no MA: %v %d", p, c)
	}
	// MA times out.
	slow := startApp(t, withMA(blockMA{}))
	start := time.Now()
	if p, c := party(slow, slow.newLink(map[string]any{"templates": []string{"music"}})); !p || c != 503 || time.Since(start) > 3*time.Second {
		t.Errorf("MA timeout: %v %d in %v", p, c, time.Since(start))
	}
	// MA works, but the link has no party template.
	ok := startApp(t, withMA(urlMA("https://ma.example/join?c=1")))
	if p, c := party(ok, ok.newLink(map[string]any{"templates": []string{"guest_room"}})); p || c != 404 {
		t.Errorf("no party template: %v %d", p, c)
	}
	l := ok.newLink(map[string]any{"templates": []string{"music"}})
	if r := ok.do(call{method: "GET", url: ok.guest + "/g/" + l.Slug + "/party"}); r.code != 200 || !r.has("https://ma.example/join?c=1") {
		t.Errorf("party: %d %s", r.code, r.body)
	}
}

type failMA struct{ t *testing.T }

func (f failMA) PartyURL(context.Context) (string, error) {
	f.t.Error("MA called while not configured")
	return "", ma.ErrUnavailable
}

func (f failMA) CheckToken(context.Context) error {
	f.t.Error("MA called while not configured")
	return ma.ErrUnavailable
}

type blockMA struct{}

func (blockMA) CheckToken(ctx context.Context) error {
	<-ctx.Done()
	return ma.ErrUnavailable
}

func (blockMA) PartyURL(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ma.ErrUnavailable
}

type urlMA string

func (u urlMA) PartyURL(context.Context) (string, error) { return string(u), nil }
func (urlMA) CheckToken(context.Context) error           { return nil }

// A bad HMAC key stops startup; it is never replaced.
func TestE2EBadKeyStopsStartup(t *testing.T) {
	dataDir := t.TempDir()
	keyPath := filepath.Join(dataDir, "hmac.key")
	if err := os.WriteFile(keyPath, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	adminLn, _ := net.Listen("tcp", "127.0.0.1:0")
	guestLn, _ := net.Listen("tcp", "127.0.0.1:0")
	cfg := config.Config{DataDir: dataDir, ConfigDir: t.TempDir(), HAURL: "ws://127.0.0.1:1/api/websocket", HAToken: "t", Options: config.DefaultOptions()}
	err := run(context.Background(), cfg, adminLn, guestLn, options{Logger: slog.New(slog.DiscardHandler)})
	if err == nil || !strings.Contains(err.Error(), "hmac") {
		t.Errorf("run = %v", err)
	}
	if raw, _ := os.ReadFile(keyPath); string(raw) != "short" {
		t.Error("bad key was replaced")
	}
}

// HA down at startup must not stop the pages.
func TestE2EStartsWithoutHA(t *testing.T) {
	adminLn, _ := net.Listen("tcp", "127.0.0.1:0")
	guestLn, _ := net.Listen("tcp", "127.0.0.1:0")
	cfg := config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir(), HAURL: "ws://127.0.0.1:1/api/websocket", HAToken: "t",
		DevAdminAnyPeer: true, Options: config.DefaultOptions()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, adminLn, guestLn, options{Logger: slog.New(slog.DiscardHandler)}) }()
	admin := "http://" + adminLn.Addr().String()
	waitFor(t, "admin page", func() bool {
		resp, err := testClient.Get(admin + "/")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	if err := checkHealthURL(admin); err != nil {
		t.Errorf("health: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}

func TestCheckHealthDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := checkHealth(addr); err == nil {
		t.Error("checkHealth on a closed port: want error")
	}
	if err := checkHealth("no-port"); err == nil {
		t.Error("checkHealth with a bad LISTEN: want error")
	}
}
