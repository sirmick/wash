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

func debianArtifacts(t *testing.T) (kernel, initramfs string) {
	t.Helper()
	wd, _ := os.Getwd()
	root := filepath.Join(wd, "..", "..")
	kernel = filepath.Join(root, "out", "vm", "vmlinuz-debian")
	initramfs = filepath.Join(root, "out", "vm", "initramfs-debian.gz")
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("qemu-system-x86_64 not found")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("/dev/kvm not available")
	}
	for _, p := range []string{kernel, initramfs} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("debian image missing: %s (run scripts/build-vm-image-debian.sh)", p)
		}
	}
	return kernel, initramfs
}

func launchDebian(ctx context.Context, t *testing.T) *VM {
	t.Helper()
	kernel, initramfs := debianArtifacts(t)
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

// TestDebianIfupdownRead boots the Debian image and proves the ifupdown backend
// Live path: wash parses the baked /etc/network/interfaces into the model. The
// baked config is a static br-lan bridge over eth1/eth2 (+ eth0 dhcp), so a
// correct read must reconstruct the bridge, its members, and the address.
func TestDebianIfupdownRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	vm := launchDebian(ctx, t)
	defer vm.Close()

	run := func(cmd string) string {
		t.Helper()
		r, err := vm.Exec(ctx, cmd)
		if err != nil {
			t.Fatalf("Exec %q: %v", cmd, err)
		}
		return r.Out
	}

	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		s := strings.TrimSpace(run("systemctl is-system-running 2>&1"))
		if s == "running" || s == "degraded" {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// ifupdown is the manager; netplan + NetworkManager absent → autodetect picks
	// ifupdown.
	if out := run("command -v ifup 2>/dev/null || echo absent"); strings.Contains(out, "absent") {
		t.Fatalf("ifup binary missing — image build broken")
	}
	if out := run("command -v netplan NetworkManager nmcli 2>/dev/null || echo absent"); !strings.Contains(out, "absent") {
		t.Fatalf("expected NO netplan/NetworkManager (ifupdown posture), got: %q", out)
	}

	// THE READ: wash's ifupdown backend parses /etc/network/interfaces into the
	// model and must reconstruct the bridge.
	uci := run("WASH_NETD_BACKEND=ifupdown washnet-read 2>&1")
	t.Logf("wash ifupdown read:\n%s", uci)
	for _, want := range []string{
		"option type 'bridge'",
		"list ports 'eth1'",
		"list ports 'eth2'",
		"list ipaddr '10.0.50.1/24'",
	} {
		if !strings.Contains(uci, want) {
			t.Fatalf("wash ifupdown read missing %q — the backend didn't reconstruct the box:\n%s", want, uci)
		}
	}
}

// TestDebianIfupdownApply is the full CLI loop at OS level: read the box's UCI,
// CHANGE the bridge address, apply it through the ifupdown backend, and assert
// the kernel carries the new address — read → edit → apply landing in the live
// stack via /etc/network/interfaces + ifup.
func TestDebianIfupdownApply(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	vm := launchDebian(ctx, t)
	defer vm.Close()

	run := func(cmd string) string {
		t.Helper()
		r, err := vm.Exec(ctx, cmd)
		if err != nil {
			t.Fatalf("Exec %q: %v", cmd, err)
		}
		return r.Out
	}
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		s := strings.TrimSpace(run("systemctl is-system-running 2>&1"))
		if s == "running" || s == "degraded" {
			break
		}
		time.Sleep(2 * time.Second)
	}

	run("WASH_NETD_BACKEND=ifupdown washnet-read > /tmp/net.uci")
	run("sed -i 's#10.0.50.1/24#10.9.9.1/24#' /tmp/net.uci")
	out := run("WASH_NETD_BACKEND=ifupdown washnet-apply /tmp/net.uci 2>&1")
	t.Logf("washnet-apply: %s", strings.TrimSpace(out))
	if !strings.Contains(out, "APPLIED") {
		t.Fatalf("washnet-apply did not report APPLIED:\n%s\n--- /tmp/net.uci ---\n%s\n--- /etc/network/interfaces ---\n%s", out, run("cat /tmp/net.uci"), run("cat /etc/network/interfaces"))
	}

	var addr string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		addr = run("ip -4 addr show br-lan 2>&1")
		if strings.Contains(addr, "10.9.9.1") {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !strings.Contains(addr, "10.9.9.1") {
		t.Fatalf("br-lan did not get the applied address 10.9.9.1 at OS level:\nip addr:\n%s\ninterfaces:\n%s", addr, run("cat /etc/network/interfaces"))
	}
	t.Logf("OS-level applied: br-lan carries 10.9.9.1\n%s", strings.TrimSpace(addr))
}
