package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
	"github.com/igrybkov/ha-guest-access/internal/inventory"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// clip cuts to at most n bytes and never splits a rune.
func TestClip(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"", 4, ""},
		{"abc", 4, "abc"},
		{"abcd", 4, "abcd"},
		{"abcde", 4, "abcd"},
		{"abéz", 3, "ab"},        // é is 2 bytes and would cross 3
		{"abéz", 4, "abé"},       // fits
		{"a\U0001F600b", 3, "a"}, // a 4-byte rune
		{"\xff\xff\xff\xff\xff", 3, "\xff\xff\xff"},
	} {
		got := clip(tc.in, tc.n)
		if got != tc.want || len(got) > tc.n {
			t.Errorf("clip(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
		if utf8.ValidString(tc.in) && !utf8.ValidString(got) {
			t.Errorf("clip(%q, %d) split a rune: %q", tc.in, tc.n, got)
		}
	}
}

// Diagnostics keep at most maxDiagValue bytes of a route or a header,
// and the audit route of a guest request is cut the same way.
func TestDiagnosticsClipValues(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("x", 5000)
	h.g(req{method: "GET", path: "/api/" + long, headers: map[string]string{"X-Forwarded-For": long, "Forwarded": long}})
	list := h.srv.diag.list()
	if len(list) == 0 {
		t.Fatal("no entry")
	}
	e := list[0]
	if len(e.Route) != maxDiagValue || !strings.HasPrefix(e.Route, "/api/xx") {
		t.Errorf("route has %d bytes: %.20q", len(e.Route), e.Route)
	}
	for k, v := range e.Headers {
		if len(v) != maxDiagValue {
			t.Errorf("%s has %d bytes", k, len(v))
		}
	}
	if got := routeLabel("/api/" + long); len(got) != maxDiagValue {
		t.Errorf("routeLabel keeps %d bytes", len(got))
	}
}

// With no home in HA (0, 0), /where refuses and sets no cookie, from any
// coordinates. Once HA has a home, the same request works.
func TestWhereNeedsHome(t *testing.T) {
	h := newHarness(t)
	h.fake.Core = ha.Config{}
	l := h.link("entry")
	for _, c := range [][2]float64{{0, 0}, {home.Lat, home.Lon}, {0.0001, -0.0001}} {
		rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/where", body: map[string]any{"lat": c[0], "lon": c[1], "accuracy": 5}})
		if rec.Code != http.StatusConflict || cookie(rec, nearCookie) != nil {
			t.Errorf("where %v with no home: %d", c, rec.Code)
		}
	}
	if c := h.act(l, "lock.front_door", "lock.unlock", nil).Code; c != http.StatusForbidden {
		t.Errorf("unlock: %d", c)
	}
	h.fake.Core = ha.Config{Latitude: home.Lat, Longitude: home.Lon}
	h.srv.deps.Inventory.(*inventory.Cache).Invalidate()
	rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/where", body: map[string]any{"lat": home.Lat, "lon": home.Lon, "accuracy": 5}})
	if rec.Code != http.StatusOK || cookie(rec, nearCookie) == nil {
		t.Fatalf("where with a home: %d %s", rec.Code, rec.Body)
	}
}

// The stream counter keeps both caps, and a release frees both.
func TestStreamCounterCaps(t *testing.T) {
	c := newStreamCounter()
	const a, b = "198.51.100.1", "198.51.100.2"
	for i := range 2 {
		if !c.acquire("l", a, 3, 2) {
			t.Fatalf("slot %d refused", i)
		}
	}
	if c.acquire("l", a, 3, 2) {
		t.Error("third stream of one client on one link")
	}
	if !c.acquire("m", a, 3, 2) {
		t.Error("the per-client cap leaks into another link")
	}
	if !c.acquire("l", b, 3, 2) {
		t.Error("second client refused")
	}
	if c.acquire("l", b, 3, 2) {
		t.Error("per-link cap not kept")
	}
	c.release("l", a)
	if !c.acquire("l", b, 3, 2) {
		t.Error("release did not free the link slot")
	}
	c.release("l", a)
	c.release("l", b)
	c.release("l", b)
	c.release("m", a)
	if len(c.open) != 0 || len(c.byClient) != 0 {
		t.Errorf("not empty after releases: %v %v", c.open, c.byClient)
	}
	// Zero caps mean none.
	for range 10 {
		if !c.acquire("l", a, 0, 0) {
			t.Fatal("zero caps refused")
		}
	}
}

// Requests from many addresses of one /64 share the per-IP limiters.
// Another /64 has its own budget.
func TestIPv6PrefixKeysLimiters(t *testing.T) {
	h := newHarness(t)
	limited := false
	for i := range 40 {
		if h.g(req{method: "GET", path: "/nothing", peer: fmt.Sprintf("[2001:db8:1:2:%x::1]:5000", i+1)}).Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("one /64 was never limited")
	}
	if c := h.g(req{method: "GET", path: "/nothing", peer: "[2001:db8:1:3::1]:5000"}).Code; c != http.StatusNotFound {
		t.Errorf("another /64: %d", c)
	}
	// With a /128 prefix each address has its own bucket.
	h = newHarness(t, func(o *config.Options, _ *Deps) { o.Settings.RateLimits.IPv6Prefix = 128 })
	for i := range 40 {
		if c := h.g(req{method: "GET", path: "/nothing", peer: fmt.Sprintf("[2001:db8:1:2::%x]:5000", i+1)}).Code; c != http.StatusNotFound {
			t.Fatalf("prefix 128, address %d: %d", i, c)
		}
	}
}

// The HA event and the log name the full address, not the prefix.
func TestIPv6FullAddressInEvents(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	rec := h.g(req{method: "POST", path: "/g/" + l.Slug + "/act", peer: "[2001:db8:1:2::abcd]:5000",
		body: map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("act: %d", rec.Code)
	}
	ev := h.fake.FiredEvents()
	if len(ev) != 1 || ev[0].Data["ip"] != "2001:db8:1:2::abcd" {
		t.Errorf("events = %+v", ev)
	}
}

// One client gets at most streams.max_per_ip streams on a link. Another
// client still gets in.
func TestStreamsPerClient(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) { o.StreamMaxPerIP = 2 })
	l := h.link("guest_room")
	h.srv.streams.acquire(l.ID, "203.0.113.10", 0, 0)
	h.srv.streams.acquire(l.ID, "203.0.113.10", 0, 0)
	rec := h.g(req{method: "GET", path: "/g/" + l.Slug + "/events", peer: "203.0.113.10:1"})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("third stream of one client: %d", rec.Code)
	}
}

// A one-time use is given back only for an error result from HA. A lost
// connection or a timeout keeps it spent.
func TestOneTimeRefundOnlyOnRefusal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		refunded bool
	}{
		{"error result", &ha.Error{Code: "home_assistant_error", Message: "x"}, true},
		{"wrapped error result", fmt.Errorf("light.turn_off: %w", &ha.Error{Code: "service_validation_error"}), true},
		{"connection lost", fmt.Errorf("light.turn_off: %w", ha.ErrDisconnected), false},
		{"timeout", fmt.Errorf("light.turn_off: %w", context.DeadlineExceeded), false},
		{"canceled", context.Canceled, false},
		{"other", errors.New("write: broken pipe"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.Respond = func(hatest.ServiceCall) (json.RawMessage, error) { return nil, tc.err }
			l, err := h.links.Create(model.Link{Name: "Once", ValidUntil: t0.Add(time.Hour), TemplateIDs: []string{"guest_room"}, OneTime: true}, "")
			if err != nil {
				t.Fatal(err)
			}
			if c := h.act(l, "light.guest_room", "light.turn_off", nil).Code; c != http.StatusBadGateway {
				t.Fatalf("act: %d", c)
			}
			got, _ := h.links.Get(l.ID)
			if refunded := len(got.Used) == 0 && got.UseCount == 0; refunded != tc.refunded {
				t.Errorf("refunded = %v, want %v (%+v)", refunded, tc.refunded, got.Used)
			}
		})
	}
}

// A hand-written link that does not validate reads as not found on every
// guest route, logs one warning however often it is hit, and shows in the
// admin API with its problems.
func TestBrokenLinkNotServed(t *testing.T) {
	const slug = "handwrittenslug0009"
	log := &lockedBuffer{}
	bad := handLink(slug, model.Rule{
		Select: model.Selector{Entities: []string{"light.guest_room"}}, Actions: []string{"light.turn_on"},
		Access: &model.Access{Network: "LOCAL"},
	})
	dir := t.TempDir()
	raw, _ := yaml.Marshal(model.Links{Version: migrate.DataCurrent, Links: []model.Link{bad}})
	if err := os.WriteFile(filepath.Join(dir, migrate.LinksFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := links.New(migrate.OpenData(dir, quietLog), links.Options{Logger: quietLog})
	h := newHarness(t, withLog(log), func(_ *config.Options, d *Deps) { d.Links = store })

	for _, r := range []req{
		{method: "GET", path: "/g/" + slug},
		{method: "GET", path: "/g/" + slug + "/state"},
		{method: "GET", path: "/g/" + slug + "/events"},
		{method: "POST", path: "/g/" + slug + "/act", body: map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}},
		{method: "POST", path: "/g/" + slug + "/password", body: map[string]any{"password": "x"}},
		{method: "POST", path: "/g/" + slug + "/device/request", body: map[string]any{}},
	} {
		if rec := h.g(r); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d", r.method, r.path, rec.Code)
		}
	}
	if n := len(h.fake.ServiceCalls()); n != 0 {
		t.Errorf("%d HA calls", n)
	}
	if n := strings.Count(log.String(), "does not validate"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, log.String())
	}
	views := decode[struct {
		Links []linkView `json:"links"`
	}](t, h.a(req{method: "GET", path: "/api/links"}))
	if len(views.Links) != 1 || len(views.Links[0].Problems) == 0 || views.Links[0].Problems[0].Path != "extra[0].access.network" {
		t.Fatalf("admin list: %+v", views.Links)
	}
}

// A good link has an empty problems list in the admin API.
func TestGoodLinkHasNoProblems(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	v := decode[linkView](t, h.a(req{method: "GET", path: "/api/links/" + l.ID}))
	if v.Problems == nil || len(v.Problems) != 0 {
		t.Errorf("problems = %#v", v.Problems)
	}
}

// An open event stream ends once a hand edit breaks its link.
func TestStreamEndsWhenLinkBreaks(t *testing.T) {
	const slug = "handwrittenslug0010"
	good := handLink(slug, model.Rule{Select: model.Selector{Entities: []string{"light.guest_room"}}, Actions: []string{"light.turn_on"}})
	dir := t.TempDir()
	write := func(l model.Link) {
		raw, _ := yaml.Marshal(model.Links{Version: migrate.DataCurrent, Links: []model.Link{l}})
		if err := os.WriteFile(filepath.Join(dir, migrate.LinksFile), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(good)
	store := links.New(migrate.OpenData(dir, quietLog), links.Options{Logger: quietLog})
	h := newHarness(t, func(_ *config.Options, d *Deps) { d.Links = store })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := httptest.NewRequest("GET", "http://guest.test/g/"+slug+"/events", nil).WithContext(ctx)
	r.RemoteAddr = "203.0.113.10:5000"
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.guest.ServeHTTP(rec, r); close(done) }()
	time.Sleep(100 * time.Millisecond)
	bad := good
	bad.Extra[0].Access = &model.Access{NearHomeM: -1}
	// A new mtime, so the store sees the change.
	time.Sleep(10 * time.Millisecond)
	write(bad)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("stream did not end")
	}
	if !strings.Contains(rec.Body.String(), "event: end") {
		t.Errorf("stream = %s", rec.Body)
	}
}

// A stray cookie with the same name, before or after the real one,
// never locks a guest out. Session and location cookies work like the
// device cookie: any valid one counts.
func TestDuplicateCookiesDoNotLockOut(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room", "entry")
	hash, _ := access.HashPassword("pw")
	if _, err := h.links.AddPassword(l.ID, "Anna", hash); err != nil {
		t.Fatal(err)
	}
	ses := cookie(h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": "pw"}}), sessionCookie)
	if ses == nil {
		t.Fatal("no session")
	}
	junk := &http.Cookie{Name: ses.Name, Value: "junk"}
	for name, cs := range map[string][]*http.Cookie{"junk first": {junk, ses}, "junk last": {ses, junk}} {
		if c := h.act(l, "light.guest_room", "light.turn_on", nil, cs...).Code; c != http.StatusOK {
			t.Errorf("session, %s: %d", name, c)
		}
	}
	if c := h.act(l, "light.guest_room", "light.turn_on", nil, junk).Code; c != http.StatusUnauthorized {
		t.Errorf("junk alone: %d", c)
	}
	near := cookie(h.g(req{method: "POST", path: "/g/" + l.Slug + "/where", cookies: []*http.Cookie{ses},
		body: map[string]any{"lat": home.Lat, "lon": home.Lon, "accuracy": 5}}), nearCookie)
	if near == nil {
		t.Fatal("no location cookie")
	}
	far := &http.Cookie{Name: near.Name, Value: h.srv.deps.Signer.SignNear(l.ID, 50_000, t0.Add(time.Hour))}
	nearJunk := &http.Cookie{Name: near.Name, Value: "junk"}
	for name, cs := range map[string][]*http.Cookie{
		"junk first": {ses, nearJunk, near}, "junk last": {ses, near, nearJunk}, "far first": {ses, far, near},
	} {
		if c := h.act(l, "lock.front_door", "lock.unlock", nil, cs...).Code; c != http.StatusOK {
			t.Errorf("location, %s: %d", name, c)
		}
	}
	if c := h.act(l, "lock.front_door", "lock.unlock", nil, ses, far).Code; c != http.StatusForbidden {
		t.Errorf("far alone: %d", c)
	}
}

// Local access with no trusted proxy shows a warning in the diagnostics.
func TestLocalAccessWithoutProxyWarning(t *testing.T) {
	has := func(h *harness) bool {
		d := decode[diagnosticsView](t, h.a(req{method: "GET", path: "/api/diagnostics"}))
		for _, w := range d.Warnings {
			if w == localNoProxyWarning {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		name    string
		on      bool
		proxies []string
		want    bool
	}{
		{"off, no proxy", false, nil, false},
		{"on, no proxy", true, nil, true},
		{"on, with proxy", true, []string{"192.0.2.50"}, false},
	} {
		h := newHarness(t, func(o *config.Options, _ *Deps) {
			o.Settings.LocalAccess.Enabled = tc.on
			o.Settings.TrustedProxies = tc.proxies
		})
		if got := has(h); got != tc.want {
			t.Errorf("%s: warning %v, want %v", tc.name, got, tc.want)
		}
		if got := len(OptionWarnings(h.srv.deps.Options)) > 0; got != tc.want {
			t.Errorf("%s: OptionWarnings %v", tc.name, got)
		}
	}
}

// A custom slug shorter than links.custom_slug_warn_len gets a warning on
// create, on rotate and in the link view. At or above it, and for a
// random slug, there is none.
func TestShortCustomSlugWarning(t *testing.T) {
	h := newHarness(t)
	warnLen := h.srv.deps.Options.CustomSlugWarnLen
	if warnLen != 16 {
		t.Fatalf("default warn length = %d", warnLen)
	}
	short, long := strings.Repeat("a", warnLen-1), strings.Repeat("b", warnLen)
	create := func(slug string) linkView {
		body := map[string]any{"name": "L", "valid_until": t0.Add(time.Hour), "templates": []string{"guest_room"}}
		if slug != "" {
			body["slug"] = slug
		}
		rec := h.a(req{method: "POST", path: "/api/links", body: body})
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("create %q: %d %s", slug, rec.Code, rec.Body)
		}
		return decode[linkView](t, rec)
	}
	if v := create(short); v.SlugWarning != shortSlugWarning {
		t.Errorf("short slug on create: %q", v.SlugWarning)
	}
	if v := create(long); v.SlugWarning != "" {
		t.Errorf("slug at the threshold: %q", v.SlugWarning)
	}
	v := create("")
	if v.SlugWarning != "" {
		t.Errorf("random slug: %q", v.SlugWarning)
	}
	rot := decode[linkView](t, h.a(req{method: "POST", path: "/api/links/" + v.ID + "/rotate", body: map[string]any{"slug": "c" + short[1:]}}))
	if rot.SlugWarning != shortSlugWarning {
		t.Errorf("short slug on rotate: %q", rot.SlugWarning)
	}
	if got := decode[linkView](t, h.a(req{method: "GET", path: "/api/links/" + v.ID})); got.SlugWarning != shortSlugWarning {
		t.Errorf("link view: %q", got.SlugWarning)
	}
	// The short slug still works for guests.
	if c := h.g(req{method: "GET", path: "/g/" + rot.Slug + "/state"}).Code; c != http.StatusOK {
		t.Errorf("short slug state: %d", c)
	}
}
