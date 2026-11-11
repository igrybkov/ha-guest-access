package web

import (
	"fmt"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/audit"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// pwLink makes a link with one password, "right".
func (h *harness) pwLink() model.Link {
	h.t.Helper()
	l := h.link("guest_room")
	hash, err := access.HashPassword("right")
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.links.AddPassword(l.ID, "Anna", hash); err != nil {
		h.t.Fatal(err)
	}
	return l
}

// tryPassword posts a password for l from peer.
func (h *harness) tryPassword(l model.Link, pw, peer string) *httptest.ResponseRecorder {
	return h.g(req{method: "POST", path: "/g/" + l.Slug + "/password", body: map[string]any{"password": pw}, peer: peer})
}

// peerN is a distinct client address for each n.
func peerN(n int) string { return fmt.Sprintf("[2001:db8::%x]:4000", n+1) }

// lockoutScopes lists the scope of each guest_access_lockout event.
func (h *harness) lockoutScopes() []string {
	var out []string
	for _, e := range h.fake.FiredEvents() {
		if e.EventType == eventLockout {
			out = append(out, fmt.Sprint(e.Data["scope"]))
		}
	}
	return out
}

// withLinkFails sets a small per-link fail limit so tests run few argon2
// checks.
func withLinkFails(n int) harnessOpt {
	return func(o *config.Options, _ *Deps) { o.Settings.RateLimits.PasswordFailsPerLink = n }
}

// Past the per-link limit, a guest from a fresh address waits the delay
// and then gets in. The slow spell fires one lockout event.
func TestGuestPasswordLinkSlowDown(t *testing.T) {
	ah := newAuditHarness(t, withLinkFails(3))
	l := ah.pwLink()
	for i := range 3 {
		if c := ah.tryPassword(l, "wrong", peerN(i)).Code; c != 403 {
			t.Fatalf("fail %d: %d", i, c)
		}
	}
	if got := ah.lockoutScopes(); len(got) != 1 || got[0] != lockoutScopeLink {
		t.Fatalf("lockout events = %v", got)
	}

	guest := peerN(100)
	rec := ah.tryPassword(l, "right", guest)
	retry, _ := strconv.Atoi(rec.Header().Get("Retry-After"))
	if rec.Code != 429 || retry < 1 || retry > 10 {
		t.Fatalf("fresh address inside the delay: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	ah.advance(time.Duration(retry) * time.Second)
	rec = ah.tryPassword(l, "right", guest)
	if rec.Code != 200 || cookie(rec, sessionCookie) == nil {
		t.Fatalf("after the delay: %d", rec.Code)
	}

	var slowed, lockouts int
	for _, r := range ah.records() {
		switch {
		case r.Event == "guest.password.denied" && r.Reason == reasonLinkSlowed:
			slowed++
		case r.Event == "guest.lockout" && r.Reason == reasonLinkSlowed:
			lockouts++
		}
	}
	if slowed != 1 || lockouts != 1 {
		t.Errorf("audit: %d link_slowed denials, %d lockout records", slowed, lockouts)
	}
}

// The per-address block still works: five wrong passwords block that
// address for the lockout time, whatever the link delay. Other addresses
// are not blocked.
func TestGuestPasswordIPBlockStays(t *testing.T) {
	h := newHarness(t)
	l := h.pwLink()
	attacker := peerN(1)
	for i := range 5 {
		if c := h.tryPassword(l, "wrong", attacker).Code; c != 403 {
			t.Fatalf("fail %d: %d", i, c)
		}
	}
	h.advance(time.Minute) // far past the link delay
	rec := h.tryPassword(l, "right", attacker)
	if rec.Code != 429 {
		t.Fatalf("blocked address: %d", rec.Code)
	}
	if retry, _ := strconv.Atoi(rec.Header().Get("Retry-After")); retry < 3000 {
		t.Errorf("Retry-After %d, want close to an hour", retry)
	}
	if got := h.lockoutScopes(); len(got) != 1 || got[0] != lockoutScopeIP {
		t.Errorf("lockout events = %v", got)
	}
	if c := h.tryPassword(l, "right", peerN(2)).Code; c != 200 {
		t.Errorf("another address: %d", c)
	}
}

// Guessing from a new address every second gets about one try per delay
// once the link is slowed: the per-link limit, then one per 10 seconds.
func TestGuestPasswordBruteForceCapped(t *testing.T) {
	const fails, run = 3, 120 // seconds of guessing
	h := newHarness(t, withLinkFails(fails))
	l := h.pwLink()
	checked := 0
	for i := range run {
		switch c := h.tryPassword(l, "wrong", peerN(i)).Code; c {
		case 403:
			checked++
		case 429:
		default:
			t.Fatalf("try %d: %d", i, c)
		}
		h.advance(time.Second)
	}
	want := fails + run/10
	if checked > want+1 || checked < want-1 {
		t.Errorf("%d passwords checked in %d s, want about %d", checked, run, want)
	}
	if got := h.lockoutScopes(); len(got) != 1 {
		t.Errorf("lockout events = %v, want one", got)
	}
}

// One page load is the shell, its files and the state call. Ten loads
// within a minute from one address all work: files do not use the
// pre-auth limiter.
func TestGuestPageLoadsShareNoLimit(t *testing.T) {
	h := newHarness(t)
	pw := h.pwLink()
	open := h.link("guest_room")
	files := make([]string, 0, len(h.srv.assets.files))
	for name := range h.srv.assets.files {
		files = append(files, "/assets/"+h.srv.assets.version+"/"+name)
	}
	if len(files) < 2 {
		t.Fatalf("guest files = %v", files)
	}
	for i := range 10 {
		l := open
		if i%2 == 1 {
			l = pw // its state call is a 401 before sign-in, on the pre-auth limiter
		}
		for _, path := range append([]string{"/g/" + l.Slug}, files...) {
			if c := h.g(req{method: "GET", path: path}).Code; c != 200 {
				t.Fatalf("load %d: %s got %d", i, path, c)
			}
		}
		// A browser asks for an icon too. There is none; it must not
		// be refused.
		if c := h.g(req{method: "GET", path: "/favicon.ico"}).Code; c != 404 {
			t.Fatalf("load %d: favicon got %d", i, c)
		}
		wantState := 200
		if l.ID == pw.ID {
			wantState = 401
		}
		if c := h.g(req{method: "GET", path: "/g/" + l.Slug + "/state"}).Code; c != wantState {
			t.Fatalf("load %d: state got %d, want %d", i, c, wantState)
		}
		h.advance(6 * time.Second)
	}
}

// Files have their own limit, and slug misses and pre-auth routes keep
// theirs.
func TestGuestAssetsOwnLimit(t *testing.T) {
	h := newHarness(t, func(o *config.Options, _ *Deps) {
		o.Settings.RateLimits.AssetsBurst = 4
		o.Settings.RateLimits.GuestBurst = 3
	})
	asset := "/assets/" + h.srv.assets.version + "/guest.js"
	for i := range 4 {
		if c := h.g(req{method: "GET", path: asset}).Code; c != 200 {
			t.Fatalf("file %d: %d", i, c)
		}
	}
	if rec := h.g(req{method: "GET", path: asset}); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("over the files limit: %d", rec.Code)
	}
	// The pre-auth limiter is untouched by the files, and still limits.
	l := h.pwLink()
	for i := range 3 {
		if c := h.g(req{method: "GET", path: "/g/" + l.Slug}).Code; c != 200 {
			t.Fatalf("page %d: %d", i, c)
		}
	}
	if c := h.g(req{method: "GET", path: "/g/" + l.Slug}).Code; c != 429 {
		t.Errorf("over pre-auth: %d", c)
	}
	// Slug misses from another address still hit the miss limit.
	other := "203.0.113.77:1"
	got429 := false
	for range 20 {
		h.advance(10 * time.Second) // keep the pre-auth limiter from answering first
		if h.g(req{method: "GET", path: "/g/no-such-link-here/state", peer: other}).Code == 429 {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Error("slug misses were never limited")
	}
}

// The audit reason of a refused file is its own.
func TestGuestAssetsLimitAudit(t *testing.T) {
	ah := newAuditHarness(t, func(o *config.Options, _ *Deps) { o.Settings.RateLimits.AssetsBurst = 1 })
	asset := "/assets/" + ah.srv.assets.version + "/guest.js"
	ah.g(req{method: "GET", path: asset})
	ah.g(req{method: "GET", path: asset})
	for _, r := range ah.records() {
		if r.Event == auditEventRateLimited && r.Reason == reasonLimitAssets && r.Result == audit.ResultDenied {
			return
		}
	}
	t.Errorf("no limit_assets record in %+v", ah.records())
}
