package main

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/igrybkov/ha-guest-access/internal/config"
)

// An event stream stays open, and keeps sending, long after the read
// timeout. The read timeout bounds reading the request only.
func TestE2EStreamOutlivesReadTimeout(t *testing.T) {
	const readTimeout = 300 * time.Millisecond
	a := startApp(t, appOpts{options: func(o *config.Options) {
		o.HTTPReadTimeout = readTimeout
		o.StreamHeartbeat = 50 * time.Millisecond
	}})
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	req, _ := http.NewRequest("GET", a.guest+"/g/"+l.Slug+"/events", nil)
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events: %d", resp.StatusCode)
	}
	lines := make(chan string, 1000)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	opened := time.Now()
	// Read for four read timeouts. The stream must stay open the whole
	// time, with pings still coming in at the end.
	pingsLate := 0
	for until := time.After(4 * readTimeout); ; {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("stream closed after %v", time.Since(opened))
			}
			if line == ": ping" && time.Since(opened) > 2*readTimeout {
				pingsLate++
			}
			continue
		case <-until:
		}
		break
	}
	if pingsLate == 0 {
		t.Fatal("no ping arrived after the read timeout")
	}
	// A state change still gets through.
	a.ha.Emit("state_changed", map[string]any{"entity_id": "light.guest_room",
		"new_state": map[string]any{"entity_id": "light.guest_room", "state": "on", "attributes": map[string]any{}}})
	for deadline := time.After(5 * time.Second); ; {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stream closed")
			}
			if strings.Contains(line, `"state":"on"`) {
				return
			}
		case <-deadline:
			t.Fatal("state change never arrived")
		}
	}
}

// A client that sends its body slowly is cut off at the read timeout.
func TestE2ESlowBodyCutOff(t *testing.T) {
	const readTimeout = 300 * time.Millisecond
	a := startApp(t, appOpts{options: func(o *config.Options) { o.HTTPReadTimeout = readTimeout }})
	l := a.newLink(map[string]any{"templates": []string{"guest_room"}})
	conn, err := net.Dial("tcp", strings.TrimPrefix(a.guest, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	host := strings.TrimPrefix(a.guest, "http://")
	_, err = io.WriteString(conn, "POST /g/"+l.Slug+"/act HTTP/1.1\r\nHost: "+host+"\r\nOrigin: "+a.guest+
		"\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err = io.ReadAll(conn) // ends when the server closes the connection
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("connection held for %v (err %v)", took, err)
	}
	if n := len(a.fake.ServiceCalls()); n != 0 {
		t.Errorf("%d HA calls", n)
	}
}

// The servers get the read bounds from the options.
func TestSetBounds(t *testing.T) {
	o := config.DefaultOptions()
	s := newServer(t.Context(), http.NotFoundHandler(), slog.New(slog.DiscardHandler))
	if s.ReadTimeout != o.HTTPReadTimeout || s.MaxHeaderBytes != o.MaxHeaderBytes || s.WriteTimeout != 0 {
		t.Errorf("defaults: read %v, header %d, write %v", s.ReadTimeout, s.MaxHeaderBytes, s.WriteTimeout)
	}
	o.HTTPReadTimeout, o.MaxHeaderBytes = 2*time.Second, 8<<10
	setBounds(s, o)
	if s.ReadTimeout != 2*time.Second || s.ReadHeaderTimeout != 2*time.Second || s.MaxHeaderBytes != 8<<10 {
		t.Errorf("set: read %v, header timeout %v, header %d", s.ReadTimeout, s.ReadHeaderTimeout, s.MaxHeaderBytes)
	}
}

// Local access on with no trusted proxy logs a warning at start.
func TestE2ELocalAccessWithoutProxyWarns(t *testing.T) {
	var buf syncBuf
	startApp(t, appOpts{options: func(o *config.Options) {
		o.Settings.LocalAccess.Enabled = true
		o.Settings.TrustedProxies = nil
	}, logger: slog.New(slog.NewTextHandler(&buf, nil))})
	if !strings.Contains(buf.String(), "trusted_proxies is empty") {
		t.Errorf("no warning in the log:\n%s", buf.String())
	}
}
