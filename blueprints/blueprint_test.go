// Package blueprints holds the HA blueprints shipped with the app. This
// test checks they parse and still match the event names the app uses.
package blueprints

import (
	"os"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/igrybkov/ha-guest-access/internal/approval"
)

// get walks a parsed YAML document by mapping keys and list indexes.
func get(t *testing.T, n *yaml.Node, path ...any) *yaml.Node {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			var next *yaml.Node
			if n.Kind == yaml.MappingNode {
				for i := 0; i+1 < len(n.Content); i += 2 {
					if n.Content[i].Value == k {
						next = n.Content[i+1]
					}
				}
			}
			if next == nil {
				t.Fatalf("no key %q at %v", k, path)
			}
			n = next
		case int:
			if n.Kind != yaml.SequenceNode || k >= len(n.Content) {
				t.Fatalf("no item %d at %v", k, path)
			}
			n = n.Content[k]
		}
	}
	return n
}

func TestDeviceApprovalBlueprint(t *testing.T) {
	raw, err := os.ReadFile("guest_access_device_approval.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	root := doc.Content[0]

	if v := get(t, root, "blueprint", "domain").Value; v != "automation" {
		t.Errorf("domain = %q", v)
	}
	if v := get(t, root, "blueprint", "input", "notify_device", "selector", "device", "filter", "integration").Value; v != "mobile_app" {
		t.Errorf("notify device filter = %q", v)
	}
	if v := get(t, root, "mode").Value; v != "parallel" {
		t.Errorf("mode = %q", v)
	}
	// The new trigger syntax needs HA 2024.10.
	if v := get(t, root, "blueprint", "homeassistant", "min_version").Value; v < "2024.10.0" {
		t.Errorf("min_version = %q", v)
	}

	var events []string
	for _, tr := range get(t, root, "triggers").Content {
		if get(t, tr, "trigger").Value != "event" {
			t.Errorf("trigger %v is not an event", tr)
		}
		events = append(events, get(t, tr, "event_type").Value)
	}
	if !slices.Equal(events, []string{approval.EventDeviceRequest, "mobile_app_notification_action"}) {
		t.Errorf("trigger events = %v", events)
	}

	choose := get(t, root, "actions", 0, "choose")
	notify := get(t, choose, 0, "sequence", 0)
	if get(t, notify, "device_id").Tag != "!input" || get(t, notify, "type").Value != "notify" {
		t.Error("notification does not go to the input device")
	}
	if msg := get(t, notify, "message").Value; !strings.Contains(msg, "d.code") || !strings.Contains(msg, "d.device") {
		t.Errorf("message lacks the code or device: %q", msg)
	}
	if title := get(t, notify, "title").Value; !strings.Contains(title, "link_name") || !strings.Contains(title, "label") {
		t.Errorf("title lacks the link or label: %q", title)
	}
	acts := get(t, notify, "data", "actions")
	if len(acts.Content) != 2 ||
		!strings.Contains(get(t, acts, 0, "action").Value, "request_id") ||
		!strings.Contains(get(t, acts, 1, "action").Value, "request_id") {
		t.Error("notification actions do not carry the request id")
	}

	// Approve and Deny fire the decision event with a literal bool, which
	// the app requires.
	for i, wantApprove := range map[int]string{1: "true", 2: "false"} {
		ev := get(t, choose, i, "sequence", 0)
		if get(t, ev, "event").Value != approval.EventDeviceDecision {
			t.Errorf("branch %d fires %q", i, get(t, ev, "event").Value)
		}
		a := get(t, ev, "event_data", "approve")
		if a.Tag != "!!bool" || a.Value != wantApprove {
			t.Errorf("branch %d approve = %s %q, want a literal bool", i, a.Tag, a.Value)
		}
		if !strings.Contains(get(t, ev, "event_data", "request_id").Value, "trigger.event.data.action") {
			t.Errorf("branch %d request_id does not come from the tapped action", i)
		}
	}
}
