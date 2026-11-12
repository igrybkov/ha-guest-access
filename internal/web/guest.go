package web

import (
	"bytes"
	"context"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/audit"
	"github.com/igrybkov/ha-guest-access/internal/authz"
	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/model"
	"github.com/igrybkov/ha-guest-access/internal/policy"
)

// Cookie name bases. The real name adds the link id
// (access.LinkCookie) and the path is access.GuestCookiePath. So two links
// open in one browser never overwrite each other's cookies, and a session
// survives a slug rotation.
const (
	sessionCookie = "ga_session"
	nearCookie    = "ga_near"
)

// guestCSP allows only the app's own files: no inline script or style,
// no framing, no other hosts.
const guestCSP = "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// routeKind says how much of the check chain a guest route needs.
type routeKind int

const (
	// kindOpen routes need a known slug only: the page shell and the
	// password form. They always use the pre-auth limiter.
	kindOpen routeKind = iota
	// kindDevice routes need an active link and the password session (if
	// the link has passwords), but not the device gate: they are how a
	// device gets through it. Device approval answers them.
	kindDevice
	// kindGuest routes need the whole chain: an active link, the password
	// session (if the link has passwords) and the device gate.
	kindGuest
)

// guestRoute is one route under /g/{slug}/.
type guestRoute struct {
	method string
	kind   routeKind
	// post routes take a JSON body. Origin and Content-Type are checked.
	post   bool
	handle func(*Server, *guestReq)
}

// guestRouteNames are the route names under /g/{slug}/. routeLabel uses
// them so logs never show a path a guest made up.
var guestRouteNames = map[string]bool{
	"password": true, "state": true, "events": true, "act": true, "where": true, "party": true,
	"device/request": true, "device/status": true, "device/pending": true, "device/decide": true,
}

// routes returns the guest routes by name. "" is the page itself.
func guestRoutes() map[string]guestRoute {
	return map[string]guestRoute{
		"":         {method: http.MethodGet, kind: kindOpen, handle: (*Server).guestPage},
		"password": {method: http.MethodPost, kind: kindOpen, post: true, handle: (*Server).guestPassword},
		"state":    {method: http.MethodGet, kind: kindGuest, handle: (*Server).guestState},
		"events":   {method: http.MethodGet, kind: kindGuest, handle: (*Server).guestEvents},
		"act":      {method: http.MethodPost, kind: kindGuest, post: true, handle: (*Server).guestAct},
		"where":    {method: http.MethodPost, kind: kindGuest, post: true, handle: (*Server).guestWhere},
		"party":    {method: http.MethodGet, kind: kindGuest, handle: (*Server).guestParty},

		"device/request": {method: http.MethodPost, kind: kindDevice, post: true, handle: deviceRoute(Devices.RequestAccess)},
		"device/status":  {method: http.MethodGet, kind: kindDevice, handle: deviceRoute(Devices.Status)},
		"device/pending": {method: http.MethodGet, kind: kindDevice, handle: deviceRoute(Devices.PendingForDevice)},
		"device/decide":  {method: http.MethodPost, kind: kindDevice, post: true, handle: deviceRoute(Devices.DecideByDevice)},
	}
}

// deviceRoute adapts a device approval handler to a guest route. The
// body of a POST is capped at the guest body limit.
func deviceRoute(h func(Devices, http.ResponseWriter, *http.Request, model.Link, access.Client, string)) func(*Server, *guestReq) {
	return func(s *Server, g *guestReq) {
		if g.r.Method == http.MethodPost {
			g.r.Body = http.MaxBytesReader(g.w, g.r.Body, s.deps.Options.GuestBodyBytes)
		}
		h(s.devices, g.w, g.r, g.link, g.client, g.label)
	}
}

// guestReq is one guest request as it moves through the checks.
type guestReq struct {
	w      http.ResponseWriter
	r      *http.Request
	now    time.Time
	st     model.Settings
	lim    *limitSet
	client access.Client
	nets   []netip.Prefix // local networks from settings
	// limKey keys the per-IP limiters: the client IP, or its IPv6 prefix
	// (see access.KeyAddr). Logs and audit records use the full IP.
	limKey string

	// Set by the chain.
	slug   string
	link   model.Link
	label  string // password label, the guest's name; "" without passwords
	device string // device id from the DeviceGate

	// sessionReason says why session() failed: no_session or
	// session_invalid.
	sessionReason string
	audit         guestAudit
}

// ipKey is the full client IP, for logs and HA events. Limiters use
// limKey.
func (g *guestReq) ipKey() string {
	if !g.client.IP.IsValid() {
		return "invalid"
	}
	return g.client.IP.String()
}

// allowPreauth takes a token from the pre-auth limiter, or answers 429.
func (g *guestReq) allowPreauth() bool {
	ok, retry := g.lim.preauth.Allow(g.limKey, g.now)
	if !ok {
		g.rateLimited(reasonLimitPreauth)
		tooMany(g.w, retry)
	}
	return ok
}

// allowAuthed takes a token from the per link + IP limiter of an
// authenticated guest, or answers 429.
func (g *guestReq) allowAuthed() bool {
	ok, retry := g.lim.authed.Allow(g.link.ID+"|"+g.limKey, g.now)
	if !ok {
		g.rateLimited(reasonLimitActions)
		tooMany(g.w, retry)
	}
	return ok
}

// allowAct takes a token for an action. A granted continuous action
// (a slider) uses the per link + IP + entity limiter; every other action
// uses allowAuthed. An ungranted pair never gets its own key, so made-up
// entity ids can't grow the limiter.
func (g *guestReq) allowAct(req authz.Request, grants authz.Grants) bool {
	act, ok := catalog.Lookup(req.Action)
	if _, granted := grants[authz.GrantKey{EntityID: req.EntityID, Action: req.Action}]; !ok || !act.Continuous || !granted {
		return g.allowAuthed()
	}
	ok, retry := g.lim.cont.Allow(g.link.ID+"|"+g.limKey+"|"+req.EntityID, g.now)
	if !ok {
		g.rateLimited(reasonLimitContinuous)
		tooMany(g.w, retry)
	}
	return ok
}

type guestKey struct{}

// GuestHandler returns the guest port's handler. It has no admin routes.
//
// Every slug route runs these checks, in order:
//
//  1. A rate limiter. Which one depends on steps 2 to 4, which are cheap
//     (a map lookup and an HMAC): an authenticated guest on a kindGuest
//     or kindDevice route uses the per link + IP limiter; every other
//     request uses the pre-auth limiter per IP. A slug miss also counts
//     against the miss limiters, per IP and across all IPs; over either,
//     it gets 429. An authenticated act is the one exception: it takes
//     its token after the body is read, from the continuous limiter
//     (sliders, per link + IP + entity) or the per link + IP limiter.
//     If steps 3 or 6 refuse it first, it takes a per link + IP token
//     before the refusal is sent.
//  2. The slug lookup. A valid slug always works, whatever the miss
//     limiters say. A link that fails policy.ServableLink (a hand edit
//     of links.yaml broke it) counts as a miss.
//  3. POST shape: JSON content type and a same-origin Origin header.
//  4. link.Active(now).
//  5. The password session, if the link has passwords.
//  6. DeviceGate.Admit, on kindGuest routes only.
//  7. The route's own work.
//
// Static assets under /assets/ have their own per-IP limiter and no
// other check: they are public and the same for everyone.
//
// The root, an unknown path and an unknown slug get s.notFound after
// their limiter: a page or a redirect for a browser, JSON otherwise.
//
// The page shell (kindOpen) stops after step 2 and is served even for an
// inactive link, so the page can say so. The password form stops after
// step 4.
func (s *Server) GuestHandler() http.Handler {
	routes := guestRoutes()
	mux := http.NewServeMux()
	slug := func(w http.ResponseWriter, r *http.Request) { s.serveSlug(routes, w, r) }
	mux.HandleFunc("/g/{slug}", slug)
	mux.HandleFunc("/g/{slug}/{route...}", slug)
	mux.HandleFunc(assetsPrefix, func(w http.ResponseWriter, r *http.Request) {
		g := reqOf(r)
		if ok, retry := g.lim.assets.Allow(g.limKey, g.now); !ok {
			g.rateLimited(reasonLimitAssets)
			tooMany(w, retry)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, msgBadRequest)
			return
		}
		s.assets.serveAsset(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if g := reqOf(r); g.allowPreauth() {
			s.notFound(w, r)
		}
	})
	return s.guestBase(s.recoverPanics(mux))
}

// guestBase sets the security headers on every guest response, derives
// the client, records it for diagnostics, and hands a guestReq down.
func (s *Server) guestBase(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", guestCSP)
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(self), camera=(), microphone=()")

		st := s.settings()
		nets := s.nets
		c := access.ClientFrom(r, s.trusted)
		now := s.now()
		e := diagEntry(r, c, c.Local(nets), now)
		s.diag.add(e)
		if s.log.Enabled(r.Context(), slog.LevelDebug) {
			attrs := []any{"route", e.Route, "peer", e.Peer, "client", e.Client, "via_proxy", c.ViaProxy, "unresolved", c.Unresolved}
			for k, v := range e.Headers {
				attrs = append(attrs, k, v)
			}
			s.log.Debug("guest request", attrs...)
		}
		g := &guestReq{w: w, now: now, st: st, lim: s.lim, client: c, nets: nets,
			limKey: access.LimitKey(c.IP, st.RateLimits.IPv6Prefix)}
		g.r = r.WithContext(context.WithValue(r.Context(), guestKey{}, g))
		next.ServeHTTP(w, g.r)
		s.emitGuest(g)
	})
}

func reqOf(r *http.Request) *guestReq { return r.Context().Value(guestKey{}).(*guestReq) }

func (s *Server) serveSlug(routes map[string]guestRoute, w http.ResponseWriter, r *http.Request) {
	g := reqOf(r)
	g.w, g.r = w, r
	route, known := routes[r.PathValue("route")]
	if !known || r.Method != route.method {
		if !g.allowPreauth() {
			return
		}
		if known {
			w.Header().Set("Allow", route.method)
			writeError(w, http.StatusMethodNotAllowed, msgBadRequest)
			return
		}
		s.notFound(w, r)
		return
	}

	// /g/{slug}/ would serve the page under a path that breaks its
	// relative asset links. Send the browser to /g/{slug}. The redirect
	// only repeats the path the client sent, so it tells nothing about
	// the slug.
	if r.PathValue("route") == "" && strings.HasSuffix(r.URL.Path, "/") {
		if !g.allowPreauth() {
			return
		}
		w.Header().Set("Location", strings.TrimSuffix(r.URL.EscapedPath(), "/"))
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}

	// Steps 1 and 2.
	g.slug = r.PathValue("slug")
	link, found := s.deps.Links.FindBySlug(g.slug)
	if found && !s.servable(link) {
		found = false // a broken link is never served: it reads as a miss
	}
	if !found {
		if g.allowPreauth() {
			s.miss(g)
		}
		return
	}
	g.link = link
	active := link.Active(g.now)
	authed := active && s.session(g)
	// An authenticated act takes its token in guestAct, once it knows the
	// action: continuous and discrete actions use different limiters.
	// Until then, any early refusal takes a discrete token first
	// (denyLimit), so a refused act is never free.
	actLater := route.kind != kindOpen && authed && r.PathValue("route") == "act"
	denyLimit := func() bool { return !actLater || g.allowAuthed() }
	switch {
	case actLater:
	case route.kind != kindOpen && authed:
		if !g.allowAuthed() {
			return
		}
	default:
		if !g.allowPreauth() {
			return
		}
	}
	if r.PathValue("route") == "" {
		route.handle(s, g)
		return
	}

	// Steps 3 to 6.
	if route.post && !checkPost(g, denyLimit) {
		return
	}
	if !active {
		g.deny(reasonInactive)
		writeError(w, http.StatusGone, msgGone)
		return
	}
	if route.kind != kindOpen && !authed {
		g.deny(g.sessionReason)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": msgPassword, "need": "password"})
		return
	}
	if route.kind == kindGuest {
		// Admit writes its own refusal. Hold it, so a refused act can
		// take its token (and maybe answer 429) first.
		held := &heldResponse{header: w.Header(), code: http.StatusOK}
		dev, ok := s.devices.Admit(held, r, link, g.client, g.label)
		if !ok {
			if !denyLimit() {
				return
			}
			g.deny(reasonDevice)
			held.flush(w)
			return
		}
		held.flush(w)
		g.device = dev
	}
	route.handle(s, g)
}

// servable reports whether a guest may use l: it passes
// policy.ServableLink. A link that fails is logged once with its
// problems, and again when they change. The admin API lists it with its
// problems, so it can be fixed.
func (s *Server) servable(l model.Link) bool {
	err := policy.ServableLink(l)
	s.badMu.Lock()
	defer s.badMu.Unlock()
	if err == nil {
		delete(s.badLinks, l.ID)
		return true
	}
	if msg := err.Error(); s.badLinks[l.ID] != msg {
		s.badLinks[l.ID] = msg
		s.log.Warn("a link in links.yaml does not validate; guests get not found until it is fixed",
			"link", l.ID, "name", l.DisplayName(), "problems", msg)
	}
	return false
}

// miss answers an unknown slug: 404, or 429 once the client or everyone
// together has missed too often.
func (s *Server) miss(g *guestReq) {
	if ok, retry := g.lim.missIP.Allow(g.limKey, g.now); !ok {
		g.rateLimited(reasonLimitMisses)
		tooMany(g.w, retry)
		return
	}
	if ok, retry := g.lim.missAll.Allow(missGlobalKey, g.now); !ok {
		s.log.Debug("slug miss cap reached", "client", g.ipKey())
		g.rateLimited(reasonLimitMisses)
		tooMany(g.w, retry)
		return
	}
	g.note(auditEventSlugMiss, audit.ResultDenied, "")
	s.notFound(g.w, g.r)
}

// notFound answers the root, an unknown path or an unknown slug. A
// browser that asks for a page gets the not-found redirect, if one is
// set, or the not-found page with status 404. Every other request gets
// the JSON 404.
//
// The answer depends on the method, the Accept header and the options
// only. So an unknown slug, a malformed slug and the root get the same
// bytes, and the answer tells nothing about which slugs exist. The
// redirect target never comes from the request.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	if !wantsHTML(r) {
		writeError(w, http.StatusNotFound, msgNotFound)
		return
	}
	if s.notFoundRedirect != "" {
		w.Header().Set("Location", s.notFoundRedirect)
		w.WriteHeader(http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(s.notFoundPage)
}

// wantsHTML reports whether r is a browser asking for a page: a GET or
// HEAD whose Accept header lists text/html with a quality above zero.
// A wildcard such as */* does not count, so fetch and EventSource calls
// keep their JSON answers.
func wantsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	for _, v := range r.Header.Values("Accept") {
		for part := range strings.SplitSeq(v, ",") {
			mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil || mt != "text/html" {
				continue
			}
			if q, ok := params["q"]; ok && isZeroQ(q) {
				continue
			}
			return true
		}
	}
	return false
}

// isZeroQ reports whether the quality value q is zero ("0", "0.0",
// "0.000"). A value that does not parse counts as not zero, which
// errs toward the page.
func isZeroQ(q string) bool {
	f, err := strconv.ParseFloat(q, 64)
	return err == nil && f <= 0
}

// session reports whether the request may use the link as far as
// passwords go: the link has none, or a session cookie names a password
// the link still has. It sets g.label.
func (s *Server) session(g *guestReq) bool {
	g.label, g.sessionReason = "", ""
	if len(g.link.Passwords) == 0 {
		return true
	}
	// A browser can hold several cookies of one name (another path, or
	// one a page script tossed in). Any one that checks out will do, so
	// a stray cookie can't lock a guest out.
	cs := g.r.CookiesNamed(access.LinkCookie(sessionCookie, g.link.ID))
	if len(cs) == 0 {
		g.sessionReason = reasonNoSession
		return false
	}
	g.sessionReason = reasonSessionInvalid
	for _, c := range cs {
		pwID, ok := s.deps.Signer.VerifySession(g.link.ID, c.Value, g.now)
		if !ok {
			continue
		}
		pw, ok := g.link.Password(pwID)
		if !ok {
			continue // its password was removed
		}
		g.sessionReason = ""
		g.label = pw.Label
		return true
	}
	return false
}

// checkPost refuses a POST that is not JSON or not from the page's own
// origin. Browsers send Origin on every POST, so a missing one is
// refused too.
//
// limit runs before a refusal is written; when it returns false it has
// answered (429) itself.
func checkPost(g *guestReq, limit func() bool) bool {
	if !isJSON(g.r) {
		if !limit() {
			return false
		}
		g.deny(reasonContentType)
		writeError(g.w, http.StatusUnsupportedMediaType, msgJSONOnly)
		return false
	}
	if !sameOrigin(g.r) {
		if !limit() {
			return false
		}
		g.deny(reasonOrigin)
		writeError(g.w, http.StatusForbidden, msgOrigin)
		return false
	}
	return true
}

// heldResponse holds a response body and status until flush. Headers go
// straight to the real writer.
type heldResponse struct {
	header http.Header
	code   int
	wrote  bool
	body   bytes.Buffer
}

func (h *heldResponse) Header() http.Header { return h.header }

func (h *heldResponse) WriteHeader(code int) {
	if !h.wrote {
		h.code, h.wrote = code, true
	}
}

func (h *heldResponse) Write(p []byte) (int, error) {
	h.WriteHeader(http.StatusOK)
	return h.body.Write(p)
}

// flush writes what was held to w, if anything was.
func (h *heldResponse) flush(w http.ResponseWriter) {
	if !h.wrote {
		return
	}
	w.WriteHeader(h.code)
	_, _ = w.Write(h.body.Bytes())
}

// sameOrigin reports whether r's Origin header names r's own host.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// setCookie sets guest cookie base for the request's link. Its name
// carries the link id and its path is access.GuestCookiePath. It is
// HttpOnly, SameSite=Strict, and Secure when the request came over
// HTTPS.
func setCookie(g *guestReq, base, value string, exp time.Time) {
	http.SetCookie(g.w, &http.Cookie{
		Name:     access.LinkCookie(base, g.link.ID),
		Value:    value,
		Path:     access.GuestCookiePath,
		Expires:  exp,
		MaxAge:   max(1, int(exp.Sub(g.now).Seconds())),
		HttpOnly: true,
		Secure:   g.client.HTTPS,
		SameSite: http.SameSiteStrictMode,
	})
}
