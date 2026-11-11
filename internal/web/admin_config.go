package web

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/igrybkov/ha-guest-access/internal/authz"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// settingsView is GET /api/settings: the effective app options. They are
// read-only here; the user edits them in the app's Configuration tab.
type settingsView struct {
	model.Settings
	PublicBaseURL string `json:"public_base_url"`
	// LimitsDisabled is true when every rate limit is off. The UI shows a
	// banner.
	LimitsDisabled bool `json:"limits_disabled"`
	// MAToken says whether a Music Assistant token is set, and whether
	// MA refused it on the last party call. The token itself never
	// leaves the app.
	MAToken struct {
		Set     bool `json:"set"`
		Refused bool `json:"refused"`
	} `json:"ma_token"`
	Tunables tunablesView `json:"tunables"`
	// TimeZone is HA's IANA time zone. The admin page shows and reads
	// every time in it. "" means HA has not reported one, and the page
	// uses the browser's zone.
	TimeZone string `json:"time_zone"`
}

// tunablesView holds the other app options, in plain units.
type tunablesView struct {
	MATimeoutMS              int64          `json:"ma_timeout_ms"`
	MACheckIntervalMin       int64          `json:"ma_check_interval_min"`
	LocationTTLMin           int64          `json:"location_ttl_min"`
	MaxAccuracyM             int            `json:"max_accuracy_m"`
	PasswordWindowPerIPMin   int64          `json:"password_window_per_ip_min"`
	PasswordWindowPerLinkMin int64          `json:"password_window_per_link_min"`
	CustomSlugMinLen         int            `json:"custom_slug_min_len"`
	CustomSlugMaxLen         int            `json:"custom_slug_max_len"`
	CustomSlugWarnLen        int            `json:"custom_slug_warn_len"`
	GuestBodyKiB             int64          `json:"guest_body_kib"`
	AdminBodyKiB             int64          `json:"admin_body_kib"`
	HTTPReadTimeoutS         int64          `json:"http_read_timeout_s"`
	MaxHeaderKiB             int            `json:"max_header_kib"`
	DiagRequests             int            `json:"diagnostics_requests"`
	InventoryMaxAgeS         int64          `json:"inventory_max_age_s"`
	Approval                 approvalTuning `json:"approval"`
	HACallTimeoutS           int64          `json:"ha_call_timeout_s"`
	HAEventTimeoutS          int64          `json:"ha_event_timeout_s"`
	StreamRecheckS           int64          `json:"stream_recheck_s"`
	StreamHeartbeatS         int64          `json:"stream_heartbeat_s"`
	StreamRetryAfterS        int64          `json:"stream_retry_after_s"`
	StreamMaxPerIP           int            `json:"stream_max_per_ip"`
	AuditEnabled             bool           `json:"audit_enabled"`
	AuditMaxFileKiB          int64          `json:"audit_max_file_kib"`
	AuditMaxDenialsPerMin    int            `json:"audit_max_denials_per_min"`
}

type approvalTuning struct {
	PendingTTLMin    int64 `json:"pending_ttl_min"`
	MaxPending       int   `json:"max_pending"`
	MaxDevices       int   `json:"max_devices"`
	EventsPerHour    int   `json:"events_per_hour"`
	TouchThrottleMin int64 `json:"touch_throttle_min"`
}

func newSettingsView(o config.Options, maRefused bool) settingsView {
	v := settingsView{Settings: o.Settings, PublicBaseURL: o.PublicBaseURL, LimitsDisabled: !o.LimitsEnabled}
	v.MAToken.Set = o.MusicAssistantToken != ""
	v.MAToken.Refused = maRefused
	v.Tunables = tunablesView{
		MATimeoutMS:              o.MATimeout.Milliseconds(),
		MACheckIntervalMin:       int64(o.MACheckInterval.Minutes()),
		LocationTTLMin:           int64(o.NearTTL.Minutes()),
		MaxAccuracyM:             o.MaxAccuracyM,
		PasswordWindowPerIPMin:   int64(o.PasswordWindowPerIP.Minutes()),
		PasswordWindowPerLinkMin: int64(o.PasswordWindowPerLink.Minutes()),
		CustomSlugMinLen:         o.CustomSlugMinLen,
		CustomSlugMaxLen:         o.CustomSlugMaxLen,
		CustomSlugWarnLen:        o.CustomSlugWarnLen,
		GuestBodyKiB:             o.GuestBodyBytes >> 10,
		AdminBodyKiB:             o.AdminBodyBytes >> 10,
		HTTPReadTimeoutS:         int64(o.HTTPReadTimeout.Seconds()),
		MaxHeaderKiB:             o.MaxHeaderBytes >> 10,
		DiagRequests:             o.DiagRequests,
		InventoryMaxAgeS:         int64(o.InventoryMaxAge.Seconds()),
		Approval: approvalTuning{
			PendingTTLMin:    int64(o.Approval.PendingTTL.Minutes()),
			MaxPending:       o.Approval.MaxPending,
			MaxDevices:       o.Approval.MaxDevices,
			EventsPerHour:    o.Approval.EventsPerHour,
			TouchThrottleMin: int64(o.Approval.TouchThrottle.Minutes()),
		},
		HACallTimeoutS:        int64(o.HACallTimeout.Seconds()),
		HAEventTimeoutS:       int64(o.HAEventTimeout.Seconds()),
		StreamRecheckS:        int64(o.StreamCheck.Seconds()),
		StreamHeartbeatS:      int64(o.StreamHeartbeat.Seconds()),
		StreamRetryAfterS:     int64(o.StreamRetry.Seconds()),
		StreamMaxPerIP:        o.StreamMaxPerIP,
		AuditEnabled:          o.AuditEnabled,
		AuditMaxFileKiB:       o.AuditMaxFileBytes >> 10,
		AuditMaxDenialsPerMin: o.AuditMaxDenialsPerMin,
	}
	return v
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	v := newSettingsView(s.deps.Options, s.maRefused.Load())
	// The settings still load when HA is away; the zone is then "".
	ctx, cancel := context.WithTimeout(r.Context(), s.deps.Options.HACallTimeout)
	defer cancel()
	if inv, err := s.deps.Inventory.Inventory(ctx); err == nil {
		v.TimeZone = inv.TimeZone
	}
	writeJSON(w, http.StatusOK, v)
}

// templatesView is GET /api/templates. Problems lists what does not
// validate in templates.yaml (a hand edit); those templates grant
// nothing until fixed. Error is a load error, such as a file that does
// not parse.
type templatesView struct {
	Templates []model.Template `json:"templates"`
	Problems  []model.Problem  `json:"problems,omitempty"`
	Error     string           `json:"error,omitempty"`
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := s.deps.Templates.Templates()
	v := templatesView{Templates: ts.Templates}
	if verr := asValidation(err); verr != nil {
		v.Problems = verr.Problems
	} else if err != nil {
		v.Error = err.Error()
	}
	if p := asValidation(s.checkTemplates(r.Context(), ts)); p != nil {
		v.Problems = append(v.Problems, p.Problems...)
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var t model.Template
	if !s.decodeAdmin(w, r, &t) {
		return
	}
	err := s.deps.Templates.Update(func(ts *model.Templates) error {
		if _, ok := ts.Find(t.ID); ok {
			return errTemplateDup
		}
		ts.Templates = append(ts.Templates, t)
		return s.checkTemplate(r.Context(), t)
	})
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) updateTemplate(w http.ResponseWriter, r *http.Request) {
	var t model.Template
	if !s.decodeAdmin(w, r, &t) {
		return
	}
	id := r.PathValue("id")
	if t.ID == "" {
		t.ID = id
	}
	if t.ID != id {
		writeError(w, http.StatusBadRequest, "the id can't change; make a new template instead")
		return
	}
	err := s.deps.Templates.Update(func(ts *model.Templates) error {
		i := slices.IndexFunc(ts.Templates, func(x model.Template) bool { return x.ID == id })
		if i < 0 {
			return errNoTemplate
		}
		ts.Templates[i] = t
		return s.checkTemplate(r.Context(), t)
	})
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// checkTemplate validates t alone: its shape, then the policy. Problem
// paths point into t, and other templates never block its save.
func (s *Server) checkTemplate(ctx context.Context, t model.Template) error {
	if err := t.Validate(); err != nil {
		return err
	}
	return s.checkTemplates(ctx, model.Templates{Templates: []model.Template{t}})
}

// deleteTemplate removes a template. Links that name it lose its rules;
// the reply lists them so the UI can say so.
func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.deps.Templates.Update(func(ts *model.Templates) error {
		n := len(ts.Templates)
		ts.Templates = slices.DeleteFunc(ts.Templates, func(t model.Template) bool { return t.ID == id })
		if len(ts.Templates) == n {
			return errNoTemplate
		}
		return nil
	})
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	used := []string{}
	if ls, err := s.deps.Links.List(); err == nil {
		for _, l := range ls.Links {
			if slices.Contains(l.TemplateIDs, id) {
				used = append(used, l.Name)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "used_by": used})
}

// previewBody is POST /api/preview: one of a template or a link draft.
type previewBody struct {
	Template *model.Template `json:"template,omitempty"`
	Link     *model.Link     `json:"link,omitempty"`
}

// previewView is the reply: what each rule selects, what the whole
// grants, and what is wrong.
type previewView struct {
	Rules    []ruleView      `json:"rules"`
	Grants   []grantView     `json:"grants"`
	Warnings []warningView   `json:"warnings"`
	Problems []model.Problem `json:"problems"`
}

type ruleView struct {
	Template string   `json:"template"` // "" for a link's extra rule
	Rule     int      `json:"rule"`
	Entities []string `json:"entities"`
}

type grantView struct {
	EntityID string       `json:"entity_id"`
	Name     string       `json:"name"`
	Action   string       `json:"action"`
	Sources  []sourceView `json:"sources"`
}

type sourceView struct {
	Template string       `json:"template"`
	Rule     int          `json:"rule"`
	Access   model.Access `json:"access"`
}

type warningView struct {
	Template string `json:"template"`
	Rule     int    `json:"rule"` // -1: the whole template
	Message  string `json:"message"`
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	var b previewBody
	if !s.decodeAdmin(w, r, &b) {
		return
	}
	if (b.Template == nil) == (b.Link == nil) {
		writeError(w, http.StatusBadRequest, "send one of template or link")
		return
	}
	inv, err := s.deps.Inventory.Inventory(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Home Assistant is not reachable: "+err.Error())
		return
	}
	stored, _ := s.deps.Templates.Templates()
	ts := slices.Clone(stored.Templates)
	var link model.Link
	v := previewView{Rules: []ruleView{}, Grants: []grantView{}, Warnings: []warningView{}, Problems: []model.Problem{}}
	if t := b.Template; t != nil {
		// Preview the draft in place of the stored template.
		ts = slices.DeleteFunc(ts, func(x model.Template) bool { return x.ID == t.ID })
		ts = append(ts, *t)
		link = model.Link{Name: "preview", TemplateIDs: []string{t.ID}}
		v.Problems = append(v.Problems, problemsOf(t.Validate())...)
		v.Problems = append(v.Problems, problemsOf(s.checkTemplates(r.Context(), model.Templates{Templates: []model.Template{*t}}))...)
	} else {
		link = *b.Link
		v.Problems = append(v.Problems, problemsOf(s.checkLinkPolicy(r.Context(), link, model.Templates{Templates: ts}))...)
	}
	// Templates that do not validate grant nothing, as on the guest side.
	valid := slices.DeleteFunc(slices.Clone(ts), func(t model.Template) bool { return t.Validate() != nil })
	for _, id := range link.TemplateIDs {
		if t, ok := (model.Templates{Templates: valid}).Find(id); ok {
			for i, rule := range t.Rules {
				v.Rules = append(v.Rules, ruleView{Template: id, Rule: i, Entities: nonNil(authz.RuleEntities(rule, inv))})
			}
		}
	}
	for i, rule := range link.Extra {
		v.Rules = append(v.Rules, ruleView{Rule: i, Entities: nonNil(authz.RuleEntities(rule, inv))})
	}
	grants, warns := authz.Resolve(link, valid, inv)
	v.Grants = grantList(grants, inv)
	for _, w := range warns {
		v.Warnings = append(v.Warnings, warningView{Template: w.Template, Rule: w.Rule, Message: w.Message})
	}
	writeJSON(w, http.StatusOK, v)
}

// grantList returns grants sorted by entity, then action.
func grantList(g authz.Grants, inv model.Inventory) []grantView {
	out := make([]grantView, 0, len(g))
	for _, gr := range g {
		gv := grantView{EntityID: gr.EntityID, Name: inv.Entities[gr.EntityID].Name, Action: gr.Action, Sources: []sourceView{}}
		for _, src := range gr.Sources {
			gv.Sources = append(gv.Sources, sourceView{Template: src.Template, Rule: src.Rule, Access: src.Access})
		}
		out = append(out, gv)
	}
	slices.SortFunc(out, func(a, b grantView) int {
		if a.EntityID != b.EntityID {
			return compare(a.EntityID, b.EntityID)
		}
		return compare(a.Action, b.Action)
	})
	return out
}

func asValidation(err error) *model.ValidationError {
	var v *model.ValidationError
	if err != nil && errors.As(err, &v) {
		return v
	}
	return nil
}

// problemsOf returns the problems of a validation error. Other errors
// become one problem with no path.
func problemsOf(err error) []model.Problem {
	if err == nil {
		return nil
	}
	if v := asValidation(err); v != nil {
		return v.Problems
	}
	return []model.Problem{{Message: err.Error()}}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
