package web

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/approval"
	"github.com/igrybkov/ha-guest-access/internal/audit"
	"github.com/igrybkov/ha-guest-access/internal/authz"
	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/links"
)

// Audit reasons. The guest response stays generic; these go to the
// audit record only.
const (
	reasonInactive        = "inactive"
	reasonNoSession       = "no_session"
	reasonSessionInvalid  = "session_invalid"
	reasonDevice          = "device_not_approved"
	reasonNotGranted      = "not_granted"
	reasonNetwork         = "access_network"
	reasonNear            = "access_near"
	reasonOneTimeUsed     = "one_time_used"
	reasonBadData         = "bad_data"
	reasonBadRequest      = "bad_request"
	reasonTooLarge        = "too_large"
	reasonHAError         = "ha_error"
	reasonOrigin          = "origin"
	reasonContentType     = "content_type"
	reasonWrongPassword   = "wrong_password"
	reasonLocked          = "locked"
	reasonInternal        = "internal"
	reasonLimitPreauth    = "limit_preauth"
	reasonLimitActions    = "limit_actions"
	reasonLimitContinuous = "limit_continuous"
	reasonLimitMisses     = "limit_slug_misses"
	reasonLimitStreams    = "limit_streams"
	reasonAccuracyTooLow  = "accuracy_too_low"
	auditThrottlePeriod   = time.Minute
	// sliderAuditPeriod spaces the allowed records of a slider drag.
	sliderAuditPeriod     = 10 * time.Second
	auditEventRateLimited = "guest.rate_limited"
	auditEventSlugMiss    = "guest.slug.miss"
)

// routeEvents maps the guest routes that change something to the prefix
// of their audit events. A denial on these routes is recorded as
// <prefix>.denied. Read-only routes record only an invalid session.
var routeEvents = map[string]string{
	"act":            "guest.act",
	"where":          "guest.near",
	"password":       "guest.password",
	"device/request": "guest.device",
	"device/decide":  "guest.device",
}

// guestAudit is the audit record a guest request builds as it moves
// through the checks. The first note wins, so one request gives at most
// one record, with the earliest reason.
type guestAudit struct {
	set bool
	rec audit.Record
}

// note sets the request's audit event, unless one is set.
func (g *guestReq) note(event, result, reason string) *audit.Record {
	if !g.audit.set {
		g.audit = guestAudit{set: true, rec: audit.Record{Event: event, Result: result, Reason: reason}}
	}
	return &g.audit.rec
}

// deny notes a denial on the current route. It returns the record, or
// a throwaway one when the route records no denials.
func (g *guestReq) deny(reason string) *audit.Record {
	route := g.r.PathValue("route")
	if prefix, ok := routeEvents[route]; ok {
		return g.note(prefix+".denied", audit.ResultDenied, reason)
	}
	if reason == reasonSessionInvalid {
		return g.note("guest.session.invalid", audit.ResultDenied, reason)
	}
	return &audit.Record{}
}

// rateLimited notes a 429.
func (g *guestReq) rateLimited(reason string) {
	g.note(auditEventRateLimited, audit.ResultDenied, reason)
}

// emitGuest writes the request's audit record, if it has one. It adds
// who: the link (never the slug), the guest, the device and the client.
// Records a client can cause in bulk are throttled per client IP.
func (s *Server) emitGuest(g *guestReq) {
	if !g.audit.set {
		return
	}
	r := g.audit.rec
	if r.Event == auditEventRateLimited || r.Event == auditEventSlugMiss {
		ok, skipped := s.auditThrottle.Allow(r.Event+"|"+g.ipKey(), g.now)
		if !ok {
			return
		}
		r.Count = skipped
	}
	if act, ok := catalog.Lookup(r.Action); ok && act.Continuous && r.Event == "guest.act.allowed" {
		// One record per drag period, per link, client and entity. The
		// next one counts the values left out.
		ok, skipped := s.sliderThrottle.Allow(g.link.ID+"|"+g.ipKey()+"|"+r.EntityID, g.now)
		if !ok {
			return
		}
		r.Count = skipped
	}
	r.Time = g.now
	r.Method = g.r.Method
	r.Route = routeLabel(g.r.URL.Path)
	if g.link.ID != "" {
		r.LinkID, r.LinkName = g.link.ID, g.link.Name
	}
	r.PasswordLabel = g.label
	if g.device != "" {
		r.DeviceID = g.device
		if d, ok := g.link.Device(g.device); ok {
			r.DeviceLabel = d.Label
		}
	}
	r.Device = approval.DescribeDevice(g.r.UserAgent())
	setClient(&r, g.client, peerAddr(g.r.RemoteAddr))
	s.audit.Log(r)
}

// setClient fills the client fields of r.
func setClient(r *audit.Record, c access.Client, peer netip.Addr) {
	if c.IP.IsValid() {
		r.ClientIP = c.IP.String()
	}
	if peer.IsValid() {
		r.Peer = peer.String()
	}
	r.ViaProxy, r.Unresolved = c.ViaProxy, c.Unresolved
}

// actDenial turns an authz.Check error into an audit reason. A pair that
// is granted but fails access is told apart by the network part, which
// does not depend on the location cookie.
func actDenial(err error, grants authz.Grants, req authz.Request, acc accessCheck) string {
	var de *authz.DataError
	if errors.As(err, &de) {
		if de.Key == "" {
			return reasonBadData
		}
		return reasonBadData + ":" + de.Key
	}
	grant, ok := grants[authz.GrantKey{EntityID: req.EntityID, Action: req.Action}]
	if !ok {
		return reasonNotGranted
	}
	if _, ok := grant.Pick(acc.network); !ok {
		return reasonNetwork
	}
	if _, ok := grant.Pick(acc.allow); !ok {
		return reasonNear
	}
	return reasonNotGranted
}

// dataKeys lists the keys of a guest's data, never the values.
func dataKeys(data map[string]any) []string {
	if len(data) == 0 {
		return nil
	}
	return sortedKeys(data)
}

type adminAuditKey struct{}

// adminEvents names the admin writes. Other writes are admin.write.
var adminEvents = map[string]string{
	"POST /api/templates":                    "admin.template.create",
	"PUT /api/templates/{id}":                "admin.template.update",
	"DELETE /api/templates/{id}":             "admin.template.delete",
	"POST /api/links":                        "admin.link.create",
	"PUT /api/links/{id}":                    "admin.link.update",
	"DELETE /api/links/{id}":                 "admin.link.delete",
	"POST /api/links/{id}/revoke":            "admin.link.revoke",
	"POST /api/links/{id}/rotate":            "admin.link.rotate",
	"POST /api/links/{id}/passwords":         "admin.password.add",
	"DELETE /api/links/{id}/passwords/{pid}": "admin.password.remove",
	"DELETE /api/links/{id}/devices/{did}":   "admin.device.revoke",
	"POST /api/links/{id}/devices/reset":     "admin.device.reset",
	"POST /api/devices/pending/{id}":         "admin.device.decide",
	"POST /api/preview":                      "", // reads only
}

// auditAdmin records every admin write after it ran: who (the HA user
// from the ingress headers), what (event, route, ids) and the result.
// It sits behind the peer check, which is what makes those headers
// trustworthy. With DEV_ADMIN_ANY_PEER set, anyone can send them.
func (s *Server) auditAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		rec := &audit.Record{}
		r = r.WithContext(context.WithValue(r.Context(), adminAuditKey{}, rec))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		event, known := adminEvents[r.Pattern]
		if !known {
			event = "admin.write"
		}
		if event == "" {
			return
		}
		rec.Time = s.now()
		rec.Event = event
		rec.Method, rec.Route, rec.Status = r.Method, r.URL.Path, sw.status
		if r.Pattern != "" {
			rec.Route = r.Pattern
		}
		rec.Result = audit.ResultOK
		if sw.status >= 400 {
			rec.Result = audit.ResultFailed
			if rec.Reason == "" {
				rec.Reason = "status_" + strconv.Itoa(sw.status)
			}
		}
		if id := r.PathValue("id"); id != "" {
			switch {
			case strings.Contains(r.Pattern, "/api/links/"):
				rec.LinkID = id
			default:
				rec.Target = id
			}
		}
		for _, k := range []string{"pid", "did"} {
			if v := r.PathValue(k); v != "" {
				rec.Target = v
			}
		}
		if rec.LinkID != "" && rec.LinkName == "" {
			if l, err := s.deps.Links.Get(rec.LinkID); err == nil {
				rec.LinkName = l.Name
			}
		}
		rec.User = haUser(r)
		peer := peerAddr(r.RemoteAddr)
		setClient(rec, access.Client{IP: peer}, peer)
		s.audit.Log(*rec)
	})
}

// adminAudit returns the audit record of an admin write, so a handler
// can add what only it knows (a new link's id, a label). Outside a write
// it returns a throwaway record.
func adminAudit(r *http.Request) *audit.Record {
	if rec, ok := r.Context().Value(adminAuditKey{}).(*audit.Record); ok {
		return rec
	}
	return &audit.Record{}
}

// haUser reads the HA user from the Supervisor ingress headers.
func haUser(r *http.Request) *audit.User {
	u := &audit.User{
		ID:          r.Header.Get("X-Remote-User-Id"),
		Name:        r.Header.Get("X-Remote-User-Name"),
		DisplayName: r.Header.Get("X-Remote-User-Display-Name"),
	}
	if *u == (audit.User{}) {
		return nil
	}
	return u
}

// statusWriter remembers the status a handler wrote.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// getAudit is GET /api/audit?link=<id>&event=<prefix>&limit=<n>: the
// newest records first, at most audit.MaxQueryLimit.
func (s *Server) getAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(n, audit.MaxQueryLimit)
	}
	recs, err := s.audit.Query(audit.Query{LinkID: q.Get("link"), EventPrefix: q.Get("event"), Limit: limit})
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": recs, "file": s.deps.Options.AuditEnabled})
}

// adminReason is the audit reason of a failed admin write.
func adminReason(err error, status int) string {
	switch {
	case errors.Is(err, links.ErrNotFound), errors.Is(err, errNoTemplate), errors.Is(err, approval.ErrUnknownRequest):
		return "not_found"
	case errors.Is(err, links.ErrTooManyDevices):
		return "too_many_devices"
	case errors.Is(err, links.ErrSlugTaken), errors.Is(err, errTemplateDup):
		return "taken"
	case status >= 500:
		return reasonInternal
	}
	return "status_" + strconv.Itoa(status)
}
