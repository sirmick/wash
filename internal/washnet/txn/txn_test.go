package txn

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
)

// fakeApplier is an in-memory Applier: Apply snapshots prior "live" state so
// Rollback can restore it, exactly as a real backend must.
type fakeApplier struct {
	caps                caps.Capabilities
	live, prev          model.Config
	verifyErr           error
	applyErr, renderErr error
	applyCalls          int
	confirmed, reverted bool
}

func (f *fakeApplier) Capabilities() caps.Capabilities { return f.caps }
func (f *fakeApplier) Render(model.Config) (backend.Artifacts, error) {
	return nil, f.renderErr
}
func (f *fakeApplier) Apply(p backend.RenderPlan) (backend.RollbackToken, error) {
	f.applyCalls++
	if f.applyErr != nil {
		return "", f.applyErr
	}
	f.prev = f.live
	f.live = p.Target
	return "tok-1", nil
}
func (f *fakeApplier) Verify(model.Config) error { return f.verifyErr }
func (f *fakeApplier) Confirm(backend.RollbackToken) error {
	f.confirmed = true
	return nil
}
func (f *fakeApplier) Rollback(backend.RollbackToken) error {
	f.reverted = true
	f.live = f.prev
	return nil
}

func validConfig() model.Config {
	return model.Config{
		Interfaces: []model.Interface{
			{Name: "lan", Device: "br-lan", Proto: model.StaticProto{IPAddr: netip.MustParsePrefix("192.168.1.1/24")}},
			{Name: "wan", Device: "eth0", Proto: model.DHCPProto{}},
		},
		Zones: []model.Zone{
			{Name: "lan", Networks: []string{"lan"}, Input: "ACCEPT", Output: "ACCEPT", Forward: "ACCEPT"},
			{Name: "wan", Networks: []string{"wan"}, Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true},
		},
		Forwardings: []model.Forwarding{{Src: "lan", Dest: "wan"}},
	}
}

func phases(j *Job) []string {
	var ps []string
	for _, e := range j.Events() {
		ps = append(ps, e.Phase)
	}
	return ps
}

func TestCommitConfirmHappyPath(t *testing.T) {
	f := &fakeApplier{caps: caps.Full()}
	target := validConfig()
	j, err := Apply(model.Config{}, target, f)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if j.State() != AwaitConfirm {
		t.Fatalf("state = %q, want await-confirm", j.State())
	}
	if !reflect.DeepEqual(f.live, target) {
		t.Fatal("live state should equal target after apply")
	}
	if err := j.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if j.State() != Committed || !f.confirmed {
		t.Fatalf("after confirm: state=%q confirmed=%v", j.State(), f.confirmed)
	}
	want := []string{"plan", "render", "apply", "verify", "await-confirm", "committed"}
	if got := phases(j); !reflect.DeepEqual(got, want) {
		t.Fatalf("phases = %v, want %v", got, want)
	}
}

func TestVerifyFailAutoReverts(t *testing.T) {
	base := validConfig()
	f := &fakeApplier{caps: caps.Full(), live: base, verifyErr: errors.New("no upstream")}
	target := model.Config{Interfaces: []model.Interface{{Name: "lan", Proto: model.DHCPProto{}}}}
	j, err := Apply(base, target, f)
	if err != nil {
		t.Fatalf("Apply returned error (should self-revert, not error): %v", err)
	}
	if j.State() != Reverted {
		t.Fatalf("state = %q, want reverted", j.State())
	}
	if !f.reverted || !reflect.DeepEqual(f.live, base) {
		t.Fatal("failed verify must roll back to prior live state")
	}
	last := phases(j)[len(phases(j))-1]
	if last != "reverted" {
		t.Fatalf("last phase = %q, want reverted", last)
	}
}

func TestConfirmTimeoutReverts(t *testing.T) {
	base := validConfig()
	f := &fakeApplier{caps: caps.Full(), live: base}
	j, _ := Apply(base, model.Config{Interfaces: []model.Interface{{Name: "x", Proto: model.DHCPProto{}}}}, f)
	if j.State() != AwaitConfirm {
		t.Fatalf("state = %q", j.State())
	}
	// Simulate the auto-revert firing (no confirmation).
	if err := j.Revert(); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if j.State() != Reverted || !reflect.DeepEqual(f.live, base) {
		t.Fatal("revert must restore prior live state")
	}
}

func TestValidationGateBlocksApply(t *testing.T) {
	f := &fakeApplier{caps: caps.Full()}
	bad := model.Config{Zones: []model.Zone{{Name: "z", Input: "ALLOW"}}} // ALLOW is invalid
	j, err := Apply(model.Config{}, bad, f)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if j.State() != Failed {
		t.Fatalf("state = %q, want failed", j.State())
	}
	if f.applyCalls != 0 {
		t.Fatal("Apply must not touch the backend when validation fails")
	}
}

func TestCapabilityGateBlocksApply(t *testing.T) {
	// NM-like profile without CanAP: an AP SSID must be refused before apply.
	nm := caps.New([]string{"wireless/wifi-iface"})
	f := &fakeApplier{caps: nm}
	cfg := model.Config{SSIDs: []model.WifiIface{{Name: "ap", Mode: "ap", SSID: "x", Encryption: model.EncNone{}}}}
	j, err := Apply(model.Config{}, cfg, f)
	if err == nil || j.State() != Failed || f.applyCalls != 0 {
		t.Fatalf("AP under NM caps must be refused; err=%v state=%q calls=%d", err, j.State(), f.applyCalls)
	}
}

func TestArmConfirmTimerReverts(t *testing.T) {
	base := validConfig()
	f := &fakeApplier{caps: caps.Full(), live: base}
	j, _ := Apply(base, model.Config{Interfaces: []model.Interface{{Name: "x", Proto: model.DHCPProto{}}}}, f)
	j.ArmConfirmTimer(10 * time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for j.State() == AwaitConfirm && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if j.State() != Reverted {
		t.Fatalf("timer should auto-revert; state = %q", j.State())
	}
	if !reflect.DeepEqual(f.live, base) {
		t.Fatal("auto-revert must restore prior live state")
	}
}

func TestArmConfirmTimerCancelledByConfirm(t *testing.T) {
	f := &fakeApplier{caps: caps.Full()}
	j, _ := Apply(model.Config{}, validConfig(), f)
	j.ArmConfirmTimer(50 * time.Millisecond)
	if err := j.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	time.Sleep(120 * time.Millisecond) // let any stale timer fire
	if j.State() != Committed {
		t.Fatalf("confirm must win over the timer; state = %q", j.State())
	}
	if f.reverted {
		t.Fatal("must not roll back after a confirm")
	}
}
