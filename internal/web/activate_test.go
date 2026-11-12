package web

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// Activate takes a URL choice and a window choice in one call. Each
// combination ends with the link on, the expected slug and window, and
// one audit record that names what changed and never the slug.
func TestAdminActivateCombinations(t *testing.T) {
	saved := t0.Add(24 * time.Hour)
	preset := t0.Add(7 * 24 * time.Hour)
	from, until := t0.Add(2*time.Hour), t0.Add(5*time.Hour)
	for _, url := range []struct {
		name string
		body map[string]any
		new  bool
		slug string // a custom slug the link must get
	}{
		{name: "keep url", body: map[string]any{}},
		{name: "random url", body: map[string]any{"rotate": true}, new: true},
		{name: "custom url", body: map[string]any{"rotate": true, "slug": "a-custom-slug-for-anna"}, new: true, slug: "a-custom-slug-for-anna"},
	} {
		for _, win := range []struct {
			name             string
			body             map[string]any
			wantFrom, wantTo time.Time
			changed          []string
		}{
			{name: "keep window", wantFrom: t0.Add(-time.Hour), wantTo: saved},
			{name: "preset", body: map[string]any{"valid_from": nil, "valid_until": preset}, wantTo: preset, changed: []string{"valid_from", "valid_until"}},
			{name: "custom", body: map[string]any{"valid_from": from, "valid_until": until}, wantFrom: from, wantTo: until, changed: []string{"valid_from", "valid_until"}},
		} {
			for _, start := range []string{"inactive", "active"} {
				t.Run(url.name+"/"+win.name+"/"+start, func(t *testing.T) {
					ah := newAuditHarness(t)
					l := ah.link("guest_room")
					if start == "inactive" {
						if _, err := ah.links.Deactivate(l.ID); err != nil {
							t.Fatal(err)
						}
					}
					body := map[string]any{}
					for k, v := range url.body {
						body[k] = v
					}
					for k, v := range win.body {
						body[k] = v
					}
					rec := ah.a(req{method: "POST", path: "/api/links/" + l.ID + "/activate", body: body})
					if rec.Code != 200 {
						t.Fatalf("activate: %d %s", rec.Code, rec.Body)
					}
					v := decode[linkView](t, rec)
					if v.Inactive || !v.ValidFrom.Equal(win.wantFrom) || !v.ValidUntil.Equal(win.wantTo) {
						t.Errorf("window: %+v", v.Link)
					}
					if url.new == (v.Slug == l.Slug) || url.slug != "" && v.Slug != url.slug {
						t.Errorf("slug %q, before %q", v.Slug, l.Slug)
					}
					if !strings.HasSuffix(v.URL, "/g/"+v.Slug) {
						t.Errorf("url %q", v.URL)
					}
					want := slices.Clone(win.changed)
					if start == "inactive" {
						want = append(want, "inactive")
					}
					if url.new {
						want = append(want, "url")
					}
					var got []string
					for _, r := range ah.records() {
						if r.Event == "admin.link.activate" {
							got = append(got, r.DataKeys...)
							if r.LinkID != l.ID || r.LinkName != l.Name || r.Result != "ok" {
								t.Errorf("record %+v", r)
							}
						}
					}
					if !slices.Equal(got, want) {
						t.Errorf("audit keys %v, want %v", got, want)
					}
					raw := ah.log.String()
					for _, s := range []string{l.Slug, v.Slug} {
						if strings.Contains(raw, s) {
							t.Errorf("slug %q in the log", s)
						}
					}
					if c := ah.act(model.Link{Slug: v.Slug}, "light.guest_room", "light.turn_on", nil).Code; c != 200 && win.name != "custom" {
						t.Errorf("guest act: %d", c)
					}
				})
			}
		}
	}
}

// Activate refuses a window that does not end in the future, or that
// does not pass a save's checks, and changes nothing.
func TestAdminActivateRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*harness, model.Link)
		body   any
		status int
		msg    string
	}{
		{"no end", func(h *harness, l model.Link) {
			l.Inactive, l.ValidUntil = true, time.Time{}
			if _, err := h.links.Update(l.ID, l); err != nil {
				h.t.Fatal(err)
			}
		}, map[string]any{}, http.StatusConflict, "Pick when it works first."},
		{"end passed", func(h *harness, _ model.Link) { h.advance(48 * time.Hour) }, map[string]any{}, http.StatusConflict, "Pick when it works first."},
		{"until in the past", nil, map[string]any{"valid_until": t0.Add(-time.Minute)}, http.StatusConflict, "Pick when it works first."},
		{"until cleared", nil, map[string]any{"valid_until": nil}, http.StatusConflict, "Pick when it works first."},
		{"until before from", nil, map[string]any{"valid_from": t0.Add(3 * time.Hour), "valid_until": t0.Add(2 * time.Hour)}, http.StatusBadRequest, "valid_until"},
		{"bad until", nil, map[string]any{"valid_until": "tomorrow"}, http.StatusBadRequest, "valid_until"},
		{"bad from", nil, map[string]any{"valid_from": 5}, http.StatusBadRequest, "valid_from"},
		{"slug without rotate", nil, map[string]any{"slug": "a-custom-slug-for-anna"}, http.StatusBadRequest, "slug"},
		{"bad slug", nil, map[string]any{"rotate": true, "slug": "no"}, http.StatusBadRequest, ""},
		{"unknown field", nil, map[string]any{"inactive": false}, http.StatusBadRequest, ""},
		{"unknown link", nil, nil, http.StatusNotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			l := h.link("guest_room")
			if _, err := h.links.Deactivate(l.ID); err != nil {
				t.Fatal(err)
			}
			if tc.setup != nil {
				l, _ = h.links.Get(l.ID)
				tc.setup(h, l)
			}
			before, _ := h.links.Get(l.ID)
			id := l.ID
			if tc.body == nil {
				id, tc.body = "nope", map[string]any{}
			}
			rec := h.a(req{method: "POST", path: "/api/links/" + id + "/activate", body: tc.body})
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.msg) {
				t.Errorf("%d %s, want %d %q", rec.Code, rec.Body, tc.status, tc.msg)
			}
			if after, _ := h.links.Get(l.ID); after.Slug != before.Slug || !after.Inactive || !after.ValidUntil.Equal(before.ValidUntil) {
				t.Errorf("a refused activate changed the link: %+v", after)
			}
		})
	}
}

// Deactivate and Activate keep uses, devices, passwords and sessions, as
// the Inactive field does. A session made before still works after.
func TestAdminDeactivateActivateKeepState(t *testing.T) {
	h := newHarness(t)
	d := model.Link{Name: "Anna", ValidUntil: t0.Add(24 * time.Hour), TemplateIDs: []string{"guest_room"}, OneTime: true}
	l, err := h.links.Create(d, "")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := access.HashPassword("pw")
	pw, err := h.links.AddPassword(l.ID, "Anna", hash)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := h.links.AddDevice(l.ID, model.LinkDevice{Label: "Anna", Device: "iPhone, Safari", ApprovedBy: model.ApprovedByAdmin}, 5)
	if err != nil {
		t.Fatal(err)
	}
	ses := cookie(h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": "pw"}}), sessionCookie)
	if ses == nil {
		t.Fatal("no session")
	}
	if c := h.act(l, "light.guest_room", "light.turn_on", nil, ses).Code; c != 200 {
		t.Fatalf("first use: %d", c)
	}
	for _, step := range []struct {
		path string
		code int // a guest act with the session after the step
	}{{"deactivate", http.StatusGone}, {"activate", http.StatusConflict}} {
		if rec := h.a(req{method: "POST", path: "/api/links/" + l.ID + "/" + step.path, body: map[string]any{}}); rec.Code != 200 {
			t.Fatalf("%s: %d %s", step.path, rec.Code, rec.Body)
		}
		got, _ := h.links.Get(l.ID)
		if got.Slug != l.Slug || got.UseCount != 1 || len(got.Used) != 1 || len(got.Passwords) != 1 || got.Passwords[0].ID != pw.ID ||
			len(got.Devices) != 1 || got.Devices[0].ID != dev.ID {
			t.Errorf("%s changed state: %+v", step.path, got)
		}
		// The used pair stays used: 409 once the link is on again.
		if c := h.act(l, "light.guest_room", "light.turn_on", nil, ses).Code; c != step.code {
			t.Errorf("after %s: %d, want %d", step.path, c, step.code)
		}
	}
	if c := h.act(l, "light.guest_room", "light.turn_off", nil, ses).Code; c != 200 {
		t.Errorf("session after activate: %d", c)
	}
}

// Activate runs the same policy checks as a save. A stored link that the
// editor would refuse (here, a local rule while local access is off)
// stays off.
func TestAdminActivatePolicy(t *testing.T) {
	h := newHarness(t)
	l, err := h.links.Create(model.Link{
		Name: "Local", Inactive: true, ValidUntil: t0.Add(24 * time.Hour),
		Extra: []model.Rule{{
			Select: model.Selector{Entities: []string{"light.guest_room"}}, Actions: []string{"light.turn_on"},
			Access: &model.Access{Network: model.NetworkLocal},
		}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := h.a(req{method: "POST", path: "/api/links/" + l.ID + "/activate", body: map[string]any{}})
	if v := decode[adminBody](t, rec); rec.Code != http.StatusBadRequest || len(v.Problems) == 0 || !strings.HasPrefix(v.Problems[0].Path, "extra[0]") {
		t.Errorf("activate: %d %s", rec.Code, rec.Body)
	}
	if got, _ := h.links.Get(l.ID); !got.Inactive {
		t.Error("a link that fails policy was turned on")
	}
	// A save of the same link is refused the same way.
	l.Inactive = false
	if rec := h.a(req{method: "PUT", path: "/api/links/" + l.ID, body: l}); rec.Code != http.StatusBadRequest {
		t.Errorf("save: %d", rec.Code)
	}
}

// Deactivate takes an empty body or {}, and nothing else.
func TestAdminDeactivateBody(t *testing.T) {
	h := newHarness(t)
	l := h.link("guest_room")
	if rec := h.a(req{method: "POST", path: "/api/links/" + l.ID + "/deactivate", body: map[string]any{"x": 1}}); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", rec.Code)
	}
	if got, _ := h.links.Get(l.ID); got.Inactive {
		t.Error("deactivated with a bad body")
	}
	if rec := h.a(req{method: "POST", path: "/api/links/nope/deactivate", body: map[string]any{}}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown link: %d", rec.Code)
	}
}
