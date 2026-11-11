package web

import (
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// A template with no rules and a link with no templates and no rules are
// valid, so the admin can build them up step by step. They grant
// nothing: the admin sees a warning, and the guest gets an empty page
// and a refusal for every action.
func TestEmptyTemplateAndLink(t *testing.T) {
	h := newHarness(t)

	empty := model.Template{ID: "empty", Name: "Empty"}
	if rec := h.a(req{method: "POST", path: "/api/templates", body: empty}); rec.Code != 201 {
		t.Fatalf("create empty template: %d %s", rec.Code, rec.Body)
	}
	rec := h.a(req{method: "POST", path: "/api/preview", body: map[string]any{"template": empty}})
	v := decode[previewView](t, rec)
	if rec.Code != 200 || len(v.Problems) != 0 || len(v.Grants) != 0 || len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0].Message, "no rules yet") {
		t.Errorf("empty template preview: %d %+v", rec.Code, v)
	}

	until := t0.Add(48 * time.Hour)
	for _, tc := range []struct {
		name      string
		templates []string
		warning   string
	}{
		{"no templates, no rules", nil, "grants nothing yet"},
		{"only an empty template", []string{"empty"}, "no rules yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.a(req{method: "POST", path: "/api/links", body: map[string]any{"name": "Anna", "valid_until": until, "templates": tc.templates}})
			if rec.Code != 201 {
				t.Fatalf("create: %d %s", rec.Code, rec.Body)
			}
			l := decode[linkView](t, rec)
			if len(l.Problems) != 0 || len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0].Message, tc.warning) {
				t.Errorf("link view: problems %+v, warnings %+v", l.Problems, l.Warnings)
			}

			st := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"})
			sv := decode[stateView](t, st)
			if st.Code != 200 || len(sv.Entities) != 0 || len(sv.States) != 0 || len(sv.Groups) != 0 {
				t.Errorf("guest state: %d %+v", st.Code, sv)
			}
			if rec := h.act(l.Link, "light.guest_room", "light.turn_on", nil); rec.Code != 403 {
				t.Errorf("act on an empty link: %d %s", rec.Code, rec.Body)
			}
			if calls := h.fake.ServiceCalls(); len(calls) != 0 {
				t.Errorf("HA got calls: %+v", calls)
			}
		})
	}
}
