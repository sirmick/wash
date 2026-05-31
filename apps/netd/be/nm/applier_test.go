package nm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
)

// The NM applier satisfies the backend.Applier seam.
var _ backend.Applier = (*Applier)(nil)

func TestCapabilitiesGreyRouterFeatures(t *testing.T) {
	c := Capabilities()
	// Type-2 covers these:
	for _, k := range []string{"network/interface", "network/device", "wireless/wifi-iface"} {
		if !c.Kinds[k] {
			t.Errorf("NM caps should cover %s", k)
		}
	}
	if !c.Has("vlan") || !c.Has("bridge") || !c.Has("wireguard") {
		t.Error("NM caps should advertise vlan/bridge/wireguard")
	}
	// Router-only — must be greyed (NM doesn't do these):
	for _, f := range []caps.Feature{caps.CanZones, caps.CanDHCPServer, caps.CanNAT, caps.CanAP} {
		if c.Has(f) {
			t.Errorf("NM caps should NOT advertise %s (router-only)", f)
		}
	}
	if c.Kinds["firewall/zone"] || c.Kinds["dhcp/dhcp"] {
		t.Error("NM caps should not cover firewall/dhcp kinds")
	}
}

func TestRenderArtifacts(t *testing.T) {
	cfg := model.Config{Interfaces: []model.Interface{
		{Name: "wan", Device: "eth0", Proto: model.DHCPProto{}},
	}}
	arts, err := (&Applier{}).Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := arts["wan.nmconnection"]
	if !ok || text == "" {
		t.Fatalf("expected wan.nmconnection artifact, got %v", arts)
	}
}

// snapshot/restore is the commit-confirm undo: capture a dir, change it, restore.
func TestSnapshotRestore(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("keep.nmconnection", "[connection]\nid=keep\n")
	write("change.nmconnection", "[connection]\nid=change-v1\n")

	snap, err := snapshotDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate: modify one, add one, (the snapshot should undo both).
	write("change.nmconnection", "[connection]\nid=change-v2\n")
	write("added.nmconnection", "[connection]\nid=added\n")

	if err := restoreDir(dir, snap); err != nil {
		t.Fatal(err)
	}
	got, _ := snapshotDir(dir)
	if len(got) != 2 {
		t.Fatalf("after restore expected 2 files, got %d: %v", len(got), keys(got))
	}
	if got["change.nmconnection"] != "[connection]\nid=change-v1\n" {
		t.Errorf("change not restored to v1: %q", got["change.nmconnection"])
	}
	if _, exists := got["added.nmconnection"]; exists {
		t.Error("added file should have been removed on restore")
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
