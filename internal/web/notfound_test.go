package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/config"
)

// browserAccept is what a browser sends when it opens a page.
const browserAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

const redirectTarget = "https://www.example.com/"

func withNotFound(nf config.NotFoundOptions) harnessOpt {
	return func(o *config.Options, _ *Deps) { o.NotFound = nf }
}

func withRedirect(u string) harnessOpt {
	return func(o *config.Options, _ *Deps) { o.NotFound.Redirect = u }
}

// notFoundPaths are paths that must all get the same answer: the root,
// unknown paths, unknown and malformed slugs, and an unknown route
// under a slug.
func notFoundPaths(validSlug string) []string {
	return []string{
		"/",
		"/g/",
		"/favicon.ico",
		"/nothing/here",
		"/g/unknownslug1234567890",
		"/g/abc",
		"/g/bad!slug",
		"/g/%3Cx%3E",
		"/g/" + strings.Repeat("a", 200),
		"/g/unknownslug1234567890/state",
		"/g/" + validSlug + "/no-such-route",
	}
}

// An unknown slug, a malformed slug and the root get byte-identical
// answers, for each kind of client, with and without the redirect. So
// the answer is no slug oracle.
func TestNotFoundIdenticalAnswers(t *testing.T) {
	accepts := []struct {
		name, accept string
	}{
		{"browser", browserAccept},
		{"no accept", ""},
		{"fetch", "*/*"},
		{"json", "application/json"},
	}
	for _, redirect := range []string{"", redirectTarget} {
		h := newHarness(t, withRedirect(redirect))
		l := h.link("guest_room")
		for _, a := range accepts {
			t.Run(fmt.Sprintf("redirect=%q/%s", redirect, a.name), func(t *testing.T) {
				var first *httptest.ResponseRecorder
				for i, p := range notFoundPaths(l.Slug) {
					r := req{method: "GET", path: p, peer: peerN(i), origin: "-"}
					if a.accept != "" {
						r.headers = map[string]string{"Accept": a.accept}
					}
					rec := h.g(r)
					if rec.Code == http.StatusTooManyRequests {
						t.Fatalf("%s: rate limited; the test must use a new peer per call", p)
					}
					if first == nil {
						first = rec
						continue
					}
					if rec.Code != first.Code {
						t.Errorf("%s: status %d, root got %d", p, rec.Code, first.Code)
					}
					if !headersEqual(rec.Header(), first.Header()) {
						t.Errorf("%s: headers\n got %v\nwant %v", p, rec.Header(), first.Header())
					}
					if rec.Body.String() != first.Body.String() {
						t.Errorf("%s: body\n got %q\nwant %q", p, rec.Body.String(), first.Body.String())
					}
				}
			})
		}
	}
}

func headersEqual(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

// Which requests get the page and which keep the JSON 404.
func TestNotFoundPageOrJSON(t *testing.T) {
	h := newHarness(t)
	for i, c := range []struct {
		name     string
		method   string
		path     string
		accept   []string
		body     any
		wantHTML bool
	}{
		{"browser root", "GET", "/", []string{browserAccept}, nil, true},
		{"browser unknown slug", "GET", "/g/unknownslug1234567890", []string{browserAccept}, nil, true},
		{"plain text/html", "GET", "/", []string{"text/html"}, nil, true},
		{"upper case", "GET", "/", []string{"TEXT/HTML"}, nil, true},
		{"second Accept header", "GET", "/", []string{"application/json", "text/html"}, nil, true},
		{"head", "HEAD", "/", []string{browserAccept}, nil, true},
		{"html refused with q=0", "GET", "/", []string{"text/html;q=0, application/json"}, nil, false},
		{"html refused with q=0.000", "GET", "/", []string{"text/html;q=0.000"}, nil, false},
		{"wildcard only", "GET", "/", []string{"*/*"}, nil, false},
		{"text wildcard", "GET", "/", []string{"text/*"}, nil, false},
		{"json", "GET", "/g/unknownslug1234567890", []string{"application/json"}, nil, false},
		{"event stream", "GET", "/g/unknownslug1234567890/events", []string{"text/event-stream"}, nil, false},
		{"no accept", "GET", "/", nil, nil, false},
		{"junk accept", "GET", "/", []string{";;;,=="}, nil, false},
		{"post root", "POST", "/", []string{browserAccept}, map[string]any{}, false},
		{"post unknown slug", "POST", "/g/unknownslug1234567890/act", []string{browserAccept}, map[string]any{}, false},
		{"put", "PUT", "/", []string{browserAccept}, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			hr := httptest.NewRequest(c.method, "http://guest.test"+c.path, nil)
			if c.body != nil {
				hr = httptest.NewRequest(c.method, "http://guest.test"+c.path, strings.NewReader("{}"))
				hr.Header.Set("Content-Type", "application/json")
				hr.Header.Set("Origin", "http://guest.test")
			}
			hr.RemoteAddr = peerN(i)
			for _, a := range c.accept {
				hr.Header.Add("Accept", a)
			}
			rec := httptest.NewRecorder()
			h.guest.ServeHTTP(rec, hr)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404", rec.Code)
			}
			ct := rec.Header().Get("Content-Type")
			if c.wantHTML {
				if ct != "text/html; charset=utf-8" {
					t.Errorf("Content-Type %q, want HTML", ct)
				}
				return
			}
			if !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("Content-Type %q, want JSON", ct)
			}
			if got := decode[errorBody](t, rec).Error; got != msgNotFound {
				t.Errorf("error %q", got)
			}
		})
	}
}

// The page is neutral, has no script, passes the strict CSP and loads
// its stylesheet from the app's own assets.
func TestNotFoundPageContent(t *testing.T) {
	h := newHarness(t)
	rec := h.g(req{method: "GET", path: "/", origin: "-", headers: map[string]string{"Accept": browserAccept}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != guestCSP {
		t.Errorf("CSP %q, want %q", got, guestCSP)
	}
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY",
		"Referrer-Policy": "no-referrer", "Cache-Control": "no-store",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	body := rec.Body.String()
	for _, want := range []string{config.DefaultNotFoundTitle, config.DefaultNotFoundMessage} {
		// html/template escapes the apostrophe.
		if !strings.Contains(body, strings.ReplaceAll(want, "'", "&#39;")) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
	lower := strings.ToLower(body)
	if regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(lower) {
		t.Errorf("page has an event handler:\n%s", body)
	}
	for _, banned := range []string{"<script", "style=", "home assistant", "guest access", "guest_access", "version", "http://", "https://"} {
		if strings.Contains(lower, banned) {
			t.Errorf("page holds %q:\n%s", banned, body)
		}
	}
	// The page must not show the guest page's asset version: with it,
	// anyone could load the guest scripts and learn what runs here.
	if strings.Contains(body, h.srv.assets.version) {
		t.Errorf("page shows the guest asset version %s", h.srv.assets.version)
	}
	m := regexp.MustCompile(`<link rel="stylesheet" href="/assets/([0-9a-f]{12})/notfound\.css">`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no absolute stylesheet link:\n%s", body)
	}
	css := h.g(req{method: "GET", path: "/assets/" + m[1] + "/notfound.css", origin: "-"})
	if css.Code != http.StatusOK || !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("stylesheet: %d %q", css.Code, css.Header().Get("Content-Type"))
	}
	cssLower := strings.ToLower(css.Body.String())
	for _, banned := range []string{"home assistant", "guest", "link", "slug", "url(", "@import"} {
		if strings.Contains(cssLower, banned) {
			t.Errorf("stylesheet holds %q", banned)
		}
	}
	// The stylesheet's hash opens that one file only.
	for i, name := range []string{"guest.js", "guest.css", "controls.js", "lib.js", "dom.js", "index.html", "notfound.html", ""} {
		rec := h.g(req{method: "GET", path: "/assets/" + m[1] + "/" + name, origin: "-", peer: peerN(i)})
		if rec.Code != http.StatusNotFound {
			t.Errorf("/assets/<page hash>/%s: status %d, want 404", name, rec.Code)
		}
	}
	// The guest version does not serve the not-found stylesheet either.
	if rec := h.g(req{method: "GET", path: "/assets/" + h.srv.assets.version + "/notfound.css", origin: "-"}); rec.Code != http.StatusNotFound {
		t.Errorf("notfound.css under the guest version: %d, want 404", rec.Code)
	}
}

// Custom text is plain text: markup in it is escaped.
func TestNotFoundCustomText(t *testing.T) {
	h := newHarness(t, withNotFound(config.NotFoundOptions{
		Title:   `<b>Gone</b> & "away"`,
		Message: `<script>alert(1)</script>` + "\nSecond line",
	}))
	body := h.g(req{method: "GET", path: "/", origin: "-", headers: map[string]string{"Accept": "text/html"}}).Body.String()
	if strings.Contains(body, "<b>") || strings.Contains(body, "<script") {
		t.Errorf("markup not escaped:\n%s", body)
	}
	for _, want := range []string{"&lt;b&gt;Gone&lt;/b&gt; &amp; &#34;away&#34;", "&lt;script&gt;alert(1)&lt;/script&gt;\nSecond line"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
}

// Empty text options fall back to the default text.
func TestNotFoundEmptyTextUsesDefaults(t *testing.T) {
	h := newHarness(t, withNotFound(config.NotFoundOptions{}))
	body := h.g(req{method: "GET", path: "/", origin: "-", headers: map[string]string{"Accept": "text/html"}}).Body.String()
	if !strings.Contains(body, "Ask the person who sent it for a new one.") {
		t.Errorf("default message missing:\n%s", body)
	}
}

// The redirect goes to the option's URL and nowhere else, whatever the
// request holds.
func TestNotFoundRedirectNeverReflectsRequest(t *testing.T) {
	h := newHarness(t, withRedirect(redirectTarget))
	hostile := map[string]string{
		"Accept":             browserAccept,
		"Origin":             "https://evil.example",
		"X-Forwarded-Host":   "evil.example",
		"X-Forwarded-Proto":  "http",
		"X-Forwarded-Prefix": "//evil.example",
		"Referer":            "https://evil.example/",
	}
	for i, p := range []string{
		"/",
		"/?next=https://evil.example/",
		"/?redirect=%2F%2Fevil.example",
		"/g/https:%2F%2Fevil.example",
		"/g/unknownslug1234567890?url=https://evil.example",
		"/evil.example/x",
		"/%0d%0aLocation:%20evil.example",
	} {
		t.Run(p, func(t *testing.T) {
			hr := httptest.NewRequest("GET", "http://guest.test"+p, nil)
			hr.Host = "evil.example"
			hr.RemoteAddr = peerN(i)
			for k, v := range hostile {
				hr.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.guest.ServeHTTP(rec, hr)
			if rec.Code != http.StatusFound {
				t.Fatalf("status %d, want 302", rec.Code)
			}
			if got := rec.Header().Values("Location"); len(got) != 1 || got[0] != redirectTarget {
				t.Errorf("Location %q, want %q", got, redirectTarget)
			}
			if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy %q: the target would see the path", got)
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != guestCSP {
				t.Errorf("CSP %q", got)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body %q, want none", rec.Body.String())
			}
		})
	}
}

// With the redirect on, API calls still get JSON, and a known link is
// still served.
func TestNotFoundRedirectLeavesAPIAndLinks(t *testing.T) {
	h := newHarness(t, withRedirect(redirectTarget))
	l := h.link("guest_room")
	for i, r := range []req{
		{method: "GET", path: "/g/unknownslug1234567890/state"},
		{method: "GET", path: "/", headers: map[string]string{"Accept": "application/json"}},
		{method: "POST", path: "/g/unknownslug1234567890/act", body: map[string]any{}, headers: map[string]string{"Accept": browserAccept}},
	} {
		r.peer = peerN(i)
		rec := h.g(r)
		if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s %s: %d %q, want JSON 404", r.method, r.path, rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("Location") != "" {
			t.Errorf("%s %s: redirected", r.method, r.path)
		}
	}
	rec := h.g(req{method: "GET", path: "/g/" + l.Slug, origin: "-", headers: map[string]string{"Accept": browserAccept}})
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Errorf("known link: %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// A redirect option that is not an absolute http or https URL is not
// used: browsers get the page, and the app warns without showing the
// value.
func TestNotFoundBadRedirectFallsBack(t *testing.T) {
	for _, bad := range []string{
		"javascript:alert(1)",
		"//evil.example",
		"/relative",
		"ftp://example.com/",
		"https://user:secret@example.com/",
		"https://",
		"example.com",
		"data:text/html,hi",
		"http://exa mple.com/",
	} {
		t.Run(bad, func(t *testing.T) {
			o := config.DefaultOptions()
			o.NotFound.Redirect = bad
			warns := OptionWarnings(o)
			if !slices.Contains(warns, notFoundRedirectWarning) {
				t.Errorf("no warning: %v", warns)
			}
			for _, w := range warns {
				if strings.Contains(w, bad) {
					t.Errorf("warning shows the value: %q", w)
				}
			}
			h := newHarness(t, withRedirect(bad))
			rec := h.g(req{method: "GET", path: "/", origin: "-", headers: map[string]string{"Accept": browserAccept}})
			if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
				t.Errorf("status %d, Location %q; want the page", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
	o := config.DefaultOptions()
	o.NotFound.Redirect = redirectTarget
	if slices.Contains(OptionWarnings(o), notFoundRedirectWarning) {
		t.Error("a good redirect warns")
	}
}

// The limiters still run first. Slug guesses from a browser still count
// as misses, and the root still uses the pre-auth limiter.
func TestNotFoundStillRateLimited(t *testing.T) {
	for _, redirect := range []string{"", redirectTarget} {
		t.Run("misses redirect="+redirect, func(t *testing.T) {
			h := newHarness(t, withRedirect(redirect))
			misses := h.srv.settings().RateLimits.MissesPerHourPerIP
			var last int
			for i := range misses + 1 {
				last = h.g(req{method: "GET", path: fmt.Sprintf("/g/guess%020d", i), origin: "-",
					headers: map[string]string{"Accept": browserAccept}}).Code
				if i < misses && last == http.StatusTooManyRequests {
					t.Fatalf("limited after %d misses", i)
				}
			}
			if last != http.StatusTooManyRequests {
				t.Errorf("miss %d: status %d, want 429", misses+1, last)
			}
		})
		t.Run("root redirect="+redirect, func(t *testing.T) {
			h := newHarness(t, withRedirect(redirect))
			burst := h.srv.settings().RateLimits.GuestBurst
			var last int
			for range burst + 1 {
				last = h.g(req{method: "GET", path: "/", origin: "-", headers: map[string]string{"Accept": browserAccept}}).Code
			}
			if last != http.StatusTooManyRequests {
				t.Errorf("request %d: status %d, want 429", burst+1, last)
			}
		})
	}
}

// A link that exists but is expired or revoked keeps its own answers:
// the page shell, and 410 from the API.
func TestNotFoundLeavesInactiveLinks(t *testing.T) {
	for _, c := range []struct {
		name string
		end  func(h *harness, id string)
	}{
		{"expired", func(h *harness, _ string) { h.advance(48 * time.Hour) }},
		{"revoked", func(h *harness, id string) {
			if err := h.links.Revoke(id); err != nil {
				h.t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, withRedirect(redirectTarget))
			l := h.link("guest_room")
			c.end(h, l.ID)
			page := h.g(req{method: "GET", path: "/g/" + l.Slug, origin: "-", headers: map[string]string{"Accept": browserAccept}})
			if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `id="view-ended"`) {
				t.Errorf("page: %d, want the page shell", page.Code)
			}
			state := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state", headers: map[string]string{"Accept": browserAccept}})
			if state.Code != http.StatusGone {
				t.Errorf("state: %d, want 410", state.Code)
			}
		})
	}
}
