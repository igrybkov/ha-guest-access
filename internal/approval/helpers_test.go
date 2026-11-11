package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/access"
	"github.com/igrybkov/ha-guest-access/internal/links"
	"github.com/igrybkov/ha-guest-access/internal/migrate"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

const (
	iPhoneUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	androidUA = "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36"
)

// clock is a test clock safe for parallel use.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fired is one recorded FireEvent call.
type fired struct {
	Type string
	Data map[string]any
}

type env struct {
	t      *testing.T
	m      *Manager
	links  *links.Store
	clock  *clock
	signer *access.Signer
	logs   *syncBuffer

	mu     sync.Mutex
	events []fired
	fail   atomic.Bool // FireEvent fails while set
}

// syncBuffer is a bytes.Buffer safe for parallel writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newEnv(t *testing.T, o Options) *env {
	t.Helper()
	e := &env{t: t, clock: &clock{now: t0}, signer: access.NewSigner(bytes.Repeat([]byte{7}, access.MinKeyLen)), logs: &syncBuffer{}}
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var n atomic.Int64
	e.links = links.New(migrate.OpenData(t.TempDir(), log), links.Options{
		NewSlug: func() string { return fmt.Sprintf("random-slug-%06d", n.Add(1)) },
		Now:     e.clock.Now,
		Logger:  log,
	})
	o.Links, o.Signer, o.Now, o.Logger = e.links, e.signer, e.clock.Now, log
	o.FireEvent = func(_ context.Context, typ string, data map[string]any) error {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.events = append(e.events, fired{typ, data})
		if e.fail.Load() {
			return fmt.Errorf("HA is down")
		}
		return nil
	}
	e.m = New(o)
	return e
}

func (e *env) fired() []fired {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]fired(nil), e.events...)
}

// newLink creates an active link with approval a.
func (e *env) newLink(a model.Approval) model.Link {
	e.t.Helper()
	l, err := e.links.Create(model.Link{
		Name: "Anna", ValidUntil: t0.Add(48 * time.Hour), TemplateIDs: []string{"house_entry"}, Approval: a,
	}, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

// link loads link id fresh, as web does on every request.
func (e *env) link(id string) model.Link {
	e.t.Helper()
	l, err := e.links.Get(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

// browser keeps cookies across calls, like a real one. Cookies are kept
// per name only: every call in a test is on one link's path.
type browser struct {
	ua      string
	ip      string
	https   bool
	mu      sync.Mutex
	cookies map[string]*http.Cookie
}

func newBrowser(ua string) *browser {
	return &browser{ua: ua, ip: "203.0.113.7", cookies: map[string]*http.Cookie{}}
}

func (b *browser) cookie(name string) *http.Cookie {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cookies[name]
}

func (b *browser) set(name, value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cookies[name] = &http.Cookie{Name: name, Value: value}
}

type handler func(http.ResponseWriter, *http.Request, model.Link, access.Client, string)

// result is one response, decoded.
type result struct {
	code    int
	status  string
	codeStr string // "code" field
	body    map[string]any
	header  http.Header
	cookies []*http.Cookie
}

// call runs h as browser b. A nil b sends no cookies.
func (e *env) call(h handler, linkID string, b *browser, body string) result {
	e.t.Helper()
	l := e.link(linkID)
	method := http.MethodGet
	var rd *strings.Reader
	if body != "" {
		method = http.MethodPost
	}
	rd = strings.NewReader(body)
	r := httptest.NewRequest(method, "/g/"+l.Slug+"/device", rd)
	c := access.Client{IP: netip.MustParseAddr("203.0.113.7")}
	if b != nil {
		r.Header.Set("User-Agent", b.ua)
		c.IP, c.HTTPS = netip.MustParseAddr(b.ip), b.https
		b.mu.Lock()
		for _, ck := range b.cookies {
			r.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
		}
		b.mu.Unlock()
	}
	w := httptest.NewRecorder()
	h(w, r, l, c, "Anna")
	return e.result(w, b)
}

// admit runs Admit as browser b.
func (e *env) admit(linkID string, b *browser) (string, bool, result) {
	e.t.Helper()
	var id string
	var ok bool
	res := e.call(func(w http.ResponseWriter, r *http.Request, l model.Link, c access.Client, label string) {
		id, ok = e.m.Admit(w, r, l, c, label)
	}, linkID, b, "")
	return id, ok, res
}

func (e *env) result(w *httptest.ResponseRecorder, b *browser) result {
	res := result{code: w.Code, header: w.Header(), cookies: w.Result().Cookies()}
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &res.body); err != nil {
			e.t.Fatalf("body %q: %v", w.Body.String(), err)
		}
		res.status, _ = res.body["status"].(string)
		res.codeStr, _ = res.body["code"].(string)
	}
	if b != nil {
		b.mu.Lock()
		for _, ck := range res.cookies {
			if ck.MaxAge < 0 {
				delete(b.cookies, ck.Name)
			} else {
				b.cookies[ck.Name] = ck
			}
		}
		b.mu.Unlock()
	}
	return res
}

// requestID returns the id of the only pending request of linkID.
func (e *env) requestID(linkID string) string {
	e.t.Helper()
	p := e.m.Pending(linkID)
	if len(p) != 1 {
		e.t.Fatalf("%d pending requests, want 1", len(p))
	}
	return p[0].ID
}

func want(t *testing.T, what string, res result, code int, status string) {
	t.Helper()
	if res.code != code || res.status != status {
		t.Fatalf("%s: %d %q (%v), want %d %q", what, res.code, res.status, res.body, code, status)
	}
}
