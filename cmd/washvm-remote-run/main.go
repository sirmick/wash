// Command washvm-remote-run is the two-VM harness for the wash-remote (R2)
// capstone e2e (docs/REMOTE.md). It boots two microvms on a shared qemu
// mcast L2 segment:
//
//   - VM-A — the desktop. Fronted by the HTTP/WS chrome proxy (like
//     washvm-run); the browser loads it and drives wash-connect.
//   - VM-B — the remote host. Runs sshd; A's com.wash.remote SSHes in and
//     brings up B's wash-router, whose app windows composite into A.
//
// Both run the same alpine wash image. The harness uses the out-of-band
// serial control plane (vm.Exec — never the segment under test) to: give
// each VM a static IP on the segment NIC, start sshd on B, generate a
// passphraseless key for A's `wash` user, authorize it on B, and sanity-
// check the ssh path before handing the browser a working desktop. Then it
// fronts VM-A's proxy, prints "wash-vm ready at <URL>", and blocks.
//
//	washvm-remote-run --chrome out/vm-chrome --addr 127.0.0.1:0
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/wash-vm/vm"
)

// Segment addressing. eth0..eth3 are qemu user-mode NICs (per-VM NAT); the
// mcast NIC the harness adds is the 5th → eth4, the only path between the
// two VMs. B is the host the user connects to as `wash@10.77.0.2`.
const (
	segNIC = "eth4"
	ipA    = "10.77.0.1"
	ipB    = "10.77.0.2"
	cidr   = "/24"
	// RemoteHost is what the e2e types into wash-connect.
	remoteHost = "wash@" + ipB
)

func main() {
	kernel := flag.String("kernel", "out/vm/vmlinuz", "guest kernel image")
	initramfs := flag.String("initramfs", "out/vm/initramfs.gz", "guest initramfs")
	chrome := flag.String("chrome", "out/vm-chrome", "host chrome dir served at /")
	addr := flag.String("addr", "127.0.0.1:0", "proxy listen address (VM-A)")
	mem := flag.String("mem", "1024M", "per-guest memory")
	bootTimeout := flag.Duration("boot-timeout", 60*time.Second, "guest readiness timeout")
	flag.Parse()

	if err := run(*kernel, *initramfs, *chrome, *addr, *mem, *bootTimeout); err != nil {
		fmt.Fprintln(os.Stderr, "washvm-remote-run:", err)
		os.Exit(1)
	}
}

func run(kernel, initramfs, chrome, addr, mem string, bootTimeout time.Duration) error {
	bootCtx, cancelBoot := context.WithTimeout(context.Background(), bootTimeout)
	defer cancelBoot()

	port, err := freeUDPPort()
	if err != nil {
		return fmt.Errorf("pick mcast port: %w", err)
	}
	const group = "230.0.0.77"
	mkOpts := func(mac string) vm.Opts {
		return vm.Opts{
			Kernel: kernel, Initramfs: initramfs, Mem: mem,
			Extra: vm.MCastLAN("seg", group, port, mac),
		}
	}

	a, err := vm.Launch(bootCtx, mkOpts("52:54:00:77:00:01"))
	if err != nil {
		return fmt.Errorf("launch VM-A: %w", err)
	}
	defer a.Close()
	b, err := vm.Launch(bootCtx, mkOpts("52:54:00:77:00:02"))
	if err != nil {
		return fmt.Errorf("launch VM-B: %w", err)
	}
	defer b.Close()

	if err := a.WaitReady(bootCtx); err != nil {
		return fmt.Errorf("VM-A not ready: %w", err)
	}
	if err := b.WaitReady(bootCtx); err != nil {
		return fmt.Errorf("VM-B not ready: %w", err)
	}

	if err := setupRemote(bootCtx, a, b); err != nil {
		return err
	}

	p, err := a.Proxy(vm.ProxyOpts{Static: chrome, Addr: addr})
	if err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	defer p.Close()

	fmt.Printf("wash-vm ready at %s\n", p.URL)
	os.Stdout.Sync()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Fprintln(os.Stderr, "washvm-remote-run: shutting down")
	return nil
}

// setupRemote wires the segment + ssh trust between the two guests over the
// out-of-band control plane, then proves A→B ssh works before the browser
// relies on it.
func setupRemote(ctx context.Context, a, b *vm.VM) error {
	// Static IPs on the segment NIC. Take it off NM's hands first so NM
	// can't flap or clear the manual address.
	if err := ifup(ctx, a, ipA); err != nil {
		return fmt.Errorf("VM-A seg ifup: %w", err)
	}
	if err := ifup(ctx, b, ipB); err != nil {
		return fmt.Errorf("VM-B seg ifup: %w", err)
	}

	// B: bring up sshd. Host keys + privsep dir + run the daemon. The wash
	// user already exists (image), with a real shell + password, so pubkey
	// auth is the only thing left to wire.
	if err := exec(ctx, b,
		"ssh-keygen -A; mkdir -p /var/empty /run/sshd; "+
			"su wash -c 'mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys'; "+
			"/usr/sbin/sshd",
	); err != nil {
		return fmt.Errorf("VM-B sshd: %w", err)
	}

	// A: generate the wash user's key (passphraseless → BatchMode ssh works
	// without an agent; the ssh-add widget path is exercised separately).
	if err := exec(ctx, a,
		"su wash -c 'mkdir -p ~/.ssh && chmod 700 ~/.ssh && [ -f ~/.ssh/id_ed25519 ] || ssh-keygen -t ed25519 -N \"\" -f ~/.ssh/id_ed25519 -q'",
	); err != nil {
		return fmt.Errorf("VM-A keygen: %w", err)
	}
	pub, err := out(ctx, a, "su wash -c 'cat ~/.ssh/id_ed25519.pub'")
	if err != nil {
		return fmt.Errorf("VM-A read pubkey: %w", err)
	}
	pub = strings.TrimSpace(pub)
	if pub == "" {
		return fmt.Errorf("VM-A pubkey empty")
	}

	// Authorize A's key on B.
	if err := exec(ctx, b, fmt.Sprintf("su wash -c 'echo %q >> ~/.ssh/authorized_keys'", pub)); err != nil {
		return fmt.Errorf("authorize key on VM-B: %w", err)
	}

	// Sanity: prove A→B ssh works as the wash user before the browser drives
	// it, so a setup fault fails here with a clear message (not a mysterious
	// "host never comes up" in the UI). accept-new mirrors com.wash.remote.
	probe := fmt.Sprintf(
		"su wash -c 'ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 %s echo WASH_SSH_OK'",
		remoteHost)
	got, err := out(ctx, a, probe)
	if err != nil {
		return fmt.Errorf("A→B ssh probe: %w", err)
	}
	if !strings.Contains(got, "WASH_SSH_OK") {
		return fmt.Errorf("A→B ssh probe did not return OK: %q", got)
	}
	return nil
}

// ifup assigns a static /24 to the segment NIC, after detaching it from NM.
func ifup(ctx context.Context, m *vm.VM, ip string) error {
	return exec(ctx, m,
		"nmcli dev set "+segNIC+" managed no 2>/dev/null; "+
			"ip addr flush dev "+segNIC+" 2>/dev/null; "+
			"ip addr add "+ip+cidr+" dev "+segNIC+"; "+
			"ip link set "+segNIC+" up")
}

// exec runs cmd in the guest and fails on a non-zero exit.
func exec(ctx context.Context, m *vm.VM, cmd string) error {
	r, err := m.Exec(ctx, cmd)
	if err != nil {
		return err
	}
	if r.Exit != 0 {
		return fmt.Errorf("exit %d: %s%s (cmd: %s)", r.Exit, r.Out, r.Err, cmd)
	}
	return nil
}

// out runs cmd and returns stdout (ignoring exit code — the caller inspects
// the output, e.g. the ssh probe whose marker proves success).
func out(ctx context.Context, m *vm.VM, cmd string) (string, error) {
	r, err := m.Exec(ctx, cmd)
	if err != nil {
		return "", err
	}
	return r.Out, nil
}

// freeUDPPort returns an unused UDP port for the per-run mcast group, so
// parallel harness instances don't share an L2 segment.
func freeUDPPort() (int, error) {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port, nil
}
