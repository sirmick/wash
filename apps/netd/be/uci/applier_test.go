package uci

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
)

type fakeRunner struct{ calls [][]string }

func (f *fakeRunner) run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return "", nil
}

func (f *fakeRunner) called(name, arg string) bool {
	for _, c := range f.calls {
		if c[0] == name && len(c) > 1 && c[1] == arg {
			return true
		}
	}
	return false
}

func newTestApplier(t *testing.T, route func() bool) (*Applier, *fakeRunner, string) {
	t.Helper()
	dir := t.TempDir()
	fr := &fakeRunner{}
	a := NewApplier()
	a.dir = dir
	a.run = fr
	a.route = route
	// Fast, deterministic verify timings.
	a.settle, a.window, a.interval = 0, 50*time.Millisecond, time.Millisecond
	return a, fr, dir
}

func sampleConfig() model.Config {
	return model.Config{Interfaces: []model.Interface{
		{Name: "wan", Device: "eth0", Proto: model.DHCPProto{}},
		{Name: "lan", Device: "br-lan", Proto: model.StaticProto{IPAddr: netip.MustParsePrefix("10.0.50.1/24")}},
	}}
}

func TestCapabilitiesAreFull(t *testing.T) {
	c := Capabilities()
	// UCI is the whole-gateway backend — link + firewall + dhcp + wireless.
	for _, k := range []string{"network/interface", "firewall/zone", "dhcp/dhcp", "wireless/wifi-iface"} {
		if !c.SupportsKind(splitKind(k)) {
			t.Errorf("UCI caps missing kind %q — expected the full profile", k)
		}
	}
	for _, f := range []caps.Feature{caps.CanZones, caps.CanDHCPServer, caps.CanAP, caps.CanWireGuard} {
		if !c.Has(f) {
			t.Errorf("UCI caps missing feature %q", f)
		}
	}
}

func splitKind(k string) (string, string) {
	i := strings.IndexByte(k, '/')
	return k[:i], k[i+1:]
}

func TestApplyWritesNetworkAndRoundTrips(t *testing.T) {
	a, fr, dir := newTestApplier(t, func() bool { return true })

	if _, err := a.Apply(backend.RenderPlan{Target: sampleConfig()}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// /etc/config/network was written and carries the interfaces.
	b, err := os.ReadFile(filepath.Join(dir, "network"))
	if err != nil {
		t.Fatalf("network file not written: %v", err)
	}
	if !strings.Contains(string(b), "interface") || !strings.Contains(string(b), "eth0") {
		t.Fatalf("rendered network missing expected content:\n%s", b)
	}
	// The network service was reloaded.
	if !fr.called("/etc/init.d/network", "reload") {
		t.Errorf("network reload not invoked; calls=%v", fr.calls)
	}
	// Live() reads it back through the codec into the model.
	live := a.Live()
	names := map[string]bool{}
	for _, i := range live.Interfaces {
		names[i.Name] = true
	}
	if !names["wan"] || !names["lan"] {
		t.Fatalf("Live() did not round-trip the interfaces: %+v", live.Interfaces)
	}
}

func TestApplyOnlyTouchesRenderedPackages(t *testing.T) {
	a, _, dir := newTestApplier(t, func() bool { return true })
	// A pre-existing firewall the user hasn't edited must survive a network-only apply.
	const fw = "config defaults\n\toption input 'ACCEPT'\n"
	if err := os.WriteFile(filepath.Join(dir, "firewall"), []byte(fw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(backend.RenderPlan{Target: sampleConfig()}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "firewall"))
	if string(got) != fw {
		t.Errorf("network-only apply clobbered /etc/config/firewall:\n got %q\nwant %q", got, fw)
	}
}

func TestRollbackRestoresPriorPackages(t *testing.T) {
	a, _, dir := newTestApplier(t, func() bool { return true })
	const prior = "config interface 'wan'\n\toption proto 'static'\n"
	if err := os.WriteFile(filepath.Join(dir, "network"), []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}
	tok, err := a.Apply(backend.RenderPlan{Target: sampleConfig()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cur, _ := os.ReadFile(filepath.Join(dir, "network")); string(cur) == prior {
		t.Fatal("Apply did not overwrite network")
	}
	if err := a.Rollback(tok); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if cur, _ := os.ReadFile(filepath.Join(dir, "network")); string(cur) != prior {
		t.Errorf("Rollback did not restore network:\n got %q\nwant %q", cur, prior)
	}
}

// TestApplyRoundTripsFullGateway is the headline check: a whole-gateway config
// (link + firewall + dhcp) writes the matching /etc/config/* files and reads back
// through the codec unchanged — proving UCI round-trips the full router model,
// not just the network package.
func TestApplyRoundTripsFullGateway(t *testing.T) {
	a, _, dir := newTestApplier(t, func() bool { return true })
	cfg := model.Config{
		Interfaces: []model.Interface{
			{Name: "lan", Device: "br-lan", Proto: model.StaticProto{IPAddr: netip.MustParsePrefix("10.0.50.1/24")}},
		},
		Zones: []model.Zone{
			{Name: "lan", Networks: []string{"lan"}, Input: "ACCEPT", Output: "ACCEPT", Forward: "REJECT"},
			{Name: "wan", Networks: []string{"wan"}, Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true},
		},
		Forwardings: []model.Forwarding{{Src: "lan", Dest: "wan"}},
		Pools:       []model.DHCPPool{{Name: "lan", Interface: "lan", Start: 100, Limit: 150, LeaseTime: "12h"}},
		Hosts:       []model.Host{{Name: "printer", MAC: "aa:bb:cc:dd:ee:ff", IP: netip.MustParseAddr("10.0.50.20"), Hostname: "printer"}},
	}
	if _, err := a.Apply(backend.RenderPlan{Target: cfg}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, pkg := range []string{"network", "firewall", "dhcp"} {
		if b, err := os.ReadFile(filepath.Join(dir, pkg)); err != nil || len(b) == 0 {
			t.Fatalf("/etc/config/%s not written (err=%v, len=%d)", pkg, err, len(b))
		}
	}
	live := a.Live()
	if len(live.Zones) != 2 {
		t.Errorf("zones did not round-trip: got %d want 2 (%+v)", len(live.Zones), live.Zones)
	}
	if len(live.Forwardings) != 1 {
		t.Errorf("forwardings did not round-trip: %+v", live.Forwardings)
	}
	if len(live.Pools) != 1 || live.Pools[0].Interface != "lan" {
		t.Errorf("dhcp pool did not round-trip: %+v", live.Pools)
	}
	if len(live.Hosts) != 1 || live.Hosts[0].Hostname != "printer" {
		t.Errorf("host did not round-trip: %+v", live.Hosts)
	}
}

func TestVerifyLockoutOnLostRoute(t *testing.T) {
	hadRoute := true
	a, _, _ := newTestApplier(t, func() bool { return hadRoute })
	if _, err := a.Apply(backend.RenderPlan{Target: sampleConfig()}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Route present after apply → Verify passes.
	if err := a.Verify(model.Config{}); err != nil {
		t.Errorf("Verify with a live route should pass, got %v", err)
	}
	// Now the apply has severed the route → Verify must fail (auto-revert trigger).
	hadRoute = false
	if err := a.Verify(model.Config{}); err == nil {
		t.Error("Verify should fail when the default route is gone after apply")
	}
}
