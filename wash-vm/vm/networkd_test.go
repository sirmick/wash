package vm

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fedoraArtifacts resolves the Fedora / systemd-networkd image, skipping when it
// or the harness prereqs are absent (so plain `go test ./...` stays green).
func fedoraArtifacts(t *testing.T) (kernel, initramfs string) {
	t.Helper()
	wd, _ := os.Getwd()
	root := filepath.Join(wd, "..", "..")
	kernel = filepath.Join(root, "out", "vm", "vmlinuz-fedora")
	initramfs = filepath.Join(root, "out", "vm", "initramfs-fedora.gz")
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("qemu-system-x86_64 not found")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("/dev/kvm not available")
	}
	for _, p := range []string{kernel, initramfs} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("fedora image missing: %s (run scripts/build-vm-image-fedora.sh)", p)
		}
	}
	return kernel, initramfs
}

// launchFedora boots the Fedora image with the settings it needs: more RAM (the
// rootfs runs from the initramfs in RAM) and net.ifnames=0 (belt-and-suspenders
// with the image's masked net-naming udev rule, which is what actually keeps the
// NICs as eth0..eth3 — matching the harness NICs + the wash model).
func launchFedora(ctx context.Context, t *testing.T) *VM {
	t.Helper()
	kernel, initramfs := fedoraArtifacts(t)
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

// TestFedoraNetworkdBoot is the Phase-C analogue of TestNMBackendReachable: it
// boots the Fedora image and verifies, over the out-of-band ctl plane, that
// systemd-networkd is the active link manager (the own-it posture) and that
// NetworkManager is absent — so wash's autodetect (WASH_NETD_BACKEND=auto)
// resolves to the networkd backend.
func TestFedoraNetworkdBoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	vm := launchFedora(ctx, t)
	defer vm.Close()

	run := func(cmd string) string {
		t.Helper()
		r, err := vm.Exec(ctx, cmd)
		if err != nil {
			t.Fatalf("Exec %q: %v", cmd, err)
		}
		return r.Out
	}

	// Fedora's full boot (sysinit → udev → sockets → dbus → networkd) is much
	// slower than Alpine's; the agent answers early (DefaultDependencies=no), so
	// wait for the system to finish initializing before asserting on networkd.
	var sysState string
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		sysState = strings.TrimSpace(run("systemctl is-system-running 2>&1"))
		if sysState == "running" || sysState == "degraded" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("system state: %s", sysState)

	if out := run("systemctl is-active systemd-networkd 2>&1"); !strings.Contains(out, "active") {
		t.Fatalf("systemd-networkd not active: %q\njournal:\n%s", out, run("journalctl -u systemd-networkd --no-pager 2>&1 | tail -20"))
	}
	if out := run("command -v NetworkManager nmcli 2>/dev/null || echo absent"); !strings.Contains(out, "absent") {
		t.Fatalf("expected NO NetworkManager (own-it posture), got: %q", out)
	}
	links := run("networkctl --no-legend 2>&1")
	if !strings.Contains(links, "eth0") {
		t.Fatalf("networkctl doesn't list eth0 (predictable-naming not suppressed?):\n%s\n=== ip -br link ===\n%s",
			links, run("ip -br link 2>&1"))
	}
	// networkd should be DRIVING eth0 via the baseline DHCP .network — it gets a
	// qemu user-net lease (10.0.2.x) + a default route. Poll: DHCP settles a beat
	// after the link comes up.
	var route string
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		route = run("ip route 2>&1")
		if strings.Contains(route, "default") {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !strings.Contains(route, "default") {
		t.Fatalf("eth0 didn't get a default route from networkd DHCP:\nroutes:\n%s\nnetworkctl:\n%s\nstatus:\n%s",
			route, run("networkctl --no-legend 2>&1"), run("networkctl status eth0 2>&1 | head -25"))
	}
	t.Logf("networkd active, NM absent → autodetect picks networkd; eth0 managed with a default route.\nlinks:\n%s\nroutes:\n%s",
		strings.TrimRight(links, "\n"), strings.TrimRight(route, "\n"))
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// applyScenarioNetworkd is the same shape as the NM apply test: bridge eth1+eth2
// into br0 with a static IP, and a VLAN on eth3 — proving wash renders + applies
// real bridging/VLAN/addressing through systemd-networkd.
const applyScenarioNetworkd = `
config interface 'lan'
	option device 'br0'
	option proto 'static'
	option ipaddr '10.0.50.1/24'

config device
	option name 'br0'
	option type 'bridge'
	list ports 'eth1'
	list ports 'eth2'

config interface 'lab'
	option device 'eth3.20'
	option proto 'static'
	option ipaddr '10.0.20.1/24'

config device
	option name 'eth3.20'
	option type '8021q'
	option ifname 'eth3'
	option vid '20'
`

// TestFedoraNetworkdApply is the Phase-C apply gate (the networkd analogue of
// TestNMApplyBridgeAndVLAN): wash renders the UCI scenario to networkd units and
// applies it via the real networkd Applier (washnet-apply WASH_NETD_BACKEND=
// networkd → write /etc/systemd/network + networkctl reload/reconfigure), and we
// assert the kernel actually built the bridge + VLAN over the out-of-band ctl
// plane.
func TestFedoraNetworkdApply(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	vm := launchFedora(ctx, t)
	defer vm.Close()

	run := func(cmd string) string {
		t.Helper()
		r, err := vm.Exec(ctx, cmd)
		if err != nil {
			t.Fatalf("Exec %q: %v", cmd, err)
		}
		return r.Out
	}
	waitFor := func(what string, d time.Duration, ok func(string) bool, cmd string) string {
		t.Helper()
		var out string
		for deadline := time.Now().Add(d); time.Now().Before(deadline); {
			out = run(cmd)
			if ok(out) {
				return out
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s: condition not met within %s\nlast: %q\nnetworkctl:\n%s", what, d, out, run("networkctl --no-legend 2>&1"))
		return out
	}
	has := func(sub string) func(string) bool { return func(s string) bool { return strings.Contains(s, sub) } }

	// Wait for the system to finish initializing (running/degraded) before
	// applying — networkctl needs the system D-Bus bus, which comes up late in
	// Fedora's boot (is-active=active alone returns before dbus is ready).
	waitFor("system initialized", 90*time.Second,
		func(s string) bool { return strings.Contains(s, "running") || strings.Contains(s, "degraded") },
		"systemctl is-system-running 2>&1")

	// Stage the scenario UCI (base64 to dodge shell quoting) + apply via networkd.
	b64 := base64.StdEncoding.EncodeToString([]byte(applyScenarioNetworkd))
	run(fmt.Sprintf("mkdir -p /tmp/wa; echo %s | base64 -d > /tmp/wa/network.uci", b64))
	if out := run("WASH_NETD_BACKEND=networkd washnet-apply /tmp/wa/network.uci 2>&1"); !strings.Contains(out, "APPLIED") {
		t.Fatalf("washnet-apply (networkd) did not report success: %q\nunits:\n%s", out, run("ls -la /etc/systemd/network 2>&1"))
	}

	// br0 exists, carries the static IP, and enslaves eth1 + eth2.
	waitFor("br0 has IP", 25*time.Second, has("10.0.50.1"), "ip -4 -o addr show br0 2>&1")
	members := waitFor("bridge members", 15*time.Second,
		func(s string) bool { return strings.Contains(s, "eth1") && strings.Contains(s, "eth2") },
		"ls /sys/class/net/br0/brif/ 2>&1")
	t.Logf("bridge members (br0/brif): %s", strings.Fields(members))

	// VLAN eth3.20 exists with its static IP and 802.1q parent eth3.
	waitFor("vlan has IP", 25*time.Second, has("10.0.20.1"), "ip -4 -o addr show eth3.20 2>&1")
	vlan := waitFor("vlan parented on eth3", 10*time.Second, has("eth3.20@eth3"),
		"ip -o link show eth3.20 2>&1")
	t.Logf("vlan link: %s", strings.TrimRight(vlan, "\n"))
	t.Logf("networkctl after apply:\n%s", run("networkctl --no-legend 2>&1"))
}
