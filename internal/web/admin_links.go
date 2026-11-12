package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/approval"
	"github.com/igrybkov/ha-guest-access/internal/authz"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/model"
	"github.com/igrybkov/ha-guest-access/internal/policy"
)

// linkView is one link in the admin API. Password hashes never appear:
// model.LinkPassword.Hash is not in JSON.
type linkView struct {
	model.Link
	Status string `json:"status"`
	// Path is the guest path, /g/<slug>. URL is the full guest URL, or ""
	// while public_base_url is not set.
	Path string `json:"path"`
	URL  string `json:"url"`
	// Warnings come from resolving the link now: a missing template, a
	// rule that selects nothing, network: local while local access is
	// off.
	Warnings []warningView `json:"warnings"`
	// Problems are why the stored link does not validate (a hand edit of
	// links.yaml). Guests get not found for it until it is fixed.
	Problems []model.Problem `json:"problems"`
	// SlugWarning is set when the slug is shorter than
	// links.custom_slug_warn_len. Only a custom slug can be.
	SlugWarning string `json:"slug_warning,omitempty"`
	// Passwords and Devices shadow the link's fields so they are always
	// lists, never left out. Approval comes from model.Link.
	Passwords []model.LinkPassword `json:"passwords"`
	Devices   []model.LinkDevice   `json:"devices"`
	// Pending are the link's device requests that wait for a decision.
	Pending []approval.Request `json:"pending"`
}

func (s *Server) linkView(l model.Link, inv *model.Inventory, ts []model.Template) linkView {
	v := linkView{
		Link: l, Status: l.Status(s.now()), Path: guestPath(l.Slug), URL: s.guestURL(l.Slug), Warnings: []warningView{},
		Problems:  []model.Problem{},
		Passwords: nonNil(l.Passwords), Devices: nonNil(l.Devices), Pending: s.devices.Pending(l.ID),
	}
	var ve *model.ValidationError
	if err := policy.ServableLink(l); errors.As(err, &ve) {
		v.Problems = ve.Problems
	}
	if len(l.Slug) < s.deps.Options.CustomSlugWarnLen {
		v.SlugWarning = shortSlugWarning
	}
	if inv != nil {
		_, warns := authz.Resolve(l, ts, *inv)
		for _, w := range warns {
			v.Warnings = append(v.Warnings, warningView{Template: w.Template, Rule: w.Rule, Message: w.Message})
		}
	}
	return v
}

// shortSlugWarning is the admin warning on a short custom slug.
const shortSlugWarning = "This custom URL is short. Short URLs are easier to guess. A random URL is safest."

func guestPath(slug string) string { return "/g/" + slug }

// guestURL returns the full guest URL, or "" without a public base URL.
func (s *Server) guestURL(slug string) string {
	base := strings.TrimRight(s.deps.Options.PublicBaseURL, "/")
	if base == "" {
		return ""
	}
	return base + guestPath(slug)
}

// linkViews builds views with warnings. Without HA, warnings are left
// out rather than failing the list.
func (s *Server) linkViews(r *http.Request, ls []model.Link) []linkView {
	var inv *model.Inventory
	if i, err := s.deps.Inventory.Inventory(r.Context()); err == nil {
		inv = &i
	}
	ts := s.validTemplates()
	out := make([]linkView, 0, len(ls))
	for _, l := range ls {
		out = append(out, s.linkView(l, inv, ts))
	}
	return out
}

func (s *Server) listLinks(w http.ResponseWriter, r *http.Request) {
	ls, err := s.deps.Links.List()
	if err != nil && len(ls.Links) == 0 {
		s.writeAdminErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": s.linkViews(r, ls.Links)})
}

func (s *Server) getLink(w http.ResponseWriter, r *http.Request) {
	l, err := s.deps.Links.Get(r.PathValue("id"))
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.linkViews(r, []model.Link{l})[0])
}

// writeLink answers with one link after a change.
func (s *Server) writeLink(w http.ResponseWriter, r *http.Request, status int, l model.Link) {
	writeJSON(w, status, s.linkViews(r, []model.Link{l})[0])
}

// checkLink runs the policy on a link draft. On create the draft has no
// id or slug yet.
func (s *Server) checkLink(ctx context.Context, l model.Link) error {
	ts, _ := s.deps.Templates.Templates()
	return s.checkLinkPolicy(ctx, l, ts)
}

// createLink takes a model.Link. Only the editable fields count (name,
// note, valid_from, valid_until, inactive, templates, extra, one_time);
// "slug", if set, asks for a custom slug.
func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	var draft model.Link
	if !s.decodeAdmin(w, r, &draft) {
		return
	}
	if err := s.checkLink(r.Context(), draft); err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	l, err := s.deps.Links.Create(draft, draft.Slug)
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	s.log.Info("link created", "link", l.ID, "name", l.DisplayName())
	rec := adminAudit(r)
	rec.LinkID, rec.LinkName = l.ID, l.DisplayName()
	s.writeLink(w, r, http.StatusCreated, l)
}

func (s *Server) updateLink(w http.ResponseWriter, r *http.Request) {
	var edit model.Link
	if !s.decodeAdmin(w, r, &edit) {
		return
	}
	if err := s.checkLink(r.Context(), edit); err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	l, err := s.deps.Links.Update(r.PathValue("id"), edit)
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	s.writeLink(w, r, http.StatusOK, l)
}

func (s *Server) deleteLink(w http.ResponseWriter, r *http.Request) {
	if l, err := s.deps.Links.Get(r.PathValue("id")); err == nil {
		adminAudit(r).LinkName = l.DisplayName()
	}
	if err := s.deps.Links.Delete(r.PathValue("id")); err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	s.log.Info("link deleted", "link", r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// deactivateLink is POST /api/links/{id}/deactivate with an empty or {}
// body. Guests lose access until the link is activated again.
func (s *Server) deactivateLink(w http.ResponseWriter, r *http.Request) {
	var b struct{}
	if r.ContentLength != 0 && !s.decodeAdmin(w, r, &b) {
		return
	}
	l, err := s.deps.Links.Deactivate(r.PathValue("id"))
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	s.log.Info("link deactivated", "link", l.ID)
	rec := adminAudit(r)
	rec.LinkID, rec.LinkName = l.ID, l.DisplayName()
	s.writeLink(w, r, http.StatusOK, l)
}

// activateBody is POST /api/links/{id}/activate. Rotate gives the link a
// new URL: Slug, or a random one when Slug is empty. A window field that
// is left out keeps the saved value. valid_from may be null to clear it.
type activateBody struct {
	Rotate     bool            `json:"rotate"`
	Slug       string          `json:"slug"`
	ValidFrom  json.RawMessage `json:"valid_from"`
	ValidUntil json.RawMessage `json:"valid_until"`
}

// activateLink turns a link on in one write. The window must end in the
// future (409 otherwise) and pass the same checks as a save (400). The
// audit record lists what changed, never the slug.
func (s *Server) activateLink(w http.ResponseWriter, r *http.Request) {
	var b activateBody
	if r.ContentLength != 0 && !s.decodeAdmin(w, r, &b) {
		return
	}
	a := links.Activation{NewURL: b.Rotate, CustomSlug: b.Slug}
	var err error
	if a.SetFrom, a.ValidFrom, err = optionalTime(b.ValidFrom); err != nil {
		s.writeAdminErr(w, r, timeProblem("valid_from"))
		return
	}
	if a.SetUntil, a.ValidUntil, err = optionalTime(b.ValidUntil); err != nil {
		s.writeAdminErr(w, r, timeProblem("valid_until"))
		return
	}
	if b.Slug != "" && !b.Rotate {
		s.writeAdminErr(w, r, &model.ValidationError{Problems: []model.Problem{{Path: "slug", Message: "needs rotate"}}})
		return
	}
	id := r.PathValue("id")
	rec := adminAudit(r)
	rec.LinkID = id
	// The same policy checks as a save. Activate never changes rules, so
	// a check of the stored link holds for the result.
	cur, err := s.deps.Links.Get(id)
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	rec.LinkName = cur.DisplayName()
	if err := s.checkLink(r.Context(), cur); err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	l, changed, err := s.deps.Links.Activate(id, a, s.now())
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	s.log.Info("link activated", "link", l.ID, "changed", changed)
	rec.LinkName, rec.DataKeys = l.DisplayName(), changed
	s.writeLink(w, r, http.StatusOK, l)
}

// optionalTime reads one optional RFC 3339 time. Left out gives set
// false. null gives set true and a zero time.
func optionalTime(raw json.RawMessage) (set bool, t time.Time, err error) {
	if raw == nil {
		return false, time.Time{}, nil
	}
	if string(raw) == "null" {
		return true, time.Time{}, nil
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return false, time.Time{}, err
	}
	return true, t, nil
}

func timeProblem(path string) error {
	return &model.ValidationError{Problems: []model.Problem{{Path: path, Message: "must be an RFC 3339 time"}}}
}

// rotateBody is POST /api/links/{id}/rotate. An empty or missing slug
// picks a random one.
type rotateBody struct {
	Slug string `json:"slug"`
}

func (s *Server) rotateLink(w http.ResponseWriter, r *http.Request) {
	var b rotateBody
	if r.ContentLength != 0 && !s.decodeAdmin(w, r, &b) {
		return
	}
	l, err := s.deps.Links.RotateSlug(r.PathValue("id"), b.Slug)
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	s.log.Info("link slug rotated", "link", l.ID)
	s.writeLink(w, r, http.StatusOK, l)
}

// passwordInput is POST /api/links/{id}/passwords. The password is
// hashed as typed and never stored or logged.
type passwordInput struct {
	Label    string `json:"label"`
	Password string `json:"password"`
}

func (s *Server) addPassword(w http.ResponseWriter, r *http.Request) {
	var b passwordInput
	if !s.decodeAdmin(w, r, &b) {
		return
	}
	hash, err := access.HashPassword(b.Password)
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	pw, err := s.deps.Links.AddPassword(r.PathValue("id"), b.Label, hash)
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	s.log.Info("link password added", "link", r.PathValue("id"), "label", pw.Label)
	rec := adminAudit(r)
	rec.Target, rec.PasswordLabel = pw.ID, pw.Label
	writeJSON(w, http.StatusCreated, pw)
}

func (s *Server) removePassword(w http.ResponseWriter, r *http.Request) {
	if l, err := s.deps.Links.Get(r.PathValue("id")); err == nil {
		if pw, ok := l.Password(r.PathValue("pid")); ok {
			adminAudit(r).PasswordLabel = pw.Label
		}
	}
	if err := s.deps.Links.RemovePassword(r.PathValue("id"), r.PathValue("pid")); err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) linkURL(w http.ResponseWriter, r *http.Request) {
	l, err := s.deps.Links.Get(r.PathValue("id"))
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": guestPath(l.Slug), "url": s.guestURL(l.Slug)})
}

// qrTarget returns the full guest URL of the link for a QR code.
func (s *Server) qrTarget(w http.ResponseWriter, r *http.Request) (string, bool) {
	l, err := s.deps.Links.Get(r.PathValue("id"))
	if err != nil {
		s.writeAdminErr(w, r, err)
		return "", false
	}
	u := s.guestURL(l.Slug)
	if u == "" {
		s.writeAdminErr(w, r, errNoBaseURL)
		return "", false
	}
	return u, true
}

func (s *Server) linkQRSVG(w http.ResponseWriter, r *http.Request) {
	u, ok := s.qrTarget(w, r)
	if !ok {
		return
	}
	svg, err := qrSVG(u)
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(svg)
}

func (s *Server) linkQRPNG(w http.ResponseWriter, r *http.Request) {
	u, ok := s.qrTarget(w, r)
	if !ok {
		return
	}
	png, err := qrPNG(u)
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}
