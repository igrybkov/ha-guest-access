package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/approval"
	"github.com/igrybkov/ha-guest-access/internal/config"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// browser keeps the cookies one guest browser holds. Every guest cookie
// has Path=/g/, so all of them go with every guest request.
type browser struct {
	a       *app
	ua      string
	cookies map[string]*http.Cookie
}

func (a *app) browser(ua string) *browser {
	return &browser{a: a, ua: ua, cookies: map[string]*http.Cookie{}}
}

func (b *browser) do(method string, l model.Link, route string, body any) reply {
	b.a.t.Helper()
	var cs []*http.Cookie
	for _, c := range b.cookies {
		cs = append(cs, c)
	}
	if method == "POST" && body == nil {
		body = map[string]any{}
	}
	r := b.a.do(call{method: method, url: b.a.guest + "/g/" + l.Slug + "/" + route, body: body, cookies: cs,
		headers: map[string]string{"User-Agent": b.ua}})
	for _, c := range r.cookies {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return r
}

func (b *browser) status(l model.Link, route string, body any) string {
	b.a.t.Helper()
	r := b.do(map[bool]string{true: "POST", false: "GET"}[body != nil], l, route, body)
	var v struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	}
	_ = json.Unmarshal(r.body, &v)
	return v.Status
}

func (b *browser) act(l model.Link) int {
	return b.do("POST", l, "act", map[string]any{"entity_id": "light.guest_room", "action": "light.turn_on"}).code
}

const (
	iPhoneUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	androidUA = "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36"
)

// lastRequestEvent returns the data of the newest device request event.
func (a *app) lastRequestEvent() map[string]any {
	a.t.Helper()
	var last map[string]any
	waitFor(a.t, "device request event", func() bool {
		for _, ev := range a.fake.FiredEvents() {
			if ev.EventType == approval.EventDeviceRequest {
				last = ev.Data
			}
		}
		return last != nil
	})
	return last
}

func approvalApp(t *testing.T) *app {
	a := startApp(t, appOpts{})
	waitFor(t, "decision subscription", func() bool { return a.ha.Subscribers(approval.EventDeviceDecision) > 0 })
	return a
}

func (a *app) approvalLink(manual, self bool) model.Link {
	return a.newLink(map[string]any{"templates": []string{"guest_room"},
		"approval": map[string]any{"manual": manual, "self_service": self}})
}

// Manual mode, decided in HA: the request fires an event with the code
// the guest sees, and HA's decision event lets the device in.
func TestE2EApprovalManualByHA(t *testing.T) {
	a := approvalApp(t)
	l := a.approvalLink(true, false)
	b := a.browser(iPhoneUA)
	if c := b.act(l); c != 403 {
		t.Fatalf("act before approval: %d", c)
	}
	r := b.do("POST", l, "device/request", nil)
	var got struct{ Status, Code string }
	_ = json.Unmarshal(r.body, &got)
	if r.code != 200 || got.Status != "pending" || len(got.Code) != approval.CodeLen {
		t.Fatalf("request: %d %s", r.code, r.body)
	}
	ev := a.lastRequestEvent()
	if ev["code"] != got.Code || ev["link_id"] != l.ID || ev["link_name"] != l.Name || ev["device"] != "iPhone, Safari" {
		t.Errorf("event = %v", ev)
	}
	if s := b.status(l, "device/status", nil); s != "pending" {
		t.Errorf("status while waiting: %s", s)
	}
	a.ha.Emit(approval.EventDeviceDecision, map[string]any{"request_id": ev["request_id"], "approve": true})
	waitFor(t, "approval", func() bool { return b.status(l, "device/status", nil) == "approved" })
	if c := b.act(l); c != 200 {
		t.Fatalf("act after approval: %d", c)
	}
	waitFor(t, "service call", func() bool { return len(a.actionCalls()) == 1 })
	var view struct {
		Devices []model.LinkDevice `json:"devices"`
	}
	_ = json.Unmarshal(a.adminJSON("GET", "/api/links/"+l.ID, nil).body, &view)
	if len(view.Devices) != 1 || view.Devices[0].ApprovedBy != model.ApprovedByHA {
		t.Errorf("devices = %+v", view.Devices)
	}
}

// A denial from HA keeps the device out.
func TestE2EApprovalDeny(t *testing.T) {
	a := approvalApp(t)
	l := a.approvalLink(true, false)
	b := a.browser(iPhoneUA)
	b.do("POST", l, "device/request", nil)
	ev := a.lastRequestEvent()
	a.ha.Emit(approval.EventDeviceDecision, map[string]any{"request_id": ev["request_id"], "approve": false})
	waitFor(t, "denial", func() bool { return b.status(l, "device/status", nil) == "denied" })
	if c := b.act(l); c != 403 {
		t.Errorf("act after deny: %d", c)
	}
	if n := len(a.actionCalls()); n != 0 {
		t.Errorf("%d HA calls", n)
	}
}

// actionCalls returns the service calls HA got, less the app's own
// persistent notifications.
func (a *app) actionCalls() []hatest.ServiceCall {
	var out []hatest.ServiceCall
	for _, c := range a.fake.ServiceCalls() {
		if c.Domain != "persistent_notification" {
			out = append(out, c)
		}
	}
	return out
}

// notifyCalls returns the persistent notification calls HA got.
func (a *app) notifyCalls() []hatest.ServiceCall {
	var out []hatest.ServiceCall
	for _, c := range a.fake.ServiceCalls() {
		if c.Domain == "persistent_notification" {
			out = append(out, c)
		}
	}
	return out
}

// Each request shows in HA as a persistent notification over the real
// websocket client, and the admin's decision dismisses it. Outside
// Supervisor the notification has no app link.
func TestE2EApprovalNotification(t *testing.T) {
	a := approvalApp(t)
	l := a.approvalLink(true, false)
	b := a.browser(iPhoneUA)
	b.do("POST", l, "device/request", nil)
	ev := a.lastRequestEvent()
	id := approval.NotificationID(ev["request_id"].(string))
	waitFor(t, "notification", func() bool { return len(a.notifyCalls()) == 1 })
	c := a.notifyCalls()[0]
	msg, _ := c.Data["message"].(string)
	if c.Service != "create" || c.Data["notification_id"] != id || !strings.Contains(msg, ev["code"].(string)) || strings.Contains(msg, "](") {
		t.Errorf("create = %+v", c)
	}
	if r := a.adminJSON("POST", "/api/devices/pending/"+ev["request_id"].(string), map[string]any{"approve": false}); r.code != 200 {
		t.Fatalf("deny: %d %s", r.code, r.body)
	}
	waitFor(t, "dismiss", func() bool { return len(a.notifyCalls()) == 2 })
	if c := a.notifyCalls()[1]; c.Service != "dismiss" || c.Data["notification_id"] != id {
		t.Errorf("dismiss = %+v", c)
	}
}

// With approval.persistent_notification off, HA gets no notification.
func TestE2EApprovalNotificationOff(t *testing.T) {
	a := startApp(t, appOpts{options: func(o *config.Options) { o.Approval.PersistentNotification = false }})
	waitFor(t, "decision subscription", func() bool { return a.ha.Subscribers(approval.EventDeviceDecision) > 0 })
	l := a.approvalLink(true, false)
	a.browser(iPhoneUA).do("POST", l, "device/request", nil)
	a.lastRequestEvent()
	time.Sleep(50 * time.Millisecond)
	if n := len(a.fake.ServiceCalls()); n != 0 {
		t.Errorf("%d HA calls", n)
	}
}

// Claim mode: the first device gets in at once, the second waits.
// "Reset devices" lets the next device claim again, and the old cookie
// stops working at once.
func TestE2EApprovalClaimAndReset(t *testing.T) {
	a := approvalApp(t)
	l := a.approvalLink(false, true)
	first, second := a.browser(iPhoneUA), a.browser(androidUA)
	if s := first.status(l, "device/request", map[string]any{}); s != "approved" {
		t.Fatalf("claim: %s", s)
	}
	if c := first.act(l); c != 200 {
		t.Fatalf("claimer act: %d", c)
	}
	if s := second.status(l, "device/request", map[string]any{}); s != "pending" {
		t.Fatalf("second: %s", s)
	}
	if r := a.adminJSON("POST", "/api/links/"+l.ID+"/devices/reset", map[string]any{}); r.code != 200 {
		t.Fatalf("reset: %d %s", r.code, r.body)
	}
	if c := first.act(l); c != 403 {
		t.Errorf("old device after reset: %d", c)
	}
	// The second browser's request went with the reset; it asks again
	// and now claims the link.
	if s := second.status(l, "device/request", map[string]any{}); s != "approved" {
		t.Fatalf("claim after reset: %s", s)
	}
	if c := second.act(l); c != 200 {
		t.Errorf("new claimer act: %d", c)
	}
}

// Self-service: an approved device sees the pending request and approves
// it. A device of another link can't.
func TestE2EApprovalSelfService(t *testing.T) {
	a := approvalApp(t)
	l := a.approvalLink(true, true)
	owner, guest := a.browser(iPhoneUA), a.browser(androidUA)
	owner.do("POST", l, "device/request", nil)
	ev := a.lastRequestEvent()
	if r := a.adminJSON("POST", "/api/devices/pending/"+ev["request_id"].(string), map[string]any{"approve": true}); r.code != 200 {
		t.Fatalf("admin approve: %d %s", r.code, r.body)
	}
	if s := owner.status(l, "device/status", nil); s != "approved" {
		t.Fatalf("owner: %s", s)
	}
	guest.do("POST", l, "device/request", nil)
	code := a.lastRequestEvent()["code"]
	r := owner.do("GET", l, "device/pending", nil)
	var pending struct {
		Requests []approval.GuestRequest `json:"requests"`
	}
	_ = json.Unmarshal(r.body, &pending)
	if r.code != 200 || len(pending.Requests) != 1 || pending.Requests[0].Code != code || strings.Contains(string(r.body), `"ip"`) {
		t.Fatalf("pending: %d %s", r.code, r.body)
	}
	// The waiting guest can't approve itself.
	if r := guest.do("POST", l, "device/decide", map[string]any{"request_id": pending.Requests[0].ID, "approve": true}); r.code != 403 {
		t.Errorf("self-approve: %d", r.code)
	}
	// Nor can an approved device of another link.
	other := a.approvalLink(false, true)
	stranger := a.browser(iPhoneUA)
	stranger.status(other, "device/request", map[string]any{})
	if r := stranger.do("POST", other, "device/decide", map[string]any{"request_id": pending.Requests[0].ID, "approve": true}); r.code != 404 {
		t.Errorf("other link's device: %d", r.code)
	}
	if s := owner.status(l, "device/decide", map[string]any{"request_id": pending.Requests[0].ID, "approve": true}); s != "approved" {
		t.Fatalf("decide: %s", s)
	}
	if s := guest.status(l, "device/status", nil); s != "approved" {
		t.Fatalf("guest: %s", s)
	}
	if c := guest.act(l); c != 200 {
		t.Errorf("guest act: %d", c)
	}
}

// Revoking a device ends its access and its open event stream.
func TestE2EApprovalRevoke(t *testing.T) {
	a := approvalApp(t)
	l := a.approvalLink(false, true)
	b := a.browser(iPhoneUA)
	b.status(l, "device/request", map[string]any{})
	req, _ := http.NewRequest("GET", a.guest+"/g/"+l.Slug+"/events", nil)
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	events := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if ev, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				events <- ev
			}
		}
		close(events)
	}()
	waitEvent := func(want string) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatalf("stream closed before %q", want)
				}
				if ev == want {
					return
				}
			case <-deadline:
				t.Fatalf("no %q event", want)
			}
		}
	}
	waitEvent("ready")

	var view struct {
		Devices []model.LinkDevice `json:"devices"`
	}
	_ = json.Unmarshal(a.adminJSON("GET", "/api/links/"+l.ID, nil).body, &view)
	if len(view.Devices) != 1 {
		t.Fatalf("devices = %+v", view.Devices)
	}
	if r := a.adminJSON("DELETE", "/api/links/"+l.ID+"/devices/"+view.Devices[0].ID, map[string]any{}); r.code != 200 {
		t.Fatalf("revoke: %d %s", r.code, r.body)
	}
	waitEvent("end")
	if c := b.act(l); c != 403 {
		t.Errorf("act after revoke: %d", c)
	}
}
