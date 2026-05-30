package vm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRouterBakedAndServing boots the image baked by build-vm-image-alpine.sh
// and verifies — entirely over the out-of-band ctl plane — that the real
// in-guest wash-router came up, found the virtio-serial data port, and
// discovered the wash-net apps (docs/NET.md §8.3, B1e). This proves the bake
// without depending on the browser chrome (B1e-2) or the data-plane wire
// timing: the router serving the wire to a browser is the next rung's gate.
func TestRouterBakedAndServing(t *testing.T) {
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

	// The raw virtio-serial data port exists (the wash.data port → vport0p1).
	if out := exec("test -e /dev/vport0p1 && echo FOUND || echo MISSING"); !strings.Contains(out, "FOUND") {
		t.Fatalf("data port /dev/vport0p1 missing\nconsole:\n%s", vm.ConsoleLog())
	}

	// The apps dir holds the baked binary + the net app symlinks.
	if out := exec("ls /usr/lib/wash"); !strings.Contains(out, "wash-net") || !strings.Contains(out, "wash-netd") {
		t.Fatalf("apps dir missing net apps: %q", out)
	}

	// Wait for the router process to be up (it starts in the background after
	// the data port appears).
	var psOut string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if psOut = exec("pgrep -f wash-router || true"); strings.TrimSpace(psOut) != "" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if strings.TrimSpace(psOut) == "" {
		t.Fatalf("wash-router not running\nrouter log:\n%s\nconsole:\n%s", exec("cat /run/wash-router.log 2>&1"), vm.ConsoleLog())
	}

	// The router log should show the app scan finding the net apps (the
	// catalog the served shell would render). Give the scan a moment.
	var log string
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		log = exec("cat /run/wash-router.log 2>&1")
		if strings.Contains(log, "com.wash.net") {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("router log:\n%s", log)
	if !strings.Contains(log, "com.wash.net") {
		t.Fatalf("router log never mentioned com.wash.net (app scan?):\n%s", log)
	}
}
