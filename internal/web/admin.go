package web

import (
	"errors"
	"net/http"
	"net/netip"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/approval"
	"github.com/igrybkov/ha-guest-access/internal/audit"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/model"
	"github.com/igrybkov/ha-guest-access/internal/store"
)

// supervisorIngress is the address of the Supervisor ingress proxy. The
// Supervisor docs name it as the only peer an ingress app should accept.
var supervisorIngress = netip.MustParseAddr("172.30.32.2")

// AdminHandler returns the admin port's handler: the JSON API under
// /api/, the admin page at /, and /healthz.
//
// Every request but /healthz must come from the ingress proxy (unless
// DevAdminAnyPeer), passes the admin rate limiter, and, if it is not a
// GET or HEAD, must say Content-Type: application/json. That last rule
// is the CSRF guard: a cross-site form can't send JSON without CORS,
// which this port never allows.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/settings", s.getSettings)

	mux.HandleFunc("GET /api/templates", s.listTemplates)
	mux.HandleFunc("POST /api/templates", s.createTemplate)
	mux.HandleFunc("PUT /api/templates/{id}", s.updateTemplate)
	mux.HandleFunc("DELETE /api/templates/{id}", s.deleteTemplate)
	mux.HandleFunc("POST /api/preview", s.preview)

	mux.HandleFunc("GET /api/links", s.listLinks)
	mux.HandleFunc("POST /api/links", s.createLink)
	mux.HandleFunc("GET /api/links/{id}", s.getLink)
	mux.HandleFunc("PUT /api/links/{id}", s.updateLink)
	mux.HandleFunc("DELETE /api/links/{id}", s.deleteLink)
	mux.HandleFunc("POST /api/links/{id}/revoke", s.revokeLink)
	mux.HandleFunc("POST /api/links/{id}/rotate", s.rotateLink)
	mux.HandleFunc("POST /api/links/{id}/passwords", s.addPassword)
	mux.HandleFunc("DELETE /api/links/{id}/passwords/{pid}", s.removePassword)
	mux.HandleFunc("DELETE /api/links/{id}/devices/{did}", s.removeDevice)
	mux.HandleFunc("POST /api/links/{id}/devices/reset", s.resetDevices)
	mux.HandleFunc("GET /api/devices/pending", s.listPending)
	mux.HandleFunc("POST /api/devices/pending/{id}", s.decidePending)
	mux.HandleFunc("GET /api/links/{id}/url", s.linkURL)
	mux.HandleFunc("GET /api/links/{id}/qr.svg", s.linkQRSVG)
	mux.HandleFunc("GET /api/links/{id}/qr.png", s.linkQRPNG)

	mux.HandleFunc("GET /api/catalog", s.getCatalog)
	mux.HandleFunc("GET /api/entities", s.listEntities)
	mux.HandleFunc("GET /api/areas", s.listAreas)
	mux.HandleFunc("GET /api/labels", s.listLabels)
	mux.HandleFunc("GET /api/devices", s.listDevices)
	mux.HandleFunc("GET /api/diagnostics", s.getDiagnostics)
	mux.HandleFunc("GET /api/audit", s.getAudit)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	mux.Handle("/", adminUI())
	return s.recoverPanics(s.adminGuard(s.auditAdmin(mux)))
}

// adminGuard runs the peer check, the admin limiter and the JSON rule.
func (s *Server) adminGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		peer := peerAddr(r.RemoteAddr)
		if !s.deps.DevAdminAnyPeer && peer != supervisorIngress {
			s.log.Warn("admin: refused peer", "peer", r.RemoteAddr, "path", r.URL.Path)
			if ok, skipped := s.auditThrottle.Allow("admin.refused|"+access.LimitKey(peer, s.settings().RateLimits.IPv6Prefix), s.now()); ok {
				rec := audit.Record{Time: s.now(), Event: "admin.refused", Result: audit.ResultDenied, Reason: "peer",
					Method: r.Method, Route: routeLabel(r.URL.Path), Count: skipped}
				setClient(&rec, access.Client{IP: peer}, peer)
				s.audit.Log(rec)
			}
			writeError(w, http.StatusForbidden, "access only through Home Assistant")
			return
		}
		if ok, retry := s.lim.admin.Allow(access.LimitKey(peer, s.settings().RateLimits.IPv6Prefix), s.now()); !ok {
			tooMany(w, retry)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !isJSON(r) {
			writeError(w, http.StatusUnsupportedMediaType, "send Content-Type: application/json")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// peerAddr parses r.RemoteAddr. Only the peer counts: headers prove
// nothing on this port.
func peerAddr(remote string) netip.Addr {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap()
	}
	if a, err := netip.ParseAddr(remote); err == nil {
		return a.Unmap()
	}
	return netip.Addr{}
}

// Admin error statuses.
var (
	errNoTemplate  = errors.New("template not found")
	errTemplateDup = errors.New("a template with this id exists")
	errNoBaseURL   = errors.New("set public_base_url in the app options first")
)

// adminBody is the JSON shape of an admin error. Problems lists
// validation failures with field paths.
type adminBody struct {
	Error    string          `json:"error"`
	Problems []model.Problem `json:"problems,omitempty"`
}

// decodeAdmin decodes an admin body, or answers 400 or 413.
func (s *Server) decodeAdmin(w http.ResponseWriter, r *http.Request, v any) bool {
	err := decodeBody(w, r, s.deps.Options.AdminBodyBytes, v)
	switch {
	case errors.Is(err, errBodyTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, msgTooLarge)
	case err != nil:
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
	}
	return err == nil
}

// writeAdminErr maps an error to a status. Admin errors may carry detail:
// only the ingress proxy reaches this port.
func (s *Server) writeAdminErr(w http.ResponseWriter, r *http.Request, err error) {
	var verr *model.ValidationError
	status := http.StatusInternalServerError
	switch {
	case errors.As(err, &verr):
		adminAudit(r).Reason = "invalid"
		writeJSON(w, http.StatusBadRequest, adminBody{Error: "invalid", Problems: verr.Problems})
		return
	case errors.Is(err, links.ErrNotFound), errors.Is(err, errNoTemplate), errors.Is(err, approval.ErrUnknownRequest):
		status = http.StatusNotFound
	case errors.Is(err, links.ErrSlugTaken), errors.Is(err, links.ErrTooManyDevices), errors.Is(err, errTemplateDup), errors.Is(err, errNoBaseURL):
		status = http.StatusConflict
	case errors.Is(err, access.ErrBadSlug), errors.Is(err, access.ErrEmptyPassword), errors.Is(err, access.ErrPasswordTooLong):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrNewerVersion):
		status = http.StatusForbidden
	}
	adminAudit(r).Reason = adminReason(err, status)
	if status >= 500 {
		s.log.Error("admin: request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	writeError(w, status, err.Error())
}
