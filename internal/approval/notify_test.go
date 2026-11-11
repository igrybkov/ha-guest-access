package approval

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/ha/hatest"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

const testPanel = "/app/local_guest_access/pending"

// notifyEnv is an env whose manager sends notifications to a fake HA.
type notifyEnv struct {
	*env
	fake *hatest.Fake
	logs *syncBuffer // the notifier's log
}

// newNotifyEnv starts the notifier and the expiry loop. panelErr makes
// the panel link lookup fail.
func newNotifyEnv(t *testing.T, panelErr error) *notifyEnv {
	t.Helper()
	fake := &hatest.Fake{}
	logs := &syncBuffer{}
	n := NewNotifier(NotifierOptions{
		CallService: func(ctx context.Context, domain, service string, data map[string]any) error {
			_, err := fake.CallService(ctx, domain, service, data, nil, false)
			return err
		},
		PanelLink: func(context.Context) (string, error) { return testPanel, panelErr },
		Timeout:   time.Second,
		Logger:    slog.New(slog.NewTextHandler(logs, nil)),
	})
	e := newEnv(t, Options{Notifier: n})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { n.Run(ctx); done <- struct{}{} }()
	go func() { e.m.RunExpiry(ctx); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done; <-done })
	return &notifyEnv{env: e, fake: fake, logs: logs}
}

// ask has browser b ask for access with the given label.
func (e *notifyEnv) ask(linkID string, b *browser, label string) result {
	e.t.Helper()
	return e.call(func(w http.ResponseWriter, r *http.Request, l model.Link, c access.Client, _ string) {
		e.m.RequestAccess(w, r, l, c, label)
	}, linkID, b, "{}")
}

// calls waits until the fake HA got n notification calls and returns them.
func (e *notifyEnv) calls(n int) []hatest.ServiceCall {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls := e.fake.ServiceCalls()
		if len(calls) >= n {
			if len(calls) > n {
				e.t.Fatalf("%d HA calls, want %d: %+v", len(calls), n, calls)
			}
			return calls
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("%d HA calls after 5s, want %d: %+v", len(calls), n, calls)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// quiet checks that no more than n calls arrive in a short while.
func (e *notifyEnv) quiet(n int) {
	e.t.Helper()
	time.Sleep(50 * time.Millisecond)
	if got := len(e.fake.ServiceCalls()); got != n {
		e.t.Fatalf("%d HA calls, want %d: %+v", got, n, e.fake.ServiceCalls())
	}
}

func wantCall(t *testing.T, c hatest.ServiceCall, service, requestID string) {
	t.Helper()
	if c.Domain != "persistent_notification" || c.Service != service || c.Target != nil {
		t.Fatalf("call %+v, want persistent_notification.%s with no target", c, service)
	}
	if id := c.Data["notification_id"]; id != NotificationID(requestID) {
		t.Fatalf("notification_id %v, want %q", id, NotificationID(requestID))
	}
}

func TestNotificationCreated(t *testing.T) {
	e := newNotifyEnv(t, nil)
	l := e.newLink(manual)
	want(t, "ask", e.ask(l.ID, newBrowser(iPhoneUA), "Anna"), 200, "pending")
	req := e.m.Pending(l.ID)[0]

	c := e.calls(1)[0]
	wantCall(t, c, "create", req.ID)
	if c.Data["notification_id"] != "guest_access_"+req.ID {
		t.Errorf("notification_id %v", c.Data["notification_id"])
	}
	if c.Data["title"] != "Someone wants in: Anna" {
		t.Errorf("title %q", c.Data["title"])
	}
	msg, _ := c.Data["message"].(string)
	for _, s := range []string{"Guest: Anna\n", "Device: iPhone\\, Safari\n", "Code: **" + req.Code + "**", "[Approve or deny in Guest Access](" + testPanel + ")"} {
		if !strings.Contains(msg, s) {
			t.Errorf("message has no %q:\n%s", s, msg)
		}
	}
	// The HA event still fires.
	if len(e.fired()) != 1 {
		t.Errorf("%d events fired", len(e.fired()))
	}
}

func TestNotificationNoLabelNoLink(t *testing.T) {
	e := newNotifyEnv(t, errors.New("supervisor is away"))
	l := e.newLink(manual)
	want(t, "ask", e.ask(l.ID, newBrowser(androidUA), ""), 200, "pending")
	msg, _ := e.calls(1)[0].Data["message"].(string)
	if strings.Contains(msg, "Guest:") || strings.Contains(msg, "](") {
		t.Errorf("message:\n%s", msg)
	}
	if !strings.Contains(e.logs.String(), "no app link") {
		t.Error("the failed lookup is not logged")
	}
}

// Guest text can't add a link, HTML or formatting to the notification.
func TestNotificationEscapesGuestText(t *testing.T) {
	for _, label := range []string{
		"[click](https://evil.example)",
		"https://evil.example/x",
		"www.evil.example",
		"me@evil.example",
		`<a href="https://evil.example">x</a>`,
		"![img](https://evil.example/i.png)",
		"**bold** _it_ `code` ~~del~~",
		"[ref]\n\n[ref]: https://evil.example",
		"# heading",
		"1. list",
		"&lt;a&gt;",
	} {
		t.Run(label, func(t *testing.T) {
			msg := notificationMessage(Request{Code: "K7Q2", Label: Clean(label), Device: "iPhone, Safari"}, testPanel)
			line := strings.SplitN(msg, "\n", 2)[0]
			if !strings.HasPrefix(line, "Guest: ") {
				t.Fatalf("label broke the line: %q", msg)
			}
			text := strings.TrimPrefix(line, "Guest: ")
			// Every ASCII punctuation character is escaped.
			for i := 0; i < len(text); i++ {
				if isASCIIPunct(text[i]) {
					if text[i] != '\\' || i+1 == len(text) || !isASCIIPunct(text[i+1]) {
						t.Fatalf("unescaped %q at %d in %q", text[i], i, text)
					}
					i++
				}
			}
			for _, bad := range []string{"](", "://", "www.", "@evil."} {
				if strings.Contains(text, bad) {
					t.Errorf("%q survives in %q", bad, text)
				}
			}
			// The only link is the app's own.
			if strings.Count(msg, "](") != 1 || !strings.HasSuffix(msg, "]("+testPanel+")") {
				t.Errorf("message:\n%s", msg)
			}
		})
	}
	got := notificationMessage(Request{Code: "K7Q2", Label: "[click](https://evil)", Device: "d"}, "")
	if !strings.Contains(got, `Guest: \[click\]\(https\:\/\/evil\)`) {
		t.Errorf("message:\n%s", got)
	}
}

func TestNotificationTitleIsPlain(t *testing.T) {
	if got := notificationTitle("Anna's [stay]\n"); got != "Someone wants in: Anna's [stay]" {
		t.Errorf("title %q", got)
	}
}

func TestNotificationDismissedOnDecision(t *testing.T) {
	for _, c := range []struct {
		name   string
		decide func(e *notifyEnv, l model.Link, reqID string)
	}{
		{"admin approve", func(e *notifyEnv, _ model.Link, id string) {
			if err := e.m.Decide(id, true); err != nil {
				t.Fatal(err)
			}
		}},
		{"admin deny", func(e *notifyEnv, _ model.Link, id string) {
			if err := e.m.Decide(id, false); err != nil {
				t.Fatal(err)
			}
		}},
		{"HA event approve", func(e *notifyEnv, _ model.Link, id string) {
			e.m.HandleDecisionEvent(json.RawMessage(`{"request_id":"` + id + `","approve":true}`))
		}},
		{"HA event deny", func(e *notifyEnv, _ model.Link, id string) {
			e.m.HandleDecisionEvent(json.RawMessage(`{"request_id":"` + id + `","approve":false}`))
		}},
		{"reset", func(e *notifyEnv, l model.Link, _ string) {
			if err := e.m.ResetDevices(l.ID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newNotifyEnv(t, nil)
			l := e.newLink(manual)
			guest := newBrowser(iPhoneUA)
			want(t, "ask", e.ask(l.ID, guest, "Anna"), 200, "pending")
			id := e.requestID(l.ID)
			c.decide(e, l, id)
			calls := e.calls(2)
			wantCall(t, calls[0], "create", id)
			wantCall(t, calls[1], "dismiss", id)
			// The guest picking up the result, and the request's later
			// expiry, send nothing more.
			e.call(e.m.Status, l.ID, guest, "")
			e.clock.Add(2 * DefaultPendingTTL)
			e.m.Pending("")
			e.quiet(2)
		})
	}
}

func TestNotificationDismissedBySelfService(t *testing.T) {
	e := newNotifyEnv(t, nil)
	l, approver, _ := e.claimed(both)
	want(t, "ask", e.ask(l.ID, newBrowser(androidUA), "Bo"), 200, "pending")
	id := e.requestID(l.ID)
	want(t, "deny", e.call(e.m.DecideByDevice, l.ID, approver, decideBody(id, false)), 200, "denied")
	// The first two calls are the approver's own request.
	calls := e.calls(4)
	wantCall(t, calls[2], "create", id)
	wantCall(t, calls[3], "dismiss", id)
}

// An expired request is dismissed by the expiry loop, with no guest or
// admin call to notice it.
func TestNotificationDismissedOnExpiry(t *testing.T) {
	e := newNotifyEnv(t, nil)
	l := e.newLink(manual)
	want(t, "ask", e.ask(l.ID, newBrowser(iPhoneUA), "Anna"), 200, "pending")
	id := e.requestID(l.ID)
	e.calls(1)
	e.clock.Add(DefaultPendingTTL - time.Second)
	e.wakeExpiry()
	e.quiet(1)
	e.clock.Add(time.Second)
	e.wakeExpiry()
	wantCall(t, e.calls(2)[1], "dismiss", id)
	e.m.mu.Lock()
	n := len(e.m.byID)
	e.m.mu.Unlock()
	if n != 0 {
		t.Errorf("%d requests kept after expiry", n)
	}
}

// wakeExpiry makes the expiry loop look at the test clock now. In
// production the loop's timer fires at the expiry.
func (e *notifyEnv) wakeExpiry() {
	e.m.wake <- struct{}{}
}

func TestNotificationOff(t *testing.T) {
	e := newEnv(t, Options{}) // no Notifier: approval.persistent_notification is false
	l := e.newLink(manual)
	want(t, "ask", e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}"), 200, "pending")
	if err := e.m.Decide(e.requestID(l.ID), false); err != nil {
		t.Fatal(err)
	}
	// A nil Notifier takes the calls and does nothing.
	var n *Notifier
	n.created(Request{}, "")
	n.ended(Request{})
}

func TestNotificationNotForClaim(t *testing.T) {
	e := newNotifyEnv(t, nil)
	l := e.newLink(claim)
	want(t, "claim", e.ask(l.ID, newBrowser(iPhoneUA), "Anna"), 200, "approved")
	e.quiet(0)
}

// HA failing or hanging never breaks the request.
func TestNotificationHAFailure(t *testing.T) {
	for _, c := range []struct {
		name    string
		respond func(release <-chan struct{}) func(hatest.ServiceCall) (json.RawMessage, error)
	}{
		{"error", func(<-chan struct{}) func(hatest.ServiceCall) (json.RawMessage, error) {
			return func(hatest.ServiceCall) (json.RawMessage, error) { return nil, errors.New("HA is down") }
		}},
		{"hang", func(release <-chan struct{}) func(hatest.ServiceCall) (json.RawMessage, error) {
			return func(hatest.ServiceCall) (json.RawMessage, error) { <-release; return nil, nil }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newNotifyEnv(t, nil)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) }) // runs before the env's cleanup
			e.fake.Respond = c.respond(release)
			l := e.newLink(manual)
			guest := newBrowser(iPhoneUA)
			start := time.Now()
			want(t, "ask", e.ask(l.ID, guest, "Anna"), 200, "pending")
			id := e.requestID(l.ID)
			if err := e.m.Decide(id, true); err != nil {
				t.Fatal(err)
			}
			want(t, "pick up", e.call(e.m.Status, l.ID, guest, ""), 200, "approved")
			if d := time.Since(start); d > time.Second {
				t.Errorf("the guest waited %v for HA", d)
			}
			if c.name == "error" {
				e.calls(2)
				deadline := time.Now().Add(5 * time.Second)
				for !strings.Contains(e.logs.String(), "HA is down") && time.Now().Before(deadline) {
					time.Sleep(5 * time.Millisecond)
				}
				if !strings.Contains(e.logs.String(), "HA is down") {
					t.Error("the failure is not logged")
				}
			}
		})
	}
}
