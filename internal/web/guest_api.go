package web

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/authz"
	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/ha"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// HA events the guest side fires.
const (
	eventAction  = "guest_access_action"
	eventLockout = "guest_access_lockout"
)

func (s *Server) guestPage(g *guestReq) { s.assets.servePage(g.w) }

// passwordBody is POST /g/{slug}/password.
type passwordBody struct {
	Password string `json:"password"`
}

// guestPassword checks a password and sets the session cookie. The guard
// slot is taken only here, right before the real check.
func (s *Server) guestPassword(g *guestReq) {
	var body passwordBody
	if !s.decodeGuest(g, &body) {
		return
	}
	if len(g.link.Passwords) == 0 || body.Password == "" || len(body.Password) > access.MaxPasswordLen {
		writeError(g.w, http.StatusBadRequest, msgBadRequest)
		return
	}
	ip := g.client.IP
	if ok, retry := g.lim.guard.Allow(g.link.ID, ip, g.now); !ok {
		tooMany(g.w, retry)
		return
	}
	id, ok := access.MatchPassword(g.link.Passwords, body.Password)
	if !ok {
		if g.lim.guard.Fail(g.link.ID, ip, g.now) {
			s.log.Warn("password form locked", "link", g.link.ID, "client", g.ipKey())
			s.fire(g.r.Context(), eventLockout, map[string]any{
				"link_id": g.link.ID, "link_name": g.link.Name, "ip": g.ipKey(),
			})
		}
		writeError(g.w, http.StatusForbidden, msgWrongPass)
		return
	}
	pw, _ := g.link.Password(id)
	setCookie(g, sessionCookie, s.deps.Signer.SignSession(g.link.ID, id, g.link.ValidUntil), g.link.ValidUntil)
	s.log.Info("guest signed in", "link", g.link.ID, "guest", pw.Label)
	writeJSON(g.w, http.StatusOK, map[string]bool{"ok": true})
}

// decodeGuest decodes a guest JSON body, or answers 400 or 413.
func (s *Server) decodeGuest(g *guestReq, v any) bool {
	err := decodeBody(g.w, g.r, s.deps.Options.GuestBodyBytes, v)
	switch {
	case errors.Is(err, errBodyTooLarge):
		writeError(g.w, http.StatusRequestEntityTooLarge, msgTooLarge)
	case err != nil:
		writeError(g.w, http.StatusBadRequest, msgBadRequest)
	}
	return err == nil
}

// accessCheck evaluates rule access for one request. See allow.
type accessCheck struct {
	localOn bool   // Settings.LocalAccess.Enabled
	local   bool   // Client.Local(networks)
	near    uint32 // distance from the location cookie, in meters
	hasNear bool
}

func (s *Server) accessFor(g *guestReq) accessCheck {
	a := accessCheck{localOn: g.st.LocalAccess.Enabled, local: g.client.Local(g.nets)}
	if c, err := g.r.Cookie(access.LinkCookie(nearCookie, g.link.ID)); err == nil {
		a.near, a.hasNear = s.deps.Signer.VerifyNear(g.link.ID, c.Value, g.now)
	}
	return a
}

// network checks the network part of acc. "local" passes only while
// local access is on and the client is local; an unresolved client is
// never local.
func (a accessCheck) network(acc model.Access) bool {
	return !acc.Local() || (a.localOn && a.local)
}

// location checks the near-home part of acc. A local client counts as
// near only while local access is on (brief decision 3); otherwise the
// location cookie must hold a distance within the radius.
func (a accessCheck) location(acc model.Access) bool {
	if acc.NearHomeM <= 0 {
		return true
	}
	if a.localOn && a.local {
		return true
	}
	return a.hasNear && int64(a.near) <= int64(acc.NearHomeM)
}

// allow is the authz.AccessFunc of the request.
func (a accessCheck) allow(acc model.Access) bool { return a.network(acc) && a.location(acc) }

// resolveLink returns the link's grants against the current templates
// and inventory. Templates that do not validate are skipped.
func (s *Server) resolveLink(ctx context.Context, link model.Link) (authz.Grants, []model.Template, error) {
	inv, err := s.deps.Inventory.Inventory(ctx)
	if err != nil {
		return nil, nil, err
	}
	ts := s.validTemplates()
	grants, _ := authz.Resolve(link, ts, inv)
	return grants, ts, nil
}

// validTemplates returns the templates that pass Validate. A bad template
// grants nothing; the admin API shows why.
func (s *Server) validTemplates() []model.Template {
	ts, err := s.deps.Templates.Templates()
	if err != nil {
		s.log.Debug("templates", "err", err)
	}
	out := make([]model.Template, 0, len(ts.Templates))
	for _, t := range ts.Templates {
		if t.Validate() == nil {
			out = append(out, t)
		}
	}
	return out
}

// Views sent by GET /g/{slug}/state.
type (
	stateView struct {
		Link   guestLink           `json:"link"`
		Guest  model.GuestSettings `json:"guest"`
		Groups []groupView         `json:"groups"`
		// Entities holds every entity with at least one action shown.
		Entities map[string]*entityView `json:"entities"`
		// States holds the filtered state of entities with an action that
		// passes now. Entities whose actions all need location have none.
		States map[string]model.State `json:"states"`
		Party  bool                   `json:"party"`
	}
	guestLink struct {
		ValidUntil time.Time `json:"valid_until"`
		OneTime    bool      `json:"one_time"`
	}
	groupView struct {
		ID       string   `json:"id"` // template id; "" for the link's own rules
		Name     string   `json:"name"`
		Icon     string   `json:"icon,omitempty"`
		Entities []string `json:"entities"`
	}
	entityView struct {
		Name    string       `json:"name"`
		Domain  string       `json:"domain"`
		Actions []actionView `json:"actions"`
	}
	actionView struct {
		ID     string                   `json:"id"`
		UI     string                   `json:"ui"`
		Fields map[string]catalog.Field `json:"fields,omitempty"`
		// NeedsLocation means the action passes once the guest shares a
		// location near enough (POST /where).
		NeedsLocation bool `json:"needs_location,omitempty"`
		// Used means a one-time link already used it.
		Used bool `json:"used,omitempty"`
	}
)

// guestPlan is what the link offers this request right now.
type guestPlan struct {
	view stateView
	// passing holds entities with an action that passes now. Only these
	// get state.
	passing map[string]bool
}

// plan works out the controls a guest may see now: every granted pair
// whose source passes access, plus pairs that pass once the guest shares
// a location. Pairs denied by the network are left out.
func (s *Server) plan(ctx context.Context, g *guestReq) (guestPlan, error) {
	inv, err := s.deps.Inventory.Inventory(ctx)
	if err != nil {
		return guestPlan{}, err
	}
	ts := s.validTemplates()
	grants, _ := authz.Resolve(g.link, ts, inv)
	acc := s.accessFor(g)

	p := guestPlan{
		view: stateView{
			Link:     guestLink{ValidUntil: g.link.ValidUntil, OneTime: g.link.OneTime},
			Guest:    g.st.Guest,
			Groups:   []groupView{},
			Entities: map[string]*entityView{},
			States:   map[string]model.State{},
		},
		passing: map[string]bool{},
	}
	groupOf := map[string]int{} // template id -> index in Groups
	for _, id := range g.link.TemplateIDs {
		i := slices.IndexFunc(ts, func(t model.Template) bool { return t.ID == id })
		if _, seen := groupOf[id]; i < 0 || seen {
			continue
		}
		groupOf[id] = len(p.view.Groups)
		p.view.Groups = append(p.view.Groups, groupView{ID: id, Name: ts[i].Name, Icon: ts[i].Icon})
	}

	keys := make([]authz.GrantKey, 0, len(grants))
	for k := range grants {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b authz.GrantKey) int {
		if a.EntityID != b.EntityID {
			return compare(a.EntityID, b.EntityID)
		}
		return compare(a.Action, b.Action)
	})
	entityGroup := map[string]int{}
	for _, k := range keys {
		grant := grants[k]
		src, ok := grant.Pick(acc.allow)
		needLoc := false
		if !ok {
			if src, ok = grant.Pick(acc.network); !ok {
				continue
			}
			needLoc = true
		}
		act, ok := catalog.Lookup(k.Action)
		if !ok {
			continue
		}
		e := p.view.Entities[k.EntityID]
		if e == nil {
			e = &entityView{Name: inv.Entities[k.EntityID].Name, Domain: model.Domain(k.EntityID)}
			if e.Name == "" {
				e.Name = k.EntityID
			}
			p.view.Entities[k.EntityID] = e
		}
		_, used := g.link.UsedAt(k.EntityID, k.Action)
		e.Actions = append(e.Actions, actionView{
			ID: act.ID, UI: act.UI, Fields: act.Data, NeedsLocation: needLoc, Used: g.link.OneTime && used,
		})
		if !needLoc {
			p.passing[k.EntityID] = true
		}
		gi, ok := groupOf[src.Template]
		if !ok { // the link's own rules
			gi = len(p.view.Groups)
			groupOf[src.Template] = gi
			p.view.Groups = append(p.view.Groups, groupView{})
		}
		if cur, ok := entityGroup[k.EntityID]; !ok || gi < cur {
			entityGroup[k.EntityID] = gi
		}
	}
	for _, id := range sortedKeys(entityGroup) {
		gi := entityGroup[id]
		p.view.Groups[gi].Entities = append(p.view.Groups[gi].Entities, id)
	}
	p.view.Groups = slices.DeleteFunc(p.view.Groups, func(gv groupView) bool { return len(gv.Entities) == 0 })
	p.view.Party = s.partyAllowed(g, ts, acc)
	return p, nil
}

// partyAllowed reports whether the guest page shows the party button:
// Music Assistant is set up, and a template of the link with party: true
// has a rule whose access passes now. It never calls MA.
func (s *Server) partyAllowed(g *guestReq, ts []model.Template, acc accessCheck) bool {
	if !s.deps.Options.MusicAssistantEnabled() {
		return false
	}
	for _, id := range g.link.TemplateIDs {
		i := slices.IndexFunc(ts, func(t model.Template) bool { return t.ID == id })
		if i < 0 || !ts[i].Party {
			continue
		}
		for r := range ts[i].Rules {
			if acc.allow(ts[i].RuleAccess(r)) {
				return true
			}
		}
	}
	return false
}

func (s *Server) guestState(g *guestReq) {
	p, err := s.plan(g.r.Context(), g)
	if err != nil {
		s.log.Warn("guest state: inventory", "err", err)
		writeError(g.w, http.StatusServiceUnavailable, msgHAFailed)
		return
	}
	if len(p.passing) > 0 {
		states, err := s.deps.Inventory.States(g.r.Context(), p.passing)
		if err != nil {
			s.log.Warn("guest state: states", "err", err)
		}
		for id, st := range states {
			if p.passing[id] {
				p.view.States[id] = catalog.FilterState(st)
			}
		}
	}
	writeJSON(g.w, http.StatusOK, p.view)
}

// decodeAct reads an /act body. Data numbers stay float64.
func (s *Server) decodeAct(g *guestReq) (authz.Request, bool) {
	var req authz.Request
	return req, s.decodeGuest(g, &req)
}

// guestAct runs one action:
//
//  1. authz.Check: the exact pair is granted, a source passes access, the
//     data fits the catalog.
//  2. links.Consume, before the HA call, so a one-time pair is used at
//     most once even under parallel requests.
//  3. The HA call, with a target of exactly one entity. On error the use
//     is refunded.
//  4. The guest_access_action event.
func (s *Server) guestAct(g *guestReq) {
	req, ok := s.decodeAct(g)
	if !ok {
		return
	}
	ctx := g.r.Context()
	grants, _, err := s.resolveLink(ctx, g.link)
	if err != nil {
		s.log.Warn("guest act: inventory", "err", err)
		writeError(g.w, http.StatusServiceUnavailable, msgHAFailed)
		return
	}
	var st *model.State
	if _, granted := grants[authz.GrantKey{EntityID: req.EntityID, Action: req.Action}]; granted && len(req.Data) > 0 {
		// Data bounds may come from the entity's attributes.
		states, err := s.deps.Inventory.States(ctx, map[string]bool{req.EntityID: true})
		if x, ok := states[req.EntityID]; err == nil && ok {
			st = &x
		}
	}
	src, call, err := authz.Check(grants, req, st, s.accessFor(g).allow)
	switch {
	case errors.Is(err, authz.ErrBadData):
		writeError(g.w, http.StatusBadRequest, msgBadRequest)
		return
	case err != nil:
		writeError(g.w, http.StatusForbidden, msgForbidden)
		return
	}

	refund, err := s.deps.Links.Consume(g.link.ID, call.EntityID, req.Action, g.now)
	switch {
	case errors.Is(err, links.ErrUsed):
		writeError(g.w, http.StatusConflict, msgUsed)
		return
	case errors.Is(err, links.ErrInactive), errors.Is(err, links.ErrNotFound):
		writeError(g.w, http.StatusGone, msgGone)
		return
	case err != nil:
		s.log.Error("guest act: record use", "link", g.link.ID, "err", err)
		writeError(g.w, http.StatusInternalServerError, msgInternal)
		return
	}

	// A guest who closes the tab must not cancel a call that may already
	// have run: the refund would then be wrong.
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), haCallTimeout)
	defer cancel()
	if _, err := s.deps.HA.CallService(hctx, call.Domain, call.Service, call.Data,
		&ha.Target{EntityID: []string{call.EntityID}}, false); err != nil {
		refund()
		s.log.Warn("guest action failed", "link", g.link.ID, "guest", g.label, "entity_id", call.EntityID, "action", req.Action, "err", err)
		writeError(g.w, http.StatusBadGateway, msgHAFailed)
		return
	}
	s.log.Info("guest action", "link", g.link.ID, "guest", g.label, "entity_id", call.EntityID,
		"action", req.Action, "template", src.Template, "client", g.ipKey())
	s.fire(ctx, eventAction, map[string]any{
		"link_id": g.link.ID, "link_name": g.link.Name, "guest": g.label,
		"entity_id": call.EntityID, "action": req.Action, "ip": g.ipKey(),
	})
	writeJSON(g.w, http.StatusOK, map[string]bool{"ok": true})
}

// fire sends an HA event. A failure is logged and never fails the
// request.
func (s *Server) fire(ctx context.Context, typ string, data map[string]any) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), haEventTimeout)
	defer cancel()
	if err := s.deps.HA.FireEvent(ctx, typ, data); err != nil {
		s.log.Warn("fire event", "type", typ, "err", err)
	}
}

// whereBody is POST /g/{slug}/where. All fields are required.
type whereBody struct {
	Lat      *float64 `json:"lat"`
	Lon      *float64 `json:"lon"`
	Accuracy *float64 `json:"accuracy"`
}

// guestWhere measures the distance to home and sets the location cookie.
// Coordinates are never stored or logged. The reply holds no distance,
// so repeated tries can't locate the home.
func (s *Server) guestWhere(g *guestReq) {
	var b whereBody
	if !s.decodeGuest(g, &b) {
		return
	}
	if b.Lat == nil || b.Lon == nil || b.Accuracy == nil ||
		!finiteIn(*b.Lat, -90, 90) || !finiteIn(*b.Lon, -180, 180) || !finiteIn(*b.Accuracy, 0, float64(s.deps.Options.MaxAccuracyM)) {
		writeError(g.w, http.StatusBadRequest, msgBadRequest)
		return
	}
	inv, err := s.deps.Inventory.Inventory(g.r.Context())
	if err != nil {
		s.log.Warn("guest where: inventory", "err", err)
		writeError(g.w, http.StatusServiceUnavailable, msgHAFailed)
		return
	}
	d := access.DistanceM(*b.Lat, *b.Lon, inv.Home.Lat, inv.Home.Lon) + *b.Accuracy
	m := uint32(min(math.Ceil(d), math.MaxUint32))
	exp := g.now.Add(s.deps.Options.NearTTL)
	setCookie(g, nearCookie, s.deps.Signer.SignNear(g.link.ID, m, exp), exp)
	s.log.Info("guest location", "link", g.link.ID, "guest", g.label, "distance_m", math.Round(d/10)*10)
	writeJSON(g.w, http.StatusOK, map[string]any{"ok": true, "expires_at": exp})
}

func finiteIn(x, lo, hi float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= lo && x <= hi
}

// guestParty returns the Music Assistant join URL, when the link may show
// it. MA off, down or slow only means no URL.
func (s *Server) guestParty(g *guestReq) {
	ctx := g.r.Context()
	if !s.partyAllowed(g, s.validTemplates(), s.accessFor(g)) {
		writeError(g.w, http.StatusNotFound, msgNotFound)
		return
	}
	o := s.deps.Options
	client, err := s.newMA(o.Settings.MusicAssistant.URL, o.MusicAssistantToken, o.MATimeout)
	if err != nil || client == nil {
		writeError(g.w, http.StatusNotFound, msgNotFound)
		return
	}
	mctx, cancel := context.WithTimeout(ctx, o.MATimeout)
	defer cancel()
	u, err := client.PartyURL(mctx)
	if err != nil {
		s.log.Warn("music assistant party url", "err", err)
		writeError(g.w, http.StatusServiceUnavailable, "The party is not available now.")
		return
	}
	writeJSON(g.w, http.StatusOK, map[string]string{"url": u})
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
