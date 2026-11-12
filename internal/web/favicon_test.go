package web

import (
	"regexp"
	"strings"
	"testing"
)

// The guest page links its icons under the versioned asset path, and
// each one is served with an image type. The not-found page links none,
// so it stays the same for every unknown path.
func TestGuestFavicon(t *testing.T) {
	h := newHarness(t)
	l := h.brandLink(nil)
	page := h.g(req{method: "GET", path: "/g/" + l.Slug}).Body.String()
	want := map[string]string{
		"favicon.svg":          "image/svg+xml",
		"favicon-32.png":       "image/png",
		"apple-touch-icon.png": "image/png",
	}
	for name, ct := range want {
		m := regexp.MustCompile(`href="\.\./assets/([0-9a-f]+)/` + regexp.QuoteMeta(name) + `"`).FindStringSubmatch(page)
		if m == nil {
			t.Errorf("page does not link %s", name)
			continue
		}
		rec := h.g(req{method: "GET", path: "/assets/" + m[1] + "/" + name})
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), ct) || rec.Body.Len() == 0 {
			t.Errorf("%s: %d %q %d bytes", name, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	}
	if svg := h.g(req{method: "GET", path: "/assets/x/favicon.svg"}); svg.Code != 404 {
		t.Errorf("wrong asset version: %d", svg.Code)
	}
	nf := h.g(req{method: "GET", path: "/nope", headers: map[string]string{"Accept": "text/html"}}).Body.String()
	if strings.Contains(nf, "icon") {
		t.Errorf("not-found page links an icon: %s", nf)
	}
}
