package ifupdown

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

var _ backend.Applier = (*Applier)(nil)

type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	out   map[string]string
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
		path:      filepath.Join(t.TempDir(), "interfaces"),
		run:       run,
		route:     route,
		settle:    time.Millisecond,
		window:    20 * time.Millisecond,
		interval:  2 * time.Millisecond,
		snapshots: map[backend.RollbackToken]string{},
	}
}

func TestCapabilities(t *testing.T) {
	c := Capabilities()
	if !c.Kinds["network/interface"] || !c.Kinds["network/device"] {
		t.Error("ifupdown caps should cover interface + device")
	}
	if !c.Has(caps.CanBridge) || !c.Has(caps.CanVLAN) {
		t.Error("ifupdown caps should advertise bridge + vlan")
	}
	for _, f := range []caps.Feature{caps.CanWireGuard, caps.CanZones, caps.CanAP, caps.CanDHCPServer} {
		if c.Has(f) {
			t.Errorf("ifupdown caps should NOT advertise %s", f)
		}
	}
}

func TestApplyTakeoverAndRollback(t *testing.T) {
	run := &fakeRunner{}
	a := newTestApplier(t, run, func() bool { return false })

	// Seed the box's original interfaces file.
	original := "auto lo\niface lo inet loopback\n"
	if err := os.WriteFile(a.path, []byte(original), 0o644); err != nil {
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
	got, _ := os.ReadFile(a.path)
	if !strings.Contains(string(got), "bridge_ports eth1") {
		t.Errorf("apply should have written the bridge config, got:\n%s", got)
	}
	if !run.saw("ifup -a") {
		t.Errorf("expected `ifup -a`, calls=%v", run.calls)
	}

	if err := a.Rollback(token); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	got, _ = os.ReadFile(a.path)
	if string(got) != original {
		t.Errorf("rollback should restore the original file, got:\n%s", got)
	}
}

func TestLiveParsesInterfaces(t *testing.T) {
	a := newTestApplier(t, &fakeRunner{}, func() bool { return true })
	os.WriteFile(a.path, []byte("auto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet static\n    address 10.0.0.5/24\n    gateway 10.0.0.1\n"), 0o644)
	cfg := a.Live()
	if len(cfg.Interfaces) != 1 || cfg.Interfaces[0].Device != "eth0" {
		t.Fatalf("expected one eth0 interface (lo ignored), got %+v", cfg.Interfaces)
	}
	sp, ok := cfg.Interfaces[0].Proto.(model.StaticProto)
	if !ok || sp.Gateway.String() != "10.0.0.1" {
		t.Fatalf("expected static with gateway 10.0.0.1, got %#v", cfg.Interfaces[0].Proto)
	}
}

func TestVerifyLockout(t *testing.T) {
	a := newTestApplier(t, &fakeRunner{}, func() bool { return false })
	a.lastHadRoute = true
	if err := a.Verify(model.Config{}); err == nil {
		t.Fatal("expected lock-out error when the default route is gone")
	}
}
