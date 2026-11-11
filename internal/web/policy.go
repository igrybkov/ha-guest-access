package web

import (
	"context"

	"github.com/igrybkov/ha-guest-access/internal/model"
	"github.com/igrybkov/ha-guest-access/internal/policy"
)

// hasHome reports whether HA has a home location, for the near_home_m
// check. HA reports 0, 0 when none is set. While HA can't be reached it
// says yes: a save is not blocked by an outage, and the guest side fails
// closed on a near-home rule without a home anyway.
func (s *Server) hasHome(ctx context.Context) bool {
	inv, err := s.deps.Inventory.Inventory(ctx)
	if err != nil {
		return true
	}
	return inv.Home != (model.Home{})
}

// checkTemplates runs the policy over ts. Callers pass only the template
// being saved, so problem paths point into it and other templates that
// broke a rule since (local access turned off, say) do not block it.
func (s *Server) checkTemplates(ctx context.Context, ts model.Templates) error {
	return policy.ValidateTemplates(ts, s.settings(), s.hasHome(ctx))
}

// checkLinkPolicy runs the policy over a link draft against ts.
func (s *Server) checkLinkPolicy(ctx context.Context, l model.Link, ts model.Templates) error {
	return policy.ValidateLink(l, ts, s.settings(), s.hasHome(ctx))
}
