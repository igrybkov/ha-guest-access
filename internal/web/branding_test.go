package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// Text that only the link holds. A response before the password and
// device checks must never carry any of it.
const (
	secretName    = "Zebra-Name-7Q"
	secretTitle   = "Zebra-Title-7Q"
	secretWelcome = "Zebra-Welcome-7Q"
)

func withGlobalBranding(o *config.Options, _ *Deps) {
	o.Settings.Guest.Title = "Global title"
	o.Settings.Guest.Welcome = "Global welcome"
}

// brandLink makes a guest room link with the secret name and edit
// applied.
func (h *harness) brandLink(edit func(*model.Link)) model.Link {
	h.t.Helper()
	d := model.Link{Name: secretName, ValidFrom: t0.Add(-time.Hour), ValidUntil: t0.Add(24 * time.Hour), TemplateIDs: []string{"guest_room"}}
	if edit != nil {
		edit(&d)
	}
	l, err := h.links.Create(d, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return l
}

func TestStateBranding(t *testing.T) {
	for _, tc := range []struct {
		name           string
		edit           func(*model.Link)
		title, welcome string
	}{
		{"name never shows; global title and welcome", nil, "Global title", "Global welcome"},
		{"own title and welcome", func(l *model.Link) { l.GuestTitle, l.GuestWelcome = secretTitle, secretWelcome }, secretTitle, secretWelcome},
		{"own welcome only", func(l *model.Link) { l.GuestWelcome = "Line 1\nLine 2" }, "Global title", "Line 1\nLine 2"},
		{"blank title falls back", func(l *model.Link) { l.GuestTitle, l.GuestWelcome = "   ", " " }, "Global title", "Global welcome"},
		{"no name falls back to the global title", func(l *model.Link) { l.Name, l.GuestTitle = "", "" }, "Global title", "Global welcome"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, withGlobalBranding)
			l := h.brandLink(tc.edit)
			v := decode[stateView](t, h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"}))
			if v.Guest.Title != tc.title || v.Guest.Welcome != tc.welcome {
				t.Errorf("guest = %q / %q, want %q / %q", v.Guest.Title, v.Guest.Welcome, tc.title, tc.welcome)
			}
			if v.Guest.SliderIntervalMS != model.DefaultSliderIntervalMS {
				t.Errorf("other guest settings lost: %+v", v.Guest)
			}
		})
	}
}

// noLinkText fails if rec carries the link's name, title or welcome in
// its body or headers.
func noLinkText(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	all := rec.Body.String() + fmt.Sprint(rec.Result().Header)
	for _, s := range []string{secretName, secretTitle, secretWelcome} {
		if strings.Contains(all, s) {
			t.Errorf("%s (%d) leaks %q: %s", what, rec.Code, s, rec.Body)
		}
	}
}

func ownText(l *model.Link) { l.GuestTitle, l.GuestWelcome = secretTitle, secretWelcome }

func TestBrandingHiddenBeforePassword(t *testing.T) {
	h := newHarness(t, withGlobalBranding)
	l := h.brandLink(ownText)
	hash, _ := access.HashPassword("open sesame")
	if _, err := h.links.AddPassword(l.ID, "Anna", hash); err != nil {
		t.Fatal(err)
	}
	base := "/g/" + l.Slug
	noLinkText(t, "page", h.g(req{method: "GET", path: base}))
	if rec := h.g(req{method: "GET", path: base + "/state"}); rec.Code != 401 {
		t.Errorf("state without session: %d", rec.Code)
	} else {
		noLinkText(t, "state without session", rec)
	}
	if rec := h.g(req{method: "GET", path: base + "/events"}); rec.Code != 401 {
		t.Errorf("events without session: %d", rec.Code)
	} else {
		noLinkText(t, "events without session", rec)
	}
	noLinkText(t, "act without session", h.act(l, "light.guest_room", "light.turn_on", nil))
	noLinkText(t, "where without session", h.g(req{method: "POST", path: base + "/where", body: map[string]any{}}))
	noLinkText(t, "party without session", h.g(req{method: "GET", path: base + "/party"}))
	noLinkText(t, "wrong password", h.g(req{method: "POST", path: base + "/password", body: map[string]any{"password": "nope"}}))
	rec := h.g(req{method: "POST", path: base + "/password", body: map[string]any{"password": "open sesame"}})
	noLinkText(t, "right password", rec)
	c := cookie(rec, sessionCookie)
	if c == nil {
		t.Fatalf("login: %d", rec.Code)
	}
	v := decode[stateView](t, h.g(req{method: "GET", path: base + "/state", cookies: []*http.Cookie{c}}))
	if v.Guest.Title != secretTitle || v.Guest.Welcome != secretWelcome {
		t.Errorf("after login: %+v", v.Guest)
	}
}

func TestBrandingHiddenBeforeDeviceApproval(t *testing.T) {
	h := newHarness(t, withGlobalBranding)
	l := h.brandLink(func(l *model.Link) { ownText(l); l.Approval = model.Approval{Manual: true} })
	j := jar{}
	noLinkText(t, "page", h.g(req{method: "GET", path: "/g/" + l.Slug}))
	if rec := h.dev(j, l, "GET", "state", nil); rec.Code != 403 {
		t.Errorf("state before request: %d", rec.Code)
	} else {
		noLinkText(t, "state before request", rec)
	}
	noLinkText(t, "device request", h.dev(j, l, "POST", "device/request", map[string]any{}))
	noLinkText(t, "device status", h.dev(j, l, "GET", "device/status", nil))
	if rec := h.dev(j, l, "GET", "state", nil); rec.Code != 403 {
		t.Errorf("state while pending: %d", rec.Code)
	} else {
		noLinkText(t, "state while pending", rec)
	}
	noLinkText(t, "events while pending", h.dev(j, l, "GET", "events", nil))
	h.approveNext(l, true)
	h.dev(j, l, "GET", "device/status", nil)
	v := decode[stateView](t, h.dev(j, l, "GET", "state", nil))
	if v.Guest.Title != secretTitle || v.Guest.Welcome != secretWelcome {
		t.Errorf("after approval: %+v", v.Guest)
	}
}

func TestBrandingHiddenWhenInactive(t *testing.T) {
	h := newHarness(t, withGlobalBranding)
	later := h.brandLink(func(l *model.Link) {
		ownText(l)
		l.ValidFrom, l.ValidUntil = t0.Add(time.Hour), t0.Add(2*time.Hour)
	})
	revoked := h.brandLink(ownText)
	if err := h.links.Revoke(revoked.ID); err != nil {
		t.Fatal(err)
	}
	for name, l := range map[string]model.Link{"scheduled": later, "revoked": revoked} {
		base := "/g/" + l.Slug
		noLinkText(t, name+" page", h.g(req{method: "GET", path: base}))
		if rec := h.g(req{method: "GET", path: base + "/state"}); rec.Code != 410 {
			t.Errorf("%s state: %d", name, rec.Code)
		} else {
			noLinkText(t, name+" state", rec)
		}
		noLinkText(t, name+" events", h.g(req{method: "GET", path: base + "/events"}))
	}
}

// The page shell carries the global title and welcome, escaped, and
// nothing of the link.
func TestPageShellBranding(t *testing.T) {
	for _, tc := range []struct {
		name           string
		title, welcome string
		want, notWant  []string
	}{
		{"global text", "Global title", "Global welcome",
			[]string{"<title>Global title</title>", `<h1 id="title">Global title</h1>`, `<p id="welcome">Global welcome</p>`}, []string{"{{"}},
		{"no welcome", "Casa", "",
			[]string{"<title>Casa</title>", `<p id="welcome" hidden></p>`}, []string{"{{"}},
		{"empty title", "", "",
			[]string{"<title>Welcome</title>"}, nil},
		{"escaped", `<b>&"x"`, `<script>alert(1)</script>`,
			[]string{"<title>&lt;b&gt;&amp;&#34;x&#34;</title>", "&lt;script&gt;alert(1)&lt;/script&gt;"}, []string{"<b>", "<script>alert"}},
		{"no recursion", "{{WELCOME}}", "w",
			[]string{"<title>{{WELCOME}}</title>"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(o *config.Options, _ *Deps) {
				o.Settings.Guest.Title, o.Settings.Guest.Welcome = tc.title, tc.welcome
			})
			l := h.brandLink(ownText)
			rec := h.g(req{method: "GET", path: "/g/" + l.Slug})
			body := rec.Body.String()
			for _, s := range tc.want {
				if !strings.Contains(body, s) {
					t.Errorf("page lacks %q", s)
				}
			}
			for _, s := range tc.notWant {
				if strings.Contains(body, s) {
					t.Errorf("page has %q", s)
				}
			}
			noLinkText(t, "page", rec)
		})
	}
}

// The admin API returns the guest text, and a save of what it returned
// keeps it.
func TestAdminLinkGuestText(t *testing.T) {
	h := newHarness(t)
	draft := map[string]any{"name": "Anna", "valid_until": t0.Add(48 * time.Hour), "templates": []string{"guest_room"},
		"guest_title": "Hi Anna", "guest_welcome": "Line 1\nLine 2"}
	rec := h.a(req{method: "POST", path: "/api/links", body: draft})
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := decode[linkView](t, rec).ID
	got := decode[map[string]any](t, h.a(req{method: "GET", path: "/api/links/" + id}))
	if got["guest_title"] != "Hi Anna" || got["guest_welcome"] != "Line 1\nLine 2" {
		t.Fatalf("get: %v %v", got["guest_title"], got["guest_welcome"])
	}
	if rec := h.a(req{method: "PUT", path: "/api/links/" + id, body: draft}); rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if l, _ := h.links.Get(id); l.GuestTitle != "Hi Anna" || l.GuestWelcome != "Line 1\nLine 2" {
		t.Errorf("after put: %q %q", l.GuestTitle, l.GuestWelcome)
	}
	draft["guest_title"] = "a\nb"
	if rec := h.a(req{method: "PUT", path: "/api/links/" + id, body: draft}); rec.Code != 400 || !strings.Contains(rec.Body.String(), "guest_title") {
		t.Errorf("bad title: %d %s", rec.Code, rec.Body)
	}
}
