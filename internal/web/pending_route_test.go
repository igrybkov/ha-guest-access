package web

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/igrybkov/ha-guest-access/internal/web/ui"
)

// The HA notification links to PendingRoute, and the admin page opens
// its pending panel on the same value. The two must not drift apart.
func TestPendingRouteMatchesAdminPage(t *testing.T) {
	raw, err := fs.ReadFile(ui.FS, "admin/lib.js")
	if err != nil {
		t.Fatal(err)
	}
	want := "export const PENDING_ROUTE = '" + PendingRoute + "';"
	if !strings.Contains(string(raw), want) {
		t.Errorf("admin/lib.js has no %q", want)
	}
}
