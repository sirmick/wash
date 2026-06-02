package netplan

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
	"github.com/sirmick/wash/internal/washnet/netplanprofile"
)

var _ backend.Applier = (*Applier)(nil)

type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	out   map[string]string // keyed by first arg ("apply"/"get"/"-d")
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

// runStdout shares the fake's canned output (tests provide clean stdout).
func (f *fakeRunner) runStdout(name string, args ...string) (string, error) {
	return f.run(name, args...)
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

func TestCapabilities(t *testing.T) {
	c := Capabilities()
	for _, k := range []string{"network/interface", "network/device", "wireless/wifi-iface"} {
		if !c.Kinds[k] {
			t.Errorf("netplan caps should cover %s", k)
		}
	}
	if !c.Has(caps.CanVLAN) || !c.Has(caps.CanBridge) || !c.Has(caps.CanWireGuard) {
		t.Error("netplan caps should advertise vlan/bridge/wireguard")
	}
	for _, f := range []caps.Feature{caps.CanZones, caps.CanDHCPServer, caps.CanAP} {
		if c.Has(f) {
			t.Errorf("netplan caps should NOT advertise %s (sibling-backend feature)", f)
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
	text, ok := arts[netplanprofile.FileName]
	if !ok || !strings.Contains(text, "version: 2") || !strings.Contains(text, "dhcp4: true") {
		t.Fatalf("expected %s with version 2 + dhcp4, got %v", netplanprofile.FileName, arts)
	}
}

// Apply takes over: writes wash's file, removes the foreign one, runs apply.
// Rollback restores the foreign file.
func TestApplyTakeoverAndRollback(t *testing.T) {
	run := &fakeRunner{}
	a := newTestApplier(t, run, func() bool { return false })

	// Seed a foreign netplan file the box came with.
	foreign := filepath.Join(a.dir, "50-cloud-init.yaml")
	if err := os.WriteFile(foreign, []byte("network: {version: 2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := model.Config{
		Devices:    []model.Device{{Name: "br0", Type: "bridge", Ports: []string{"eth1"}}},
		Interfaces: []model.Interface{{Name: "lan", Device: "br0", Proto: model.StaticProto{IPAddr: netip.MustParsePrefix("192.168.1.1/24")}}},
	}
	token, err := a.Apply(backend.RenderPlan{Target: cfg})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Takeover: wash's file present, foreign gone, `netplan apply` ran.
	if _, err := os.Stat(filepath.Join(a.dir, netplanprofile.FileName)); err != nil {
		t.Errorf("wash file %s not written: %v", netplanprofile.FileName, err)
	}
	if _, err := os.Stat(foreign); !os.IsNotExist(err) {
		t.Errorf("takeover should have removed the foreign file, stat err=%v", err)
	}
	if !run.saw("netplan apply") {
		t.Errorf("expected `netplan apply`, calls=%v", run.calls)
	}

	// Rollback restores the box's original /etc/netplan.
	if err := a.Rollback(token); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("rollback should restore the foreign file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.dir, netplanprofile.FileName)); !os.IsNotExist(err) {
		t.Errorf("rollback should remove wash's file (not in snapshot)")
	}
}

// Live reads the merged config via `netplan get` — the bug fix: it sees the box
// even when keyfiles render to /run.
func TestLiveReadsNetplanGet(t *testing.T) {
	run := &fakeRunner{out: map[string]string{
		"get": `network:
  version: 2
  ethernets:
    eth0:
      addresses: [10.0.0.5/24]
      routes:
        - to: default
          via: 10.0.0.1
`,
	}}
	a := newTestApplier(t, run, func() bool { return true })
	cfg := a.Live()
	if len(cfg.Interfaces) != 1 || cfg.Interfaces[0].Device != "eth0" {
		t.Fatalf("expected one eth0 interface from netplan get, got %+v", cfg.Interfaces)
	}
	sp, ok := cfg.Interfaces[0].Proto.(model.StaticProto)
	if !ok || sp.Gateway.String() != "10.0.0.1" {
		t.Fatalf("expected static proto with gateway 10.0.0.1, got %#v", cfg.Interfaces[0].Proto)
	}
}

// Verify auto-reverts when the default route the box had is lost.
func TestVerifyLockout(t *testing.T) {
	run := &fakeRunner{}
	a := newTestApplier(t, run, func() bool { return false })
	a.lastHadRoute = true // box had a route before apply
	if err := a.Verify(model.Config{}); err == nil {
		t.Fatal("expected lock-out error when the default route is gone")
	}
}
