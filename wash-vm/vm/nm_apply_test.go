package vm

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The B4c apply gate (docs/NET.md §5): the type-2 backend configures a real
// box's NICs and we verify the kernel state over the out-of-band ctl plane. The
// VM has eth0..eth3; we bridge eth1+eth2 into br0 and put a VLAN on eth3, then
// assert NM/the kernel actually built them.
const applyScenario = `
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

func TestNMApplyBridgeAndVLAN(t *testing.T) {
	kernel, initramfs := artifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	// waitFor polls an assertion over the ctl plane until it holds or times out.
	waitFor := func(what string, d time.Duration, ok func(string) bool, cmd string) string {
		t.Helper()
		var out string
		for deadline := time.Now().Add(d); time.Now().Before(deadline); {
			out = exec(cmd)
			if ok(out) {
				return out
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("%s: condition not met within %s\nlast: %q\nnm.log:\n%s", what, d, out, exec("tail -25 /run/nm.log"))
		return out
	}
	has := func(sub string) func(string) bool { return func(s string) bool { return strings.Contains(s, sub) } }

	// NM must be up and managing the eths before we apply.
	waitFor("NM connected", 30*time.Second, has("OK NetworkManager"), "washnet-nmprobe 2>&1")
	// no-auto-default leaves the NICs managed but disconnected (no profile yet) —
	// we just need them out of the "unmanaged" state before applying.
	waitFor("eth1 managed", 20*time.Second,
		func(s string) bool {
			return strings.Contains(s, "eth1:ethernet:") && !strings.Contains(s, "eth1:ethernet:unmanaged")
		},
		"nmcli -t -f DEVICE,TYPE,STATE device 2>&1")

	// Stage the scenario UCI in the guest (base64 to dodge shell quoting) + apply.
	// The file must be named <package>.uci — washnet-apply keys by filename stem.
	b64 := base64.StdEncoding.EncodeToString([]byte(applyScenario))
	exec(fmt.Sprintf("mkdir -p /tmp/wa; echo %s | base64 -d > /tmp/wa/network.uci", b64))
	if out := exec("washnet-apply /tmp/wa/network.uci 2>&1"); !strings.Contains(out, "APPLIED") {
		t.Fatalf("washnet-apply did not report success: %q\nnm.log:\n%s", out, exec("tail -25 /run/nm.log"))
	}

	// Bridge br0 exists, carries the static IP, and enslaves eth1 + eth2. (Assert
	// with /sys, not `ip show master` — busybox's ip doesn't support that.)
	waitFor("br0 up", 20*time.Second, has("10.0.50.1"), "ip -4 -o addr show br0 2>&1")
	members := waitFor("bridge members", 15*time.Second,
		func(s string) bool { return strings.Contains(s, "eth1") && strings.Contains(s, "eth2") },
		"ls /sys/class/net/br0/brif/ 2>&1")
	t.Logf("bridge members (br0/brif): %s", strings.Fields(members))

	// VLAN eth3.20 exists with its static IP and 802.1q parent eth3.
	waitFor("vlan up", 20*time.Second, has("10.0.20.1"), "ip -4 -o addr show eth3.20 2>&1")
	vlan := waitFor("vlan parented on eth3", 10*time.Second, has("eth3.20@eth3"),
		"ip -o link show eth3.20 2>&1")
	t.Logf("vlan link: %s", strings.TrimRight(vlan, "\n"))

	t.Logf("active connections:\n%s", exec("nmcli -t -f NAME,TYPE,DEVICE connection show --active 2>&1"))

	// Read-back: the backend loads the box's current NM state into the model
	// (the UI's "load current settings" path) and we get our bridge + vlan back.
	read := exec("washnet-read 2>&1")
	t.Logf("washnet-read (NM → model → UCI):\n%s", read)
	for _, want := range []string{
		"option type 'bridge'", "list ports 'eth1'", "list ports 'eth2'",
		"option type '8021q'", "option ifname 'eth3'", "option vid '20'",
	} {
		if !strings.Contains(read, want) {
			t.Errorf("read-back of current NM state missing %q", want)
		}
	}
}
