package config

import (
	"os"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

func readManifest(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../guest_access/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// templates.yaml lives in /homeassistant/guest_access (DefaultConfigDir)
// and the docs say it is in the HA config, in git. The manifest maps no
// folder, so the app writes templates into its own container: an update
// or rebuild deletes every template, and every link then grants nothing.
func TestSecManifestMapsHAConfig(t *testing.T) {
	m := readManifest(t)
	maps, _ := m["map"].([]any)
	found := false
	for _, e := range maps {
		switch e := e.(type) {
		case string:
			found = found || e == "homeassistant_config:rw" || e == "homeassistant_config"
		case map[string]any:
			found = found || (e["type"] == "homeassistant_config" && e["read_only"] != true)
		}
	}
	if !found {
		t.Fatalf("config.yaml has no read-write homeassistant_config map; map = %v", m["map"])
	}
}

// The manifest asks for no privilege beyond the HA Core API, publishes
// only the guest port, and leaves the admin port to ingress.
func TestSecManifestPrivileges(t *testing.T) {
	m := readManifest(t)
	for _, k := range []string{"host_network", "privileged", "full_access", "hassio_api", "hassio_role", "auth_api", "docker_api", "host_pid", "host_ipc", "host_dbus", "devices", "usb", "uart", "kernel_modules", "apparmor", "ingress_stream", "backup_exclude"} {
		if v, ok := m[k]; ok && v != false {
			t.Errorf("manifest sets %s: %v", k, v)
		}
	}
	if m["homeassistant_api"] != true {
		t.Errorf("homeassistant_api = %v", m["homeassistant_api"])
	}
	ports, _ := m["ports"].(map[string]any)
	if len(ports) != 1 || ports["8888/tcp"] == nil {
		t.Errorf("ports = %v", ports)
	}
	if v, ok := m["panel_admin"]; ok && v != true {
		t.Errorf("panel_admin = %v: the admin panel must stay admin only", v)
	}
	if m["ingress"] != true || m["ingress_port"] != 8099 {
		t.Errorf("ingress = %v port %v", m["ingress"], m["ingress_port"])
	}
	if slices.Contains([]any{"rw"}, m["map"]) {
		t.Error("unexpected map")
	}
}
