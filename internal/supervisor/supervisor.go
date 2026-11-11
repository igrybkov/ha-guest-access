// Package supervisor reads facts about this app from the Supervisor API.
// Only the app's own info is read. The endpoint needs no hassio_api
// permission: Supervisor lets every app read /addons/self/info with its
// SUPERVISOR_TOKEN (the api_bypass list in
// supervisor/api/middleware/security.py of home-assistant/supervisor).
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// DefaultURL is the Supervisor API as an app sees it.
const DefaultURL = "http://supervisor"

// maxInfoBytes caps the self info response. The real one is a few KiB.
const maxInfoBytes = 1 << 20

// Info is the part of /addons/self/info the app uses.
type Info struct {
	// Slug is the app's slug as Supervisor knows it. For an app from a
	// repository it has the repository hash in front, such as
	// 1a2b3c4d_guest_access.
	Slug string `json:"slug"`
	// IngressURL is the ingress path of the app, such as
	// /api/hassio_ingress/<token>/. HA's frontend loads it in an iframe.
	IngressURL string `json:"ingress_url"`
	// IngressPanel is true when the app shows in HA's sidebar.
	IngressPanel bool `json:"ingress_panel"`
}

// slugRe is what a Supervisor slug may hold. A slug goes into a link in
// an HA notification, so anything else is refused.
var slugRe = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)

// Client calls the Supervisor API.
type Client struct {
	URL   string // DefaultURL under Supervisor
	Token string // SUPERVISOR_TOKEN. Never log it.
	HTTP  *http.Client
}

// SelfInfo reads /addons/self/info. ctx bounds the call.
func (c Client) SelfInfo(ctx context.Context) (Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/addons/self/info", nil)
	if err != nil {
		return Info{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("supervisor self info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("supervisor self info: %s", resp.Status)
	}
	var env struct {
		Result string `json:"result"`
		Data   Info   `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxInfoBytes)).Decode(&env); err != nil {
		return Info{}, fmt.Errorf("supervisor self info: %w", err)
	}
	if env.Result != "ok" {
		return Info{}, fmt.Errorf("supervisor self info: result %q", env.Result)
	}
	if !slugRe.MatchString(env.Data.Slug) {
		return Info{}, errors.New("supervisor self info: no valid slug")
	}
	return env.Data, nil
}

// PanelPath is the path of the app's page in HA's frontend, plus tail
// (such as "/pending"). It has no host, so it works on any HA URL.
//
// HA registers a built-in panel at /app (core,
// homeassistant/components/hassio/__init__.py). The frontend panel
// (frontend, src/panels/app/ha-panel-app.ts) reads the app slug from the
// first path part after /app and loads the app's ingress_url in an
// iframe. It passes the rest of the path to the app as the route of its
// "home-assistant/properties" message. This works whether or not the app
// shows in the sidebar; the sidebar panel lives at /<slug> instead
// (homeassistant/components/hassio/addon_panel.py).
//
// The /app panel came with HA 2026.2 (frontend 20260128.0). Older HA has
// no page at this path.
func PanelPath(slug, tail string) string {
	return "/app/" + slug + tail
}
