package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/audit"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// auditRecords reads the app's audit file, oldest first.
func (a *app) auditRecords() []audit.Record {
	a.t.Helper()
	raw, _ := os.ReadFile(filepath.Join(a.dataDir, audit.FileName))
	var out []audit.Record
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r audit.Record
		if err := json.Unmarshal(line, &r); err != nil {
			a.t.Fatalf("bad audit line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// Every way an action is denied gives exactly one guest.act.denied
// record with the internal reason.
func TestE2EAuditActDenials(t *testing.T) {
	a := startApp(t, appOpts{options: func(o *config.Options) { o.Settings.LocalAccess.Networks = []string{"127.0.0.0/8"} }})
	waitFor(t, "app.start record", func() bool {
		r := a.auditRecords()
		return len(r) > 0 && r[0].Event == "app.start"
	})
	if r := a.auditRecords()[0]; r.LimitsEnabled == nil || !*r.LimitsEnabled || r.Version == "" {
		t.Errorf("app.start = %+v", r)
	}
	light := map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}

	all := a.newLink(map[string]any{"templates": []string{"guest_room", "entry", "office"}})
	off := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	a.adminJSON("POST", "/api/links/"+off.ID+"/deactivate", map[string]any{})
	locked := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	a.adminJSON("POST", "/api/links/"+locked.ID+"/passwords", map[string]any{"label": "Anna", "password": "pw"})
	approve := a.newLink(map[string]any{"templates": []string{"guest_room"}, "approval": map[string]any{"manual": true}})
	once := a.newLink(map[string]any{"templates": []string{"guest_room"}, "one_time": true})
	if r := a.act(once, "light.guest_room", "light.turn_on", nil); r.code != 200 {
		t.Fatalf("first one-time use: %d", r.code)
	}
	failing := a.newLink(map[string]any{"templates": []string{"guest_room"}})

	for _, c := range []struct {
		reason string
		send   func() reply
	}{
		{"inactive", func() reply { return a.act(off, "light.guest_room", "light.turn_on", nil) }},
		{"no_session", func() reply { return a.act(locked, "light.guest_room", "light.turn_on", nil) }},
		{"session_invalid", func() reply {
			return a.act(locked, "light.guest_room", "light.turn_on", nil, &http.Cookie{Name: "ga_session_" + locked.ID, Value: "AQIAAAAAZ2ZnZ2ZnZ2ZnZ2Zn"})
		}},
		{"device_not_approved", func() reply { return a.act(approve, "light.guest_room", "light.turn_on", nil) }},
		{"not_granted", func() reply { return a.act(all, "light.bedroom", "light.turn_on", nil) }},
		{"access_network", func() reply { return a.act(all, "switch.office", "switch.turn_on", nil) }},
		{"access_near", func() reply { return a.act(all, "lock.front_door", "lock.unlock", nil) }},
		{"bad_data:brightness_pct", func() reply {
			return a.act(all, "light.guest_room", "light.turn_on", map[string]any{"brightness_pct": 900})
		}},
		{"one_time_used", func() reply { return a.act(once, "light.guest_room", "light.turn_on", nil) }},
		{"ha_error", func() reply {
			a.failHA.Store(true)
			defer a.failHA.Store(false)
			return a.act(failing, "light.guest_room", "light.turn_on", nil)
		}},
		{"origin", func() reply {
			return a.do(call{method: "POST", url: a.guest + "/g/" + all.Slug + "/act", body: light, origin: "https://evil.example"})
		}},
		{"content_type", func() reply {
			return a.do(call{method: "POST", url: a.guest + "/g/" + all.Slug + "/act", body: "x", ctype: "text/plain"})
		}},
		{"bad_request", func() reply {
			return a.do(call{method: "POST", url: a.guest + "/g/" + all.Slug + "/act", body: `{"entity_id":1}`})
		}},
	} {
		before := len(a.auditRecords())
		r := c.send()
		if r.code < 400 || r.has(c.reason) {
			t.Errorf("%s: %d %s", c.reason, r.code, r.body)
		}
		var denied []audit.Record
		for _, rec := range a.auditRecords()[before:] {
			if rec.Result == audit.ResultDenied {
				denied = append(denied, rec)
			}
		}
		if len(denied) != 1 || denied[0].Event != "guest.act.denied" || denied[0].Reason != c.reason || denied[0].LinkID == "" ||
			denied[0].ClientIP != "127.0.0.1" {
			t.Errorf("%s: denied records %+v", c.reason, denied)
		}
	}
}

// Admin writes are recorded with who made them. The admin port here takes
// any peer (the dev bypass), and the peer is not the ingress proxy, so the
// X-Remote-User headers from the test are not trusted: the user is "dev".
func TestE2EAuditAdminWrites(t *testing.T) {
	a := startApp(t, appOpts{})
	user := map[string]string{"X-Remote-User-Id": "u1", "X-Remote-User-Name": "illia", "X-Remote-User-Display-Name": "Illia"}
	r := a.do(call{method: "POST", url: a.admin + "/api/links", headers: user,
		body: map[string]any{"name": "Anna", "valid_until": t0.Add(time.Hour), "templates": []string{"guest_room"}}})
	var l model.Link
	_ = json.Unmarshal(r.body, &l)
	a.do(call{method: "POST", url: a.admin + "/api/links/" + l.ID + "/rotate", headers: user, body: map[string]any{}})
	a.do(call{method: "DELETE", url: a.admin + "/api/links/" + l.ID, headers: user, body: map[string]any{}})
	var got []string
	for _, rec := range a.auditRecords() {
		if !strings.HasPrefix(rec.Event, "admin.") {
			continue
		}
		got = append(got, rec.Event)
		if rec.User == nil || *rec.User != (audit.User{Name: "dev"}) || rec.LinkID != l.ID || rec.LinkName != "Anna" || rec.Result != audit.ResultOK {
			t.Errorf("record = %+v", rec)
		}
	}
	if strings.Join(got, ",") != "admin.link.create,admin.link.rotate,admin.link.delete" {
		t.Errorf("events = %v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(a.dataDir, audit.FileName))
	if bytes.Contains(raw, []byte(l.Slug)) {
		t.Error("audit file holds a slug")
	}
}

// syncBuf is a bytes.Buffer safe for parallel writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// With audit.enabled false, no file is written, and records still reach
// the app log.
func TestE2EAuditFileOff(t *testing.T) {
	var buf syncBuf
	a := startApp(t, appOpts{options: func(o *config.Options) { o.AuditEnabled = false },
		logger: slog.New(slog.NewTextHandler(&buf, nil))})
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	a.act(l, "light.bedroom", "light.turn_on", nil)
	waitFor(t, "audit log line", func() bool { return strings.Contains(buf.String(), "event=guest.act.denied") })
	if !strings.Contains(buf.String(), "event=app.start") {
		t.Error("no app.start line")
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, audit.FileName)); !os.IsNotExist(err) {
		t.Errorf("audit file exists: %v", err)
	}
}

// Denials outside /act: each gives exactly one denied record.
func TestE2EAuditOtherDenials(t *testing.T) {
	a := approvalApp(t)
	locked := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	a.adminJSON("POST", "/api/links/"+locked.ID+"/passwords", map[string]any{"label": "Anna", "password": "pw"})
	entry := a.newLink(map[string]any{"templates": []string{"entry"}})
	manual := a.approvalLink(true, false)
	b := a.browser(iPhoneUA)
	for _, c := range []struct {
		event, reason string
		send          func()
	}{
		{"guest.password.fail", "wrong_password", func() {
			a.do(call{method: "POST", url: a.guest + "/g/" + locked.Slug + "/password", body: map[string]any{"password": "nope"}})
		}},
		{"guest.near.denied", "accuracy_too_low", func() {
			a.do(call{method: "POST", url: a.guest + "/g/" + entry.Slug + "/where", body: map[string]any{"lat": home.lat, "lon": home.lon, "accuracy": 1e6}})
		}},
		{"guest.slug.miss", "", func() { a.do(call{method: "GET", url: a.guest + "/g/not-a-real-link/state"}) }},
		{"device.denied", "", func() {
			b.do("POST", manual, "device/request", nil)
			ev := a.lastRequestEvent()
			a.ha.Emit("guest_access_device_decision", map[string]any{"request_id": ev["request_id"], "approve": false})
			waitFor(t, "denial", func() bool {
				for _, r := range a.auditRecords() {
					if r.Event == "device.denied" {
						return true
					}
				}
				return false
			})
		}},
	} {
		before := len(a.auditRecords())
		c.send()
		var denied []audit.Record
		for _, rec := range a.auditRecords()[before:] {
			if rec.Result == audit.ResultDenied {
				denied = append(denied, rec)
			}
		}
		if len(denied) != 1 || denied[0].Event != c.event || denied[0].Reason != c.reason {
			t.Errorf("%s: denied records %+v", c.event, denied)
		}
	}
}
