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

// artifacts resolves the kernel + initramfs (env override, else out/vm), and
// skips the test when the harness prerequisites (qemu, /dev/kvm, built image)
// are absent — so plain `go test ./...` stays green on machines without them.
func artifacts(t *testing.T) (kernel, initramfs string) {
	t.Helper()
	kernel = os.Getenv("WASHVM_KERNEL")
	initramfs = os.Getenv("WASHVM_INITRAMFS")
	if kernel == "" || initramfs == "" {
		wd, _ := os.Getwd()
		root := filepath.Join(wd, "..", "..") // wash-vm/vm -> repo root
		kernel = filepath.Join(root, "out", "vm", "vmlinuz")
		initramfs = filepath.Join(root, "out", "vm", "initramfs.gz")
	}
	if _, err := exec.LookPath("qemu-system-x86_64"); err != nil {
		t.Skip("qemu-system-x86_64 not found")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("/dev/kvm not available")
	}
	for _, p := range []string{kernel, initramfs} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("image artifact missing: %s (run scripts/build-vm-image-alpine.sh)", p)
		}
	}
	return kernel, initramfs
}

func TestBootAndExec(t *testing.T) {
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

	// The headline: drive a real command in a real kernel over the out-of-band
	// serial control plane.
	resp, err := vm.Exec(ctx, "uname -s")
	if err != nil {
		t.Fatalf("Exec: %v\nconsole:\n%s", err, vm.ConsoleLog())
	}
	if got := strings.TrimSpace(resp.Out); got != "Linux" || resp.Exit != 0 {
		t.Fatalf("uname -s = %q (exit %d), want Linux/0", got, resp.Exit)
	}

	// Exit codes propagate.
	r2, err := vm.Exec(ctx, "exit 7")
	if err != nil {
		t.Fatalf("Exec exit 7: %v", err)
	}
	if r2.Exit != 7 {
		t.Fatalf("exit code = %d, want 7", r2.Exit)
	}

	// It's really Alpine.
	r3, _ := vm.Exec(ctx, "cat /etc/alpine-release 2>/dev/null || true")
	t.Logf("guest: uname=%q alpine-release=%q", resp.Out, strings.TrimSpace(r3.Out))
}
