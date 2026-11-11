package web

import (
	"net/http"

	"github.com/igrybkov/ha-guest-access/internal/approval"
)

// pendingView is one pending device request in the admin API.
type pendingView struct {
	approval.Request
	LinkName string `json:"link_name"`
}

// listPending is GET /api/devices/pending[?link=<id>]: the pending device
// requests, oldest first.
func (s *Server) listPending(w http.ResponseWriter, r *http.Request) {
	reqs := s.devices.Pending(r.URL.Query().Get("link"))
	names := map[string]string{}
	if ls, err := s.deps.Links.List(); err == nil {
		for _, l := range ls.Links {
			names[l.ID] = l.Name
		}
	}
	out := make([]pendingView, 0, len(reqs))
	for _, req := range reqs {
		out = append(out, pendingView{Request: req, LinkName: names[req.LinkID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

// decideBody is POST /api/devices/pending/{id}.
type decideBody struct {
	Approve *bool `json:"approve"`
}

// decidePending approves or denies a pending request. It answers
// {"status":"approved"|"denied"}, 404 for an unknown or expired request,
// and 409 when the link has the most devices.
func (s *Server) decidePending(w http.ResponseWriter, r *http.Request) {
	var b decideBody
	if !s.decodeAdmin(w, r, &b) {
		return
	}
	if b.Approve == nil {
		writeError(w, http.StatusBadRequest, "approve must be true or false")
		return
	}
	rec := adminAudit(r)
	rec.RequestID, rec.Action = r.PathValue("id"), "deny"
	if *b.Approve {
		rec.Action = "approve"
	}
	if err := s.devices.Decide(r.PathValue("id"), *b.Approve); err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	status := "denied"
	if *b.Approve {
		status = "approved"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

// removeDevice is DELETE /api/links/{id}/devices/{did}. The device's
// cookie fails on its next request, and its open streams end.
func (s *Server) removeDevice(w http.ResponseWriter, r *http.Request) {
	if l, err := s.deps.Links.Get(r.PathValue("id")); err == nil {
		if d, ok := l.Device(r.PathValue("did")); ok {
			adminAudit(r).DeviceLabel = d.Label
		}
	}
	if err := s.devices.RemoveDevice(r.PathValue("id"), r.PathValue("did")); err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// resetDevices is POST /api/links/{id}/devices/reset. It removes every
// device and pending request of the link and answers with the link.
func (s *Server) resetDevices(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.devices.ResetDevices(id); err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	l, err := s.deps.Links.Get(id)
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	s.writeLink(w, r, http.StatusOK, l)
}
