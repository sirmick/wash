package vm

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestNMLockoutAutoRevert is the commit-confirm lock-out truth test (docs/NET.md
// §7, §10): a real apply that severs the box's own connectivity is detected by
// VERIFY and autonomously reverted, restoring access — witnessed over the
// out-of-band ctl plane. We establish internet on eth0 (DHCP via qemu user-net),
// then apply a WAN-breaking static config; the engine's VERIFY sees connectivity
// regress from full and auto-reverts to the working config.
//
// Skips if the env has no outbound connectivity (NM can never reach "full").
func TestNMLockoutAutoRevert(t *testing.T) {
	kernel, initramfs := artifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
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

	for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); {
		if strings.Contains(exec("washnet-nmprobe 2>&1"), "OK NetworkManager") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	stage := func(name, body string) {
		exec(fmt.Sprintf("mkdir -p /tmp/cc; echo %s | base64 -d > /tmp/cc/%s",
			base64.StdEncoding.EncodeToString([]byte(body)), name))
	}
	// eth0 DHCP (gets internet via qemu user-net) → the baseline.
	stage("establish.uci", "config interface 'wan'\n\toption device 'eth0'\n\toption proto 'dhcp'\n")
	// eth0 pinned to a dead TEST-NET static with no gateway → severs the default
	// route, killing connectivity. This is the lock-out the engine must undo.
	stage("break.uci", "config interface 'wan'\n\toption device 'eth0'\n\toption proto 'static'\n\toption ipaddr '192.0.2.99/24'\n")

	out := exec("washnet-cc /tmp/cc/establish.uci /tmp/cc/break.uci 2>&1")
	t.Logf("washnet-cc:\n%s", strings.TrimRight(out, "\n"))

	if strings.Contains(out, "SKIP:") {
		t.Skip("no default route in this env (no DHCP/internet) — can't run the lock-out test")
	}
	if !strings.Contains(out, "ESTABLISHED default-route=present") {
		t.Fatalf("never established a baseline default route:\n%s\nnm.log:\n%s", out, exec("tail -20 /run/nm.log"))
	}
	// VERIFY must have failed (default route lost) → engine auto-reverted.
	if !strings.Contains(out, "STATE reverted") {
		t.Fatalf("WAN-breaking apply was NOT auto-reverted (lock-out protection failed):\n%s", out)
	}
	// And the way out is restored.
	if !strings.Contains(out, "after default-route=present") {
		t.Errorf("default route not restored after the auto-revert:\n%s", out)
	}
	// The dead static must be gone from the live config (revert restored DHCP).
	if got := exec("ip -4 -o addr show eth0 2>&1"); strings.Contains(got, "192.0.2.99") {
		t.Errorf("eth0 still has the reverted-away static IP: %s", got)
	}
}
