package authz

import (
	"errors"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

func TestPickFirstAllowedSource(t *testing.T) {
	g := Grant{EntityID: "lock.front_door", Action: "lock.unlock", Sources: []Source{
		{Template: "music", Access: model.Access{Network: model.NetworkLocal}},
		{Template: "house_entry", Rule: 1, Access: model.Access{NearHomeM: 300}},
	}}
	notLocal := func(a model.Access) bool { return !a.Local() }
	s, ok := g.Pick(notLocal)
	if !ok || s.Template != "house_entry" || s.Rule != 1 {
		t.Fatalf("Pick = %+v, %v", s, ok)
	}
	if _, ok := g.Pick(func(model.Access) bool { return false }); ok {
		t.Fatal("Pick passed with every source denied")
	}
	if _, ok := (Grant{}).Pick(func(model.Access) bool { return true }); ok {
		t.Fatal("Pick passed with no sources")
	}
}

// The stubs fail closed until wave 1 fills them.
func TestStubsFailClosed(t *testing.T) {
	g, _ := Resolve(model.Link{}, nil, model.Inventory{})
	if len(g) != 0 {
		t.Fatalf("Resolve granted %v", g)
	}
	allowAll := func(model.Access) bool { return true }
	if _, _, err := Check(g, Request{EntityID: "lock.front_door", Action: "lock.unlock"}, nil, allowAll); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Check = %v", err)
	}
}
