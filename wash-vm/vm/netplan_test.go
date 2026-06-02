package vm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ubuntuArtifacts resolves the Ubuntu / netplan image, skipping when it or the
// harness prereqs are absent (so plain `go test ./...` stays green).
func ubuntuArtifacts(t *testing.T) (kernel, initramfs string) {
	t.Helper()
	wd, _ := os.Getwd()
	root := filepath.Join(wd, "..", "..")
	kernel = filepath.Join(root, "out", "vm", "vmlinuz-ubuntu")
	initramfs = filepath.Join(root, "out", "vm", "initramfs-ubuntu.gz")
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("qemu-system-x86_64 not found")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("/dev/kvm not available")
	}
	for _, p := range []string{kernel, initramfs} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("ubuntu image missing: %s (run scripts/build-vm-image-ubuntu.sh)", p)
		}
	}
	return kernel, initramfs
}

func launchUbuntu(ctx context.Context, t *testing.T) *VM {
	t.Helper()
	kernel, initramfs := ubuntuArtifacts(t)
	vm, err := Launch(ctx, Opts{
		Kernel:     kernel,
		Initramfs:  initramfs,
		Mem:        "2048M",
		KernelArgs: "console=ttyS0 panic=-1 net.ifnames=0",
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err := vm.WaitReady(ctx); err != nil {
		out := vm.ConsoleLog()
		vm.Close()
		t.Fatalf("WaitReady: %v\nconsole tail:\n%s", err, lastLines(out, 30))
	}
	return vm
}

// TestUbuntuNetplanRead boots the Ubuntu image and proves the netplan backend's
// Live path: wash reads the baked /etc/netplan config via `netplan get` and
// decompiles it into the model — the fix for the empty-read bug. The baked
// config is a non-trivial shape (a static br-lan bridge over eth1/eth2 + dns),
// so a correct read must reconstruct the bridge, its members, and the address.
func TestUbuntuNetplanRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	vm := launchUbuntu(ctx, t)
	defer vm.Close()

	run := func(cmd string) string {
		t.Helper()
		r, err := vm.Exec(ctx, cmd)
		if err != nil {
			t.Fatalf("Exec %q: %v", cmd, err)
		}
		return r.Out
	}

	// Let the system finish initializing (the netplan generator runs at boot).
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		s := strings.TrimSpace(run("systemctl is-system-running 2>&1"))
		if s == "running" || s == "degraded" {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// netplan is the manager; NetworkManager is absent → autodetect picks netplan.
	if out := run("command -v netplan 2>/dev/null || echo absent"); strings.Contains(out, "absent") {
		t.Fatalf("netplan binary missing — image build broken")
	}
	if out := run("command -v NetworkManager nmcli 2>/dev/null || echo absent"); !strings.Contains(out, "absent") {
		t.Fatalf("expected NO NetworkManager (netplan posture), got: %q", out)
	}
	if out := run("ls /etc/netplan/ 2>&1"); !strings.Contains(out, "00-wash-default.yaml") {
		t.Fatalf("baked netplan config missing: %q", out)
	}

	// `netplan get` must answer (the Live source).
	if out := run("netplan get 2>&1"); !strings.Contains(out, "br-lan") {
		t.Fatalf("netplan get doesn't show br-lan:\n%s", out)
	}

	// THE FIX: wash's netplan backend reads the box. washnet-read with the netplan
	// backend runs netd's exact Live() path (`netplan get` → netplanprofile.Parse →
	// codec.Render) and must reproduce the bridge in the model.
	uci := run("WASH_NETD_BACKEND=netplan washnet-read 2>&1")
	t.Logf("wash netplan read:\n%s", uci)
	for _, want := range []string{
		"config device", // the bridge device section
		"option type 'bridge'",
		"list ports 'eth1'",
		"list ports 'eth2'",
		"option ipaddr '10.0.50.1/24'",
	} {
		if !strings.Contains(uci, want) {
			t.Fatalf("wash netplan read missing %q — the backend didn't reconstruct the box:\n%s", want, uci)
		}
	}
}
