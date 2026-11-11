package catalog

import "testing"

func TestLookup(t *testing.T) {
	a, ok := Lookup("lock.lock")
	if !ok || a.Domain != "lock" || a.Service != "lock" {
		t.Fatalf("Lookup(lock.lock) = %+v, %v", a, ok)
	}
	for _, id := range []string{"", "lock", "lock.LOCK", " lock.lock", "lock.open", "homeassistant.restart"} {
		if _, ok := Lookup(id); ok {
			t.Errorf("Lookup(%q) found an action", id)
		}
	}
}

func TestEveryActionIsConsistent(t *testing.T) {
	for _, a := range All() {
		if domainOf(a.ID) == "" || a.Domain == "" || a.Service == "" || a.UI == "" {
			t.Errorf("%s: incomplete %+v", a.ID, a)
		}
		for k, f := range a.Data {
			switch f.Kind {
			case KindBool, KindInt, KindFloat:
			case KindString:
				if len(f.Enum) == 0 {
					t.Errorf("%s.%s: string field without enum", a.ID, k)
				}
			default:
				t.Errorf("%s.%s: unknown kind %q", a.ID, k, f.Kind)
			}
			if k == "code" {
				t.Errorf("%s: has a code field", a.ID)
			}
		}
	}
}

func TestDomainsAndAttributes(t *testing.T) {
	if d := Domains(); len(d) == 0 || d[0] != "lock" {
		t.Fatalf("Domains = %v", d)
	}
	if got := AttributeAllowlist("nope"); got != nil {
		t.Fatalf("unknown domain attributes = %v", got)
	}
}
