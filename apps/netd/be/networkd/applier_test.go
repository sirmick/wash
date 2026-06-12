package networkd

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
)

// The networkd applier satisfies the backend.Applier seam.
var _ backend.Applier = (*Applier)(nil)

// fakeRunner records every exec call and returns canned output per command.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	out   map[string]string // keyed by first arg (networkctl subcommand / "ip")
	err   map[string]error
}

func (f *fakeRunner) run(name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	key := name
	if len(args) > 0 {
		key = args[0]
	}
	return f.out[key], f.err[key]
}

func (f *fakeRunner) saw(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func newTestApplier(t *testing.T, run *fakeRunner, route func() bool) *Applier {
	t.Helper()
	return &Applier{
		dir:       t.TempDir(),
		run:       run,
		route:     route,
		settle:    time.Millisecond,
		window:    20 * time.Millisecond,
		interval:  2 * time.Millisecond,
		snapshots: map[backend.RollbackToken]map[string]string{},
	}
}

func TestCapabilitiesGreyRouterFeatures(t *testing.T) {
	c := Capabilities()
	for _, k := range []string{"network/interface", "network/device", "network/wireguard_peer"} {
		if !c.Kinds[k] {
			t.Errorf("networkd caps should cover %s", k)
		}
	}
	if !c.Has(caps.CanVLAN) || !c.Has(caps.CanBridge) || !c.Has(caps.CanWireGuard) {
		t.Error("networkd caps should advertise vlan/bridge/wireguard")
	}
	// networkd is IP-layer only: no wifi, no router features.
	if c.Kinds["wireless/wifi-iface"] {
		t.Error("networkd does no wifi — wireless kinds must stay greyed")
	}
	for _, f := range []caps.Feature{caps.CanZones, caps.CanDHCPServer, caps.CanNAT, caps.CanAP} {
		if c.Has(f) {
			t.Errorf("networkd caps should NOT advertise %s (sibling-backend feature)", f)
		}
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
	text, ok := arts["30-wan.network"]
	if !ok || !strings.Contains(text, "DHCP=yes") {
		t.Fatalf("expected 30-wan.network with DHCP=yes, got %v", arts)
	}
}

// Apply writes the units, reloads, and reconfigures each affected link.
func TestApplyWritesUnitsAndReloads(t *testing.T) {
	run := &fakeRunner{}
	a := newTestApplier(t, run, func() bool { return false })

	cfg := model.Config{
		Devices:    []model.Device{{Name: "br0", Type: "bridge", Ports: []string{"eth1", "eth2"}}},
		Interfaces: []model.Interface{{Name: "lan", Device: "br0", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("192.168.1.1/24")}}}},
	}
	token, err := a.Apply(backend.RenderPlan{Target: cfg})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if token == "" {
		t.Fatal("apply returned empty token")
	}

	// units written to disk
	got, _ := readUnits(a.dir)
	for _, fn := range []string{"20-br0.netdev", "30-lan.network", "30-eth1.network", "30-eth2.network"} {
		if _, ok := got[fn]; !ok {
			t.Errorf("expected %s written, got %v", fn, keys(got))
		}
	}
	// reload + reconfigure of each touched link
	if !run.saw("networkctl reload") {
		t.Error("expected a networkctl reload")
	}
	for _, link := range []string{"br0", "eth1", "eth2"} {
		if !run.saw("networkctl reconfigure " + link) {
			t.Errorf("expected reconfigure of %s", link)
		}
	}
}

// Verify fails (→ engine auto-reverts) when an apply severs the box's default
// route; passes when the route survives.
func TestVerifyLockoutOnLostRoute(t *testing.T) {
	run := &fakeRunner{}
	// Had a route at apply time; it's gone now → lock-out.
	a := newTestApplier(t, run, func() bool { return false })
	a.lastHadRoute = true
	if err := a.Verify(model.Config{}); err == nil {
		t.Error("expected Verify to fail when the default route was lost")
	}

	// No route before the apply ⇒ nothing to lose ⇒ a disconnected box is valid.
	a2 := newTestApplier(t, run, func() bool { return false })
	a2.lastHadRoute = false
	if err := a2.Verify(model.Config{}); err != nil {
		t.Errorf("expected Verify to pass when there was no route to lose: %v", err)
	}
}

// Verify surfaces a dead networkd regardless of routing.
func TestVerifyNetworkdUnreachable(t *testing.T) {
	run := &fakeRunner{err: map[string]error{"status": os.ErrDeadlineExceeded}}
	a := newTestApplier(t, run, func() bool { return true })
	if err := a.Verify(model.Config{}); err == nil {
		t.Error("expected Verify to fail when networkctl status errors")
	}
}

// Apply → Rollback restores the exact prior unit set and re-reloads.
func TestApplyRollbackRestores(t *testing.T) {
	run := &fakeRunner{}
	a := newTestApplier(t, run, func() bool { return false })

	// Seed a pre-existing unit so Rollback has something to restore to.
	seed := filepath.Join(a.dir, "30-old.network")
	if err := os.WriteFile(seed, []byte("[Match]\nName=eth0\n\n[Network]\nDHCP=yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := model.Config{Interfaces: []model.Interface{
		{Name: "wan", Device: "eth0", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("10.0.0.2/24")}}},
	}}
	token, err := a.Apply(backend.RenderPlan{Target: cfg})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	// The new unit replaced the seed (wan matches eth0 → 30-wan.network), old removed.
	got, _ := readUnits(a.dir)
	if _, ok := got["30-old.network"]; ok {
		t.Error("expected the pre-existing 30-old.network to be removed by Apply")
	}

	if err := a.Rollback(token); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	got, _ = readUnits(a.dir)
	if len(got) != 1 || got["30-old.network"] == "" {
		t.Fatalf("rollback should restore exactly the seed, got %v", keys(got))
	}
	if _, ok := got["30-wan.network"]; ok {
		t.Error("rollback should have removed the applied 30-wan.network")
	}
}

func TestDevicesFiltersVirtual(t *testing.T) {
	run := &fakeRunner{out: map[string]string{"-d": `[
		{"ifname":"lo","link_type":"loopback"},
		{"ifname":"eth0","link_type":"ether"},
		{"ifname":"eth1","link_type":"ether"},
		{"ifname":"br0","link_type":"ether","linkinfo":{"info_kind":"bridge"}},
		{"ifname":"eth0.10","link_type":"ether","linkinfo":{"info_kind":"vlan"}}
	]`}}
	a := newTestApplier(t, run, func() bool { return false })
	got := a.Devices()
	want := []string{"eth0", "eth1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Devices() = %v, want %v (physical NICs only)", got, want)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
