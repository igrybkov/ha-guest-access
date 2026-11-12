package web

import (
	"maps"
	"net/http"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// entityTextsView is GET /api/entity-text: the admin's own text of every
// entity that has some, keyed by entity id.
type entityTextsView struct {
	Entities map[string]model.EntityText `json:"entities"`
}

func (s *Server) listEntityText(w http.ResponseWriter, _ *http.Request) {
	ts, _ := s.deps.Templates.Templates()
	v := entityTextsView{Entities: map[string]model.EntityText{}}
	maps.Copy(v.Entities, ts.Entities)
	writeJSON(w, http.StatusOK, v)
}

// putEntityText is PUT /api/entity-text/{id}: it sets the name and the
// description of one entity, app-wide. Both empty removes the entry. The
// entity does not have to exist in HA. Only the entities map of
// templates.yaml changes, under the store lock, so a template saved at
// the same time is kept.
func (s *Server) putEntityText(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !model.ValidEntityID(id) {
		writeError(w, http.StatusBadRequest, "not an entity id like light.kitchen")
		return
	}
	var t model.EntityText
	if !s.decodeAdmin(w, r, &t) {
		return
	}
	t = t.Trimmed()
	if err := t.Validate(); err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	err := s.deps.Templates.Update(func(ts *model.Templates) error {
		if t.Empty() {
			delete(ts.Entities, id)
			if len(ts.Entities) == 0 {
				ts.Entities = nil
			}
			return nil
		}
		if ts.Entities == nil {
			ts.Entities = map[string]model.EntityText{}
		}
		ts.Entities[id] = t
		return nil
	})
	if err != nil {
		s.writeAdminErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}
