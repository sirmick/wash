package vm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRouterBakedAndServing boots the image baked by build-vm-image-alpine.sh
// and verifies — entirely over the out-of-band ctl plane — that the bake is
// sound: the virtio-serial data port exists, the wash multicall + net-app
// symlinks are present, and the in-guest login front (wash-vmlogin) is running,
// ready to fork wash-router on a browser login (wash-vm/UNIFY.md). The router
// actually serving its catalog over the wire (which now requires a login) is
// proven by TestProxyServesVMWire; here we just prove the launcher came up,
// without depending on the browser chrome or the data-plane wire timing.
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

	// Wait for the login front to be up (wash-router-launch execs wash-vmlogin
	// once the data port appears; it owns the channel and forks wash-router only
	// after a browser logs in — so pre-login, wash-vmlogin is the process to see).
	var psOut string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if psOut = exec("pgrep -f wash-vmlogin || true"); strings.TrimSpace(psOut) != "" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if strings.TrimSpace(psOut) == "" {
		t.Fatalf("wash-vmlogin (login front) not running\nlauncher log:\n%s\nconsole:\n%s", exec("cat /run/wash-router.log 2>&1"), vm.ConsoleLog())
	}
}
