package vm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestNMBackendReachable boots the baked image and verifies — over the
// out-of-band ctl plane — that NetworkManager came up and that wash-net's
// pure-Go nm backend (godbus → NM, no cgo) can reach it. This is the B4b gate
// (docs/NET.md §5): the type-2 backend's D-Bus path proven in a real guest,
// before any Applier touches a connection. washnet-nmprobe is baked by
// build-vm-image-alpine.sh and reads NM's version/state/connectivity/devices.
func TestNMBackendReachable(t *testing.T) {
	kernel, initramfs := artifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	vm, err := Launch(ctx, Opts{Kernel: kernel, Initramfs: initramfs})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer vm.Close()
	if err := vm.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	exec := func(cmd string) string {
		t.Helper()
		r, err := vm.Exec(ctx, cmd)
		if err != nil {
			t.Fatalf("Exec %q: %v", cmd, err)
		}
		return r.Out
	}

	// NM starts from the init script after dbus; give it a moment, then probe
	// over godbus. Retry while it's still coming up.
	var out string
	for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); {
		out = exec("washnet-nmprobe 2>&1")
		if strings.Contains(out, "OK NetworkManager") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !strings.Contains(out, "OK NetworkManager") {
		t.Fatalf("nm backend could not reach NetworkManager over godbus:\n%s\nnm.log:\n%s",
			out, exec("cat /run/nm.log 2>&1"))
	}
	t.Logf("nm backend reached NetworkManager:\n%s", strings.TrimRight(out, "\n"))
}
