package netd

import (
	"sync"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/model"
)

// fakeApplier is B1a's stand-in backend (docs/NET.md §11 B1a). It implements the
// backend.Applier seam without touching a kernel: it renders to UCI text, holds
// the "live" config in memory, and commits a staged target on Confirm. The real
// NetworkManager backend lands in B4; the engine and these tests are identical.
//
// The single-consumer rule ([[no premature service]]) keeps this inside netd:
// the txn package's own fake lives in its _test.go and isn't importable, and no
// second consumer exists yet. Promote to backend/fake only if B2 wants it too.
type fakeApplier struct {
	mu     sync.Mutex
	live   model.Config  // confirmed state — the base for the next diff/apply
	staged *model.Config // applied-but-unconfirmed target; nil otherwise

	// verifyErr, when set, makes Verify fail so the txn engine exercises its
	// autonomous auto-revert path (the lock-out truth test, §10). Test-only.
	verifyErr error
}

func newFakeApplier() *fakeApplier { return &fakeApplier{} }

// Capabilities reports Full so B1a gates nothing — the whole Advanced UI is
// editable offline. Real backends advertise a subset and the UI greys the rest.
func (a *fakeApplier) Capabilities() caps.Capabilities { return caps.Full() }

func (a *fakeApplier) Render(c model.Config) (backend.Artifacts, error) {
	files, err := codec.Render(c)
	if err != nil {
		return nil, err
	}
	return backend.Artifacts(files), nil
}

func (a *fakeApplier) Apply(p backend.RenderPlan) (backend.RollbackToken, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := p.Target
	a.staged = &t
	return backend.RollbackToken("netd-fake"), nil
}

func (a *fakeApplier) Verify(model.Config) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.verifyErr
}

func (a *fakeApplier) Confirm(backend.RollbackToken) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.staged != nil {
		a.live = *a.staged
		a.staged = nil
	}
	return nil
}

func (a *fakeApplier) Rollback(backend.RollbackToken) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.staged = nil
	return nil
}

// Live returns the confirmed config — the base netd diffs and applies against.
func (a *fakeApplier) Live() model.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.live
}

// setVerifyErr is a test hook to force the auto-revert path.
func (a *fakeApplier) setVerifyErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.verifyErr = err
}

// Devices: the fake presents four unconfigured links — matching the four
// virtio NICs the Alpine VM image carries — so the host-side e2e (and a
// developer poking the kiosk net app without a VM) can drive the Add wizards
// against real-looking interfaces. The real NM/networkd appliers read these
// from the live box.
func (a *fakeApplier) Devices() []string { return []string{"eth0", "eth1", "eth2", "eth3"} }
