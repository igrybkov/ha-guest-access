package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func newLogger(t *testing.T, enabled bool, maxBytes int64) (*Logger, string, *bytes.Buffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	var buf bytes.Buffer
	l, err := New(Options{Path: path, Enabled: enabled, MaxBytes: maxBytes,
		Logger: slog.New(slog.NewJSONHandler(&buf, nil)), Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l, path, &buf
}

func TestLogWritesFileAndSlog(t *testing.T) {
	l, path, buf := newLogger(t, true, 0)
	d := 120
	l.Log(Record{Event: "guest.act.denied", Result: ResultDenied, Reason: "not_granted", LinkID: "l1",
		EntityID: "light.x", Action: "light.turn_on", DataKeys: []string{"brightness"}, DistanceM: &d,
		User: &User{Name: "anna"}})
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", st, err)
	}
	raw, _ := os.ReadFile(path)
	var r Record
	if err := json.Unmarshal(bytes.TrimSpace(raw), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Time.Equal(t0) || r.Event != "guest.act.denied" || r.Reason != "not_granted" || *r.DistanceM != 120 || r.User.Name != "anna" {
		t.Errorf("record = %+v", r)
	}
	for _, want := range []string{`"msg":"audit"`, `"event":"guest.act.denied"`, `"reason":"not_granted"`, `"user_name":"anna"`, `"distance_m":120`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("slog lacks %s: %s", want, buf)
		}
	}
}

// With the file off, records still reach slog, and no file appears.
func TestDisabledWritesNoFile(t *testing.T) {
	l, path, buf := newLogger(t, false, 0)
	l.Log(Record{Event: "app.start"})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file exists: %v", err)
	}
	if !strings.Contains(buf.String(), "app.start") {
		t.Errorf("slog = %s", buf)
	}
	if rs, err := l.Query(Query{}); err != nil || len(rs) != 0 {
		t.Errorf("query = %v, %v", rs, err)
	}
}

// The file rotates at the size cap: the current file and one older file,
// never more. Query reads both, newest first.
func TestRotation(t *testing.T) {
	l, path, _ := newLogger(t, true, 1024)
	for i := range 40 {
		l.Log(Record{Event: "guest.act.allowed", LinkID: fmt.Sprintf("l%02d", i)})
	}
	for _, p := range []string{path, path + ".1"} {
		st, err := os.Stat(p)
		if err != nil || st.Size() > 1024 || st.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", p, st, err)
		}
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Error("a third file exists")
	}
	rs, err := l.Query(Query{})
	if err != nil || len(rs) == 0 || rs[0].LinkID != "l39" {
		t.Fatalf("query: %v %v", rs, err)
	}
	for i := 1; i < len(rs); i++ {
		if rs[i].LinkID >= rs[i-1].LinkID {
			t.Fatalf("not newest first: %s then %s", rs[i-1].LinkID, rs[i].LinkID)
		}
	}
}

func TestQueryFilters(t *testing.T) {
	l, _, _ := newLogger(t, true, 0)
	l.Log(Record{Event: "guest.act.allowed", LinkID: "a"})
	l.Log(Record{Event: "guest.act.denied", LinkID: "b"})
	l.Log(Record{Event: "admin.link.create", LinkID: "a"})
	l.Log(Record{Event: "guest.password.fail", LinkID: "ab"})
	for _, c := range []struct {
		q    Query
		want []string
	}{
		{Query{}, []string{"guest.password.fail", "admin.link.create", "guest.act.denied", "guest.act.allowed"}},
		{Query{LinkID: "a"}, []string{"admin.link.create", "guest.act.allowed"}},
		{Query{EventPrefix: "guest.act"}, []string{"guest.act.denied", "guest.act.allowed"}},
		{Query{EventPrefix: "guest.", Limit: 1}, []string{"guest.password.fail"}},
	} {
		rs, err := l.Query(c.q)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rs {
			got = append(got, r.Event)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%+v: %v, want %v", c.q, got, c.want)
		}
	}
}

// Guest text can't add lines or grow without bound.
func TestCleanCapsAndStripsControls(t *testing.T) {
	l, path, _ := newLogger(t, true, 0)
	l.Log(Record{Event: "guest.act.denied", EntityID: "light.x\n{\"event\":\"forged\"}", PasswordLabel: strings.Repeat("a", 1000),
		DataKeys: strings.Split(strings.Repeat("k,", 40), ",")})
	raw, _ := os.ReadFile(path)
	if n := bytes.Count(raw, []byte("\n")); n != 1 {
		t.Errorf("%d lines", n)
	}
	var r Record
	_ = json.Unmarshal(raw, &r)
	if len(r.PasswordLabel) != maxField || len(r.DataKeys) != maxDataKeys || strings.ContainsAny(r.EntityID, "\n\r") {
		t.Errorf("record = %+v", r)
	}
}

func TestNilLoggerIsSafe(t *testing.T) {
	var l *Logger
	l.Log(Record{Event: "x"})
	if rs, err := l.Query(Query{}); err != nil || len(rs) != 0 {
		t.Error("nil query")
	}
	if l.Close() != nil {
		t.Error("nil close")
	}
}

func TestConcurrentLog(t *testing.T) {
	l, path, _ := newLogger(t, true, 4096)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				l.Log(Record{Event: "guest.act.allowed", LinkID: fmt.Sprint(i, "-", j)})
			}
		})
	}
	wg.Wait()
	for _, p := range []string{path, path + ".1"} {
		raw, _ := os.ReadFile(p)
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			if !json.Valid(line) {
				t.Fatalf("%s: torn line %q", p, line)
			}
		}
	}
}

func TestThrottle(t *testing.T) {
	th := NewThrottle(time.Minute)
	if ok, _ := th.Allow("ip", t0); !ok {
		t.Fatal("first denied")
	}
	for range 5 {
		if ok, _ := th.Allow("ip", t0.Add(time.Second)); ok {
			t.Fatal("inside the period")
		}
	}
	if ok, _ := th.Allow("other", t0.Add(time.Second)); !ok {
		t.Error("another key is held back")
	}
	if ok, n := th.Allow("ip", t0.Add(time.Minute)); !ok || n != 5 {
		t.Errorf("after the period: %v %d", ok, n)
	}
	for i := range maxThrottleKeys + 10 {
		th.Allow(fmt.Sprint(i), t0.Add(2*time.Minute))
	}
	if len(th.keys) > maxThrottleKeys {
		t.Errorf("%d keys", len(th.keys))
	}
}

func TestCap(t *testing.T) {
	c := NewCap(3, time.Minute)
	for i := range 3 {
		if ok, s := c.Allow(t0.Add(time.Duration(i) * time.Second)); !ok || s != 0 {
			t.Fatalf("record %d: %v %d", i, ok, s)
		}
	}
	for range 5 {
		if ok, _ := c.Allow(t0.Add(10 * time.Second)); ok {
			t.Fatal("over the cap")
		}
	}
	if s := c.Flush(t0.Add(30 * time.Second)); s != 0 {
		t.Errorf("flush inside the window: %d", s)
	}
	// The next window reports the five left out, once.
	if ok, s := c.Allow(t0.Add(time.Minute)); !ok || s != 5 {
		t.Errorf("new window: %v %d", ok, s)
	}
	if _, s := c.Allow(t0.Add(time.Minute + time.Second)); s != 0 {
		t.Errorf("summary repeated: %d", s)
	}
	// Flush ends a window with no capped record coming.
	for range 4 {
		c.Allow(t0.Add(time.Minute + 2*time.Second))
	}
	if s := c.Flush(t0.Add(3 * time.Minute)); s != 3 { // 2 + 4 tries, 3 let through
		t.Errorf("flush after the window: %d", s)
	}
	// No cap.
	c = NewCap(0, time.Minute)
	for range 1000 {
		if ok, _ := c.Allow(t0); !ok {
			t.Fatal("max 0 capped")
		}
	}
}
