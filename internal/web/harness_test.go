package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
	"github.com/igrybkov/ha-guest-access/internal/inventory"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/ma"
	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

var quietLog = slog.New(slog.DiscardHandler)

// home is HA's home location in the fake.
var home = model.Home{Lat: 41.8781, Lon: -87.6298}

// t0 is the harness clock's start. Links run from t0-1h to t0+24h.
var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// fakeHA is a house with a guest room, an entry, a bedroom the guest may
// not touch, an office switch for local-only tests, and music.
func fakeHA() *hatest.Fake {
	return &hatest.Fake{
		AreaList: []ha.Area{{AreaID: "guest_room", Name: "Guest room"}, {AreaID: "bedroom", Name: "Bedroom"}, {AreaID: "entry", Name: "Entry"}},
		DeviceList: []ha.Device{
			{ID: "dev-lamp", Name: "Guest lamp", AreaID: "guest_room"},
			{ID: "dev-lock", Name: "Front lock", AreaID: "entry"},
		},
		EntityList: []ha.Entity{
			{EntityID: "light.guest_room", DeviceID: "dev-lamp", Name: "Guest lamp"},
			{EntityID: "lock.guest_room_window", AreaID: "guest_room", Name: "Window lock"},
			{EntityID: "light.bedroom", AreaID: "bedroom", Name: "Bedroom"},
			{EntityID: "lock.front_door", DeviceID: "dev-lock", Name: "Front door"},
			{EntityID: "script.let_me_in", Name: "Let me in"},
			{EntityID: "switch.office", Name: "Office"},
			{EntityID: "media_player.living_room", Name: "Living room"},
		},
		StateList: []ha.State{
			{EntityID: "light.guest_room", State: "off", Attributes: map[string]any{
				"friendly_name": "Guest lamp", "brightness": 0, "min_color_temp_kelvin": 2000.0, "max_color_temp_kelvin": 6500.0,
				"user_id": "secret-user", "entity_picture": "/api/camera_proxy?token=secret",
			}},
			{EntityID: "lock.front_door", State: "locked", Attributes: map[string]any{"friendly_name": "Front door", "changed_by": "secret-user"}},
			{EntityID: "light.bedroom", State: "on", Attributes: map[string]any{"friendly_name": "Bedroom"}},
			{EntityID: "lock.guest_room_window", State: "locked", Attributes: map[string]any{"code_format": "number"}},
			{EntityID: "script.let_me_in", State: "off"},
			{EntityID: "switch.office", State: "off"},
			{EntityID: "media_player.living_room", State: "idle"},
		},
		Core: ha.Config{Latitude: home.Lat, Longitude: home.Lon},
	}
}

// testTemplates are the templates of every harness.
func testTemplates() []model.Template {
	return []model.Template{
		{ID: "guest_room", Name: "Guest room", Rules: []model.Rule{{
			Select:  model.Selector{Areas: []string{"guest_room"}},
			Actions: []string{"light.turn_on", "light.turn_off"},
		}}},
		{ID: "entry", Name: "Entry", Access: &model.Access{NearHomeM: 300}, Rules: []model.Rule{
			{Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock", "lock.lock"}},
			{Select: model.Selector{Entities: []string{"script.let_me_in"}}, Actions: []string{"script.run"},
				Variables: map[string]any{"source": "guest_access"}},
		}},
		{ID: "office", Name: "Office", Access: &model.Access{Network: model.NetworkLocal}, Rules: []model.Rule{{
			Select: model.Selector{Entities: []string{"switch.office"}}, Actions: []string{"switch.turn_on"},
		}}},
		{ID: "music", Name: "Music", Party: true, Rules: []model.Rule{{
			Select: model.Selector{Entities: []string{"media_player.living_room"}}, Actions: []string{"media_player.volume_set"},
		}}},
	}
}

// harness is a Server over a fake HA, real stores in temp dirs, and a
// clock the test moves.
type harness struct {
	t     testing.TB
	fake  *hatest.Fake
	srv   *Server
	guest http.Handler
	admin http.Handler
	links *links.Store
	tpl   *config.TemplatesStore

	mu  sync.Mutex
	now time.Time
}

type harnessOpt func(*config.Options, *Deps)

// newHarness builds a harness. Options start at the defaults with a
// public base URL; opts change them.
func newHarness(t testing.TB, opts ...harnessOpt) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &harness{t: t, fake: fakeHA(), now: t0}
	tpl := config.NewTemplatesStore(migrate.OpenConfig(t.TempDir(), quietLog))
	if err := tpl.Save(model.Templates{Version: 1, Templates: testTemplates()}); err != nil {
		t.Fatal(err)
	}
	h.tpl = tpl
	h.links = links.New(migrate.OpenData(t.TempDir(), quietLog), links.Options{Logger: quietLog, Now: h.clock})
	inv := inventory.New(h.fake, quietLog)
	if err := inv.Run(ctx); err != nil {
		t.Fatal(err)
	}
	o := config.DefaultOptions()
	o.PublicBaseURL = "https://guest.example.com"
	o.StreamCheck = 20 * time.Millisecond
	d := Deps{
		Templates: tpl, Links: h.links, Inventory: inv, HA: h.fake,
		Signer: access.NewSigner(bytes.Repeat([]byte{7}, 32)), Now: h.clock, Logger: quietLog,
	}
	for _, f := range opts {
		f(&o, &d)
	}
	d.Options = o
	h.srv = New(d)
	h.guest = h.srv.GuestHandler()
	h.admin = h.srv.AdminHandler()
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

// link makes a link over the given templates, valid from t0-1h to
// t0+24h.
func (h *harness) link(templates ...string) model.Link {
	h.t.Helper()
	l, err := h.links.Create(model.Link{
		Name: "Anna's visit", ValidFrom: t0.Add(-time.Hour), ValidUntil: t0.Add(24 * time.Hour), TemplateIDs: templates,
	}, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return l
}

// req is one guest request.
type req struct {
	method, path string
	body         any    // marshaled to JSON unless it is a string
	peer         string // RemoteAddr; default 203.0.113.10:5000
	origin       string // default http://guest.test; "-" sends none
	ctype        string // default application/json for bodies
	headers      map[string]string
	cookies      []*http.Cookie
}

func (h *harness) do(handler http.Handler, r req) *httptest.ResponseRecorder {
	h.t.Helper()
	var body io.Reader
	switch b := r.body.(type) {
	case nil:
	case string:
		body = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		body = bytes.NewReader(raw)
	}
	hr := httptest.NewRequest(r.method, "http://guest.test"+r.path, body)
	hr.RemoteAddr = r.peer
	if hr.RemoteAddr == "" {
		hr.RemoteAddr = "203.0.113.10:5000"
	}
	if r.body != nil {
		ct := r.ctype
		if ct == "" {
			ct = "application/json"
		}
		hr.Header.Set("Content-Type", ct)
	}
	switch r.origin {
	case "":
		hr.Header.Set("Origin", "http://guest.test")
	case "-":
	default:
		hr.Header.Set("Origin", r.origin)
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	for _, c := range r.cookies {
		hr.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, hr)
	return rec
}

func (h *harness) g(r req) *httptest.ResponseRecorder { return h.do(h.guest, r) }

// a sends an admin request from the ingress proxy.
func (h *harness) a(r req) *httptest.ResponseRecorder {
	if r.peer == "" {
		r.peer = "172.30.32.2:40000"
	}
	return h.do(h.admin, r)
}

// act posts an action for link l.
func (h *harness) act(l model.Link, entity, action string, data map[string]any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.g(req{method: "POST", path: "/g/" + l.Slug + "/act",
		body: map[string]any{"entity_id": entity, "action": action, "data": data}, cookies: cookies})
}

// cookie returns the cookie of name base set by rec, for any link: the
// name is base + "_" + a link id (access.LinkCookie).
func cookie(rec *httptest.ResponseRecorder, base string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if id, ok := strings.CutPrefix(c.Name, base+"_"); ok && !strings.Contains(id, "_") {
			return c
		}
	}
	return nil
}

func decode[T any](t testing.TB, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

// stubMA is an ma.Client.
type stubMA struct {
	url   string
	err   error
	block bool
}

func (m stubMA) CheckToken(ctx context.Context) error {
	_, err := m.PartyURL(ctx)
	return err
}

func (m stubMA) PartyURL(ctx context.Context) (string, error) {
	if m.block {
		<-ctx.Done()
		return "", ma.ErrUnavailable
	}
	return m.url, m.err
}

func withMA(c ma.Client) harnessOpt {
	return func(o *config.Options, d *Deps) {
		o.Settings.MusicAssistant.URL = "http://ma:8095"
		o.MusicAssistantToken = "tok"
		d.NewMA = func(string, string, time.Duration) (ma.Client, error) { return c, nil }
	}
}
