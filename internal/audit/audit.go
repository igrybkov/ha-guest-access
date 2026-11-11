// Package audit records who did what and when, for everything that
// touches security: guest actions and their denials, passwords, device
// approval, rate limits and every admin write.
//
// Each record goes to two sinks:
//
//   - the app log, through slog at info level, with structured attributes;
//   - a JSON Lines file in /data (when the file is on), rotated by size.
//     The app keeps the current file and one older file, <name>.1.
//
// Records never hold slugs, passwords, tokens, cookies, coordinates or
// raw headers. Callers fill only the fields below; there is no free-form
// map of request data.
package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Results.
const (
	ResultAllowed = "allowed"
	ResultDenied  = "denied"
	ResultOK      = "ok"
	ResultFailed  = "failed"
)

// FileName is the audit file in the data dir.
const FileName = "audit.jsonl"

// MaxQueryLimit caps the records one Query returns.
const MaxQueryLimit = 1000

// maxField caps every text field, in bytes. Some come from guests.
const maxField = 128

// maxDataKeys caps DataKeys.
const maxDataKeys = 16

// Record is one audit record. Empty fields are left out of the JSON.
type Record struct {
	Time  time.Time `json:"time"`
	Event string    `json:"event"`
	// Result is allowed, denied, ok or failed.
	Result string `json:"result,omitempty"`
	// Reason is the internal reason for a denial or failure, such as
	// not_granted or bad_data:brightness. The guest never sees it.
	Reason string `json:"reason,omitempty"`

	// Who: the link (never its slug), the guest and the client.
	LinkID        string `json:"link_id,omitempty"`
	LinkName      string `json:"link_name,omitempty"`
	PasswordLabel string `json:"password_label,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	DeviceLabel   string `json:"device_label,omitempty"`
	// Device is a short summary of the User-Agent, like "iPhone, Safari".
	Device     string `json:"device,omitempty"`
	ClientIP   string `json:"client_ip,omitempty"`
	Peer       string `json:"peer,omitempty"`
	ViaProxy   bool   `json:"via_proxy,omitempty"`
	Unresolved bool   `json:"unresolved,omitempty"`
	// User is the HA user behind an admin request.
	User *User `json:"user,omitempty"`

	// What.
	Method   string   `json:"method,omitempty"`
	Route    string   `json:"route,omitempty"`
	Status   int      `json:"status,omitempty"`
	EntityID string   `json:"entity_id,omitempty"`
	Action   string   `json:"action,omitempty"`
	DataKeys []string `json:"data_keys,omitempty"`
	// Target is the id an admin write acted on: a template, password,
	// device or pending request id.
	Target string `json:"target,omitempty"`
	// RequestID and Code identify a device request.
	RequestID string `json:"request_id,omitempty"`
	Code      string `json:"code,omitempty"`
	// By says who decided a device request: admin, ha, device:<id> or
	// claim.
	By string `json:"by,omitempty"`
	// DistanceM and AccuracyM are a location check, in meters. The
	// coordinates themselves are never recorded.
	DistanceM *int `json:"distance_m,omitempty"`
	AccuracyM *int `json:"accuracy_m,omitempty"`
	// Count is how many records of this kind and key were left out since
	// the last one (see Throttle).
	Count int `json:"count,omitempty"`
	// LimitsEnabled and Version describe the app at app.start.
	LimitsEnabled *bool  `json:"limits_enabled,omitempty"`
	Version       string `json:"version,omitempty"`
}

// User is the HA user from the Supervisor ingress headers.
type User struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// Options configure a Logger.
type Options struct {
	// Path is the audit file. Empty or Enabled false writes no file.
	Path    string
	Enabled bool
	// MaxBytes is the size at which the file is rotated. Default 2 MiB.
	MaxBytes int64
	Logger   *slog.Logger
	// Now is the clock. Default time.Now.
	Now func() time.Time
}

// Logger writes audit records. A nil *Logger drops every record, so code
// under test may leave it out. Safe for concurrent use.
type Logger struct {
	opts Options
	log  *slog.Logger

	mu     sync.Mutex
	f      *os.File
	size   int64
	closed bool // after Close, records reach slog only
}

// New returns a Logger. With the file on, it opens (or creates) the file
// with mode 0600.
func New(o Options) (*Logger, error) {
	if o.MaxBytes <= 0 {
		o.MaxBytes = 2 << 20
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	l := &Logger{opts: o, log: log}
	if l.fileOn() {
		if err := l.open(); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func (l *Logger) fileOn() bool { return l.opts.Enabled && l.opts.Path != "" }

func (l *Logger) open() error {
	f, err := os.OpenFile(l.opts.Path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("audit: open %s: %w", l.opts.Path, err)
	}
	// A file from an older run may have looser bits.
	_ = f.Chmod(0o600)
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("audit: stat %s: %w", l.opts.Path, err)
	}
	l.f, l.size = f, st.Size()
	return nil
}

// Close closes the file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Log writes r to the app log and, with the file on, to the file. It
// sets Time when it is zero and caps every text field. A file error is
// logged and never fails the caller.
func (l *Logger) Log(r Record) {
	if l == nil {
		return
	}
	if r.Time.IsZero() {
		r.Time = l.opts.Now()
	}
	r.Time = r.Time.UTC()
	r = clean(r)
	l.log.LogAttrs(context.Background(), slog.LevelInfo, "audit", attrs(r)...)
	if !l.fileOn() {
		return
	}
	line, err := json.Marshal(r)
	if err != nil {
		l.log.Warn("audit: encode", "err", err)
		return
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.write(line); err != nil {
		l.log.Warn("audit: write", "err", err)
	}
}

// write appends line, rotating first when it would pass MaxBytes. It
// requires l.mu.
func (l *Logger) write(line []byte) error {
	if l.closed {
		return nil
	}
	if l.f == nil {
		if err := l.open(); err != nil {
			return err
		}
	}
	if l.size > 0 && l.size+int64(len(line)) > l.opts.MaxBytes {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	n, err := l.f.Write(line)
	l.size += int64(n)
	return err
}

// rotate moves the file to <path>.1 and starts a new one. It requires
// l.mu.
func (l *Logger) rotate() error {
	l.f.Close()
	l.f = nil
	if err := os.Rename(l.opts.Path, l.opts.Path+".1"); err != nil {
		return fmt.Errorf("audit: rotate: %w", err)
	}
	return l.open()
}

// Query selects records from the file.
type Query struct {
	// LinkID keeps records of this link only.
	LinkID string
	// EventPrefix keeps records whose event starts with it, like
	// "guest.act" or "admin.".
	EventPrefix string
	// Limit caps the result. 0 or more than MaxQueryLimit means
	// MaxQueryLimit.
	Limit int
}

// Query returns matching records from the file and the older file,
// newest first. With the file off it returns none.
func (l *Logger) Query(q Query) ([]Record, error) {
	if l == nil || !l.fileOn() {
		return []Record{}, nil
	}
	if q.Limit <= 0 || q.Limit > MaxQueryLimit {
		q.Limit = MaxQueryLimit
	}
	// Hold the lock so a rotation can't move the files mid-read.
	l.mu.Lock()
	defer l.mu.Unlock()
	var ring []Record // the newest q.Limit matches, oldest first
	for _, p := range []string{l.opts.Path + ".1", l.opts.Path} {
		if err := scan(p, q, &ring); err != nil {
			return nil, err
		}
	}
	out := make([]Record, 0, len(ring))
	for i := len(ring) - 1; i >= 0; i-- {
		out = append(out, ring[i])
	}
	return out, nil
}

// scan reads one file, oldest first, and keeps the last q.Limit
// matches in ring. A missing file is empty. A line that does not parse
// is skipped.
func scan(path string, q Query, ring *[]Record) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("audit: read %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if q.LinkID != "" && !bytes.Contains(line, []byte(q.LinkID)) {
			continue
		}
		var r Record
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if (q.LinkID != "" && r.LinkID != q.LinkID) || !strings.HasPrefix(r.Event, q.EventPrefix) {
			continue
		}
		*ring = append(*ring, r)
		if len(*ring) > q.Limit {
			*ring = (*ring)[1:]
		}
	}
	return sc.Err()
}

// clean caps text fields and drops control characters, so a guest
// can't forge log lines or fill the file.
func clean(r Record) Record {
	for _, p := range []*string{
		&r.Event, &r.Result, &r.Reason, &r.LinkID, &r.LinkName, &r.PasswordLabel, &r.DeviceID, &r.DeviceLabel,
		&r.Device, &r.ClientIP, &r.Peer, &r.Method, &r.Route, &r.EntityID, &r.Action, &r.Target,
		&r.RequestID, &r.Code, &r.By, &r.Version,
	} {
		*p = Text(*p)
	}
	if r.User != nil {
		u := User{ID: Text(r.User.ID), Name: Text(r.User.Name), DisplayName: Text(r.User.DisplayName)}
		r.User = &u
		if u == (User{}) {
			r.User = nil
		}
	}
	if len(r.DataKeys) > maxDataKeys {
		r.DataKeys = r.DataKeys[:maxDataKeys]
	}
	keys := make([]string, len(r.DataKeys))
	for i, k := range r.DataKeys {
		keys[i] = Text(k)
	}
	r.DataKeys = keys
	return r
}

// Text caps s at 128 bytes of valid UTF-8 and drops control characters.
func Text(s string) string {
	if len(s) > maxField {
		s = s[:maxField]
	}
	s = strings.ToValidUTF8(s, "")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// attrs turns r into slog attributes, leaving out empty fields.
func attrs(r Record) []slog.Attr {
	out := []slog.Attr{slog.String("event", r.Event)}
	add := func(k, v string) {
		if v != "" {
			out = append(out, slog.String(k, v))
		}
	}
	add("result", r.Result)
	add("reason", r.Reason)
	add("link_id", r.LinkID)
	add("link_name", r.LinkName)
	add("password_label", r.PasswordLabel)
	add("device_id", r.DeviceID)
	add("device_label", r.DeviceLabel)
	add("device", r.Device)
	add("client_ip", r.ClientIP)
	add("peer", r.Peer)
	if r.ViaProxy {
		out = append(out, slog.Bool("via_proxy", true))
	}
	if r.Unresolved {
		out = append(out, slog.Bool("unresolved", true))
	}
	if r.User != nil {
		add("user_id", r.User.ID)
		add("user_name", r.User.Name)
		add("user_display_name", r.User.DisplayName)
	}
	add("method", r.Method)
	add("route", r.Route)
	if r.Status != 0 {
		out = append(out, slog.Int("status", r.Status))
	}
	add("entity_id", r.EntityID)
	add("action", r.Action)
	if len(r.DataKeys) > 0 {
		out = append(out, slog.String("data_keys", strings.Join(r.DataKeys, ",")))
	}
	add("target", r.Target)
	add("request_id", r.RequestID)
	add("code", r.Code)
	add("by", r.By)
	if r.DistanceM != nil {
		out = append(out, slog.Int("distance_m", *r.DistanceM))
	}
	if r.AccuracyM != nil {
		out = append(out, slog.Int("accuracy_m", *r.AccuracyM))
	}
	if r.Count != 0 {
		out = append(out, slog.Int("count", r.Count))
	}
	if r.LimitsEnabled != nil {
		out = append(out, slog.Bool("limits_enabled", *r.LimitsEnabled))
	}
	add("version", r.Version)
	return out
}
