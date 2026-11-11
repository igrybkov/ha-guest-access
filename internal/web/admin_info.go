package web

import (
	"net/http"
	"slices"

	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// catalogView is GET /api/catalog: every action, and what a guest may see
// of each domain's state.
type catalogView struct {
	Actions    []catalog.Action    `json:"actions"`
	Attributes map[string][]string `json:"attributes"`
}

func (s *Server) getCatalog(w http.ResponseWriter, _ *http.Request) {
	v := catalogView{Actions: catalog.All(), Attributes: map[string][]string{}}
	for _, d := range catalog.Domains() {
		v.Attributes[d] = catalog.AttributeAllowlist(d)
	}
	writeJSON(w, http.StatusOK, v)
}

// inventory answers 503 when HA can't be reached.
func (s *Server) inventory(w http.ResponseWriter, r *http.Request) (model.Inventory, bool) {
	inv, err := s.deps.Inventory.Inventory(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Home Assistant is not reachable: "+err.Error())
		return model.Inventory{}, false
	}
	return inv, true
}

// pickerEntity is one entity for the editor's picker.
type pickerEntity struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	DeviceID string `json:"device_id,omitempty"`
	// AreaID is the effective area: the entity's own, else its device's.
	AreaID         string   `json:"area_id,omitempty"`
	Labels         []string `json:"labels"`
	Hidden         bool     `json:"hidden"`
	Disabled       bool     `json:"disabled"`
	EntityCategory string   `json:"entity_category,omitempty"`
	// NeedsCode is true when HA wants a code for this entity
	// (code_format is set). The app never sends codes; the editor warns
	// and suggests a script.
	NeedsCode bool `json:"needs_code"`
}

func (s *Server) listEntities(w http.ResponseWriter, r *http.Request) {
	inv, ok := s.inventory(w, r)
	if !ok {
		return
	}
	ids := make(map[string]bool, len(inv.Entities))
	for id := range inv.Entities {
		ids[id] = true
	}
	states, err := s.deps.Inventory.States(r.Context(), ids)
	if err != nil {
		s.log.Debug("entities: states", "err", err)
	}
	out := make([]pickerEntity, 0, len(inv.Entities))
	for _, id := range sortedKeys(inv.Entities) {
		e := inv.Entities[id]
		area := e.AreaID
		if area == "" && e.DeviceID != "" {
			area = inv.Devices[e.DeviceID].AreaID
		}
		cf, _ := states[id].Attributes["code_format"].(string)
		out = append(out, pickerEntity{
			EntityID: id, Name: e.Name, Domain: model.Domain(id), DeviceID: e.DeviceID, AreaID: area,
			Labels: nonNil(e.Labels), Hidden: e.Hidden, Disabled: e.Disabled, EntityCategory: e.EntityCategory,
			NeedsCode: cf != "",
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listAreas(w http.ResponseWriter, r *http.Request) {
	inv, ok := s.inventory(w, r)
	if !ok {
		return
	}
	out := make([]model.Area, 0, len(inv.Areas))
	for _, id := range sortedKeys(inv.Areas) {
		a := inv.Areas[id]
		a.Labels = nonNil(a.Labels)
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listLabels(w http.ResponseWriter, r *http.Request) {
	inv, ok := s.inventory(w, r)
	if !ok {
		return
	}
	out := make([]model.Label, 0, len(inv.Labels))
	for _, id := range sortedKeys(inv.Labels) {
		out = append(out, inv.Labels[id])
	}
	writeJSON(w, http.StatusOK, out)
}

// pickerDevice is one device. Name is the display name that device
// patterns match on.
type pickerDevice struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	AreaID string   `json:"area_id,omitempty"`
	Labels []string `json:"labels"`
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	inv, ok := s.inventory(w, r)
	if !ok {
		return
	}
	out := make([]pickerDevice, 0, len(inv.Devices))
	for _, id := range sortedKeys(inv.Devices) {
		d := inv.Devices[id]
		out = append(out, pickerDevice{ID: id, Name: d.DisplayName(), AreaID: d.AreaID, Labels: nonNil(d.Labels)})
	}
	slices.SortStableFunc(out, func(a, b pickerDevice) int { return compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, out)
}

// diagnosticsView is GET /api/diagnostics: the last guest requests as the
// app saw them, newest first, and warnings about the proxy setup.
type diagnosticsView struct {
	Requests []DiagEntry `json:"requests"`
	Warnings []string    `json:"warnings"`
}

func (s *Server) getDiagnostics(w http.ResponseWriter, _ *http.Request) {
	v := diagnosticsView{Requests: s.diag.list(), Warnings: []string{}}
	for _, e := range v.Requests {
		if e.UntrustedForwarding {
			v.Warnings = append(v.Warnings, "A guest request had proxy headers, but its peer "+e.Peer+
				" is not a trusted proxy, so the headers were ignored. If that peer is your reverse proxy or tunnel, add it to trusted_proxies.")
			break
		}
	}
	for _, e := range v.Requests {
		if e.Unresolved {
			v.Warnings = append(v.Warnings, "A request came through a trusted proxy without a usable X-Forwarded-For. Its client counts as not local.")
			break
		}
	}
	if s.lim.off {
		v.Warnings = append(v.Warnings, "Rate limits are off.")
	}
	writeJSON(w, http.StatusOK, v)
}
