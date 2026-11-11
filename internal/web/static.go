package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/web/ui"
)

// assetSet is the guest page and its static files. The files are served
// under /assets/<version>/, where version is a hash of their content, so
// they can be cached for a year and a new build never serves stale files.
type assetSet struct {
	version string
	page    []byte            // the guest page shell, asset paths filled in
	files   map[string][]byte // name -> content, pages excluded
	// notFound is the not-found page template. Server renders it once.
	notFound *template.Template
	// notFoundCSS is the not-found page's stylesheet, served at
	// notFoundCSSPath under assetsPrefix. That path has its own hash, so
	// the page never shows the guest page's version, and the guest
	// page's files can't be found from it.
	notFoundCSS     []byte
	notFoundCSSPath string
}

const (
	guestDir     = "guest"
	shellName    = "index.html"
	notFoundName = "notfound.html"
	notFoundCSS  = "notfound.css"
	assetsPrefix = "/assets/"
	// assetCache is for versioned assets only. Every other guest response
	// is no-store.
	assetCache = "public, max-age=31536000, immutable"
)

// loadAssets reads the embedded guest files. It panics when they are
// missing: that is a build bug.
func loadAssets() *assetSet {
	entries, err := fs.ReadDir(ui.FS, guestDir)
	if err != nil {
		panic("web: embedded guest files missing: " + err.Error())
	}
	a := &assetSet{files: map[string][]byte{}}
	h := sha256.New()
	var shell []byte
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	for _, name := range names {
		raw, err := fs.ReadFile(ui.FS, path.Join(guestDir, name))
		if err != nil {
			panic("web: read embedded " + name + ": " + err.Error())
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(raw)
		switch name {
		case shellName:
			shell = raw
		case notFoundName:
			a.notFound = template.Must(template.New(name).Parse(string(raw)))
		case notFoundCSS:
			sum := sha256.Sum256(raw)
			a.notFoundCSS = raw
			a.notFoundCSSPath = hex.EncodeToString(sum[:])[:12] + "/" + name
		default:
			a.files[name] = raw
		}
	}
	if shell == nil || a.notFound == nil || a.notFoundCSS == nil {
		panic("web: embedded guest index.html, notfound.html or notfound.css missing")
	}
	a.version = hex.EncodeToString(h.Sum(nil))[:12]
	// The page lives at /g/<slug>, so "../assets" is /assets.
	a.page = bytes.ReplaceAll(shell, []byte("{{ASSETS}}"), []byte(".."+assetsPrefix+a.version))
	return a
}

// serveAsset serves /assets/<version>/<name>. A wrong version is a 404,
// so an old page never gets new files under an old cache key. The
// not-found stylesheet is the one file under its own hash.
func (a *assetSet) serveAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, assetsPrefix)
	version, name, ok := strings.Cut(rest, "/")
	raw, found := a.files[name]
	switch {
	case rest == a.notFoundCSSPath:
		raw = a.notFoundCSS
	case !ok || version != a.version || !found:
		writeError(w, http.StatusNotFound, msgNotFound)
		return
	}
	ct := mime.TypeByExtension(path.Ext(name))
	if path.Ext(name) == ".js" {
		ct = "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", assetCache)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(raw))
}

// renderNotFound renders the not-found page with title and message,
// which are plain text. The stylesheet path is absolute: the page is
// served at many paths, and every one must get the same bytes.
func (a *assetSet) renderNotFound(title, message string) []byte {
	var b bytes.Buffer
	err := a.notFound.Execute(&b, struct{ Title, Message, CSS string }{
		Title: title, Message: message, CSS: assetsPrefix + a.notFoundCSSPath,
	})
	if err != nil {
		panic("web: render not-found page: " + err.Error())
	}
	return b.Bytes()
}

// servePage writes the guest page shell. It holds no link data: the page
// script asks the API.
func (a *assetSet) servePage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(a.page)
}

// adminCSP goes on the admin page. HA shows it in an iframe on its own
// origin, so frame-ancestors is 'self'.
const adminCSP = "default-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'"

// adminUI serves the embedded admin page and its files.
func adminUI() http.Handler {
	sub, err := fs.Sub(ui.FS, "admin")
	if err != nil {
		panic("web: embedded admin files missing: " + err.Error())
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", adminCSP)
		files.ServeHTTP(w, r)
	})
}
