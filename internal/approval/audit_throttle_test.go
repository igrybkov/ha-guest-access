package approval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/audit"
)

// A device that keeps asking past the pending cap gives one refused
// record per period, and the next one counts the ones left out.
func TestRefusedRequestsThrottled(t *testing.T) {
	path := filepath.Join(t.TempDir(), audit.FileName)
	al, err := audit.New(audit.Options{Path: path, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { al.Close() })
	e := newEnv(t, Options{MaxPending: 1, Audit: al})
	l := e.newLink(manual)
	want(t, "first", e.call(e.m.RequestAccess, l.ID, newBrowser(iPhoneUA), "{}"), 200, "pending")
	for range 50 {
		want(t, "refused", e.call(e.m.RequestAccess, l.ID, newBrowser(androidUA), "{}"), 429, "try_later")
	}
	e.clock.Add(refusedAuditPeriod)
	e.call(e.m.RequestAccess, l.ID, newBrowser(androidUA), "{}")

	raw, _ := os.ReadFile(path)
	var refused []audit.Record
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var r audit.Record
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		if r.Event == "device.request" && r.Result == audit.ResultDenied {
			refused = append(refused, r)
		}
	}
	if len(refused) != 2 || refused[0].Count != 0 || refused[1].Count != 49 || refused[1].Reason != "too_many_pending" {
		t.Errorf("refused records = %+v", refused)
	}
}
