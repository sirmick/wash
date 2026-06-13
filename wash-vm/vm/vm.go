// Package vm is the host side of the wash-vm/vm harness (docs/NET.md §8): it
// boots a microvm under qemu and talks to the in-guest agent over an
// out-of-band serial control plane. The network is the system under test, so
// this channel never rides it. B0 scope: Launch + Ctl.Exec; the embedded
// HTTP/WS proxy and named virtio-serial planes build on top.
package vm

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/washvm/proto"
	"github.com/sirmick/wash/internal/wire"
)

// Opts configures a microvm launch. Kernel and Initramfs are required.
type Opts struct {
	Kernel     string
	Initramfs  string
	Mem        string // default "256M"
	Accel      string // default "kvm"
	CPU        string // default "host"
	KernelArgs string // default "console=ttyS0 panic=-1"
	QEMU       string // default "qemu-system-x86_64"
	// Extra is appended verbatim to the qemu argv after wash's defaults, so a
	// caller (washvm-run's `-- …` passthrough, docs/NET.md §8.2) can add or
	// override devices/flags — e.g. `-smp 2` `-m 2048` `-cpu max`.
	Extra []string
}

func (o *Opts) defaults() {
	if o.Mem == "" {
		o.Mem = "1024M" // NM + linux-virt modules live in a RAM-backed initramfs
	}
	if o.Accel == "" {
		o.Accel = "kvm"
	}
	if o.CPU == "" {
		o.CPU = "host"
	}
	if o.KernelArgs == "" {
		o.KernelArgs = "console=ttyS0 panic=-1"
	}
	if o.QEMU == "" {
		o.QEMU = "qemu-system-x86_64"
	}
}

// VM is a running microvm with an open control plane (and a data plane carrying
// wash wire frames — the inside-out counterpart of wash-vm's in-browser
// VirtioConsoleSocket).
type VM struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc // tears down qemu on Close (NOT on the boot ctx)
	ctl     net.Conn
	data    net.Conn
	dataT   *wire.StreamTransport
	dir     string
	logPath string
	stderr  *bytes.Buffer

	mu     sync.Mutex
	nextID uint64
}

// Launch boots the microvm and connects the control plane. The caller must
// Close the returned VM.
func Launch(ctx context.Context, o Opts) (*VM, error) {
	o.defaults()
	if o.Kernel == "" || o.Initramfs == "" {
		return nil, fmt.Errorf("vm: Kernel and Initramfs are required")
	}
	dir, err := os.MkdirTemp("", "washvm-")
	if err != nil {
		return nil, err
	}
	vm := &VM{
		dir:     dir,
		logPath: filepath.Join(dir, "console.log"),
		stderr:  &bytes.Buffer{},
	}
	ctlPath := filepath.Join(dir, "ctl.sock")
	dataPath := filepath.Join(dir, "data.sock")

	args := []string{
		"-machine", "q35", "-accel", o.Accel, "-cpu", o.CPU,
		"-m", o.Mem, "-smp", "1",
		"-display", "none", "-nodefaults", "-no-reboot",
		"-kernel", o.Kernel, "-initrd", o.Initramfs, "-append", o.KernelArgs,
		"-serial", "file:" + vm.logPath, // ttyS0: console / log plane
		"-chardev", "socket,id=ctl,path=" + ctlPath + ",server=on,wait=off",
		"-serial", "chardev:ctl", // ttyS1: control plane (small line-framed Exec)
		// Data plane: the wash wire (FE bundle + protocol). A virtio-serial
		// port, NOT a UART — /dev/vport0p0 in-guest is a raw chardev with no tty
		// line discipline, so the in-guest wash-router (which doesn't cfmakeraw
		// its transport fd) gets clean binary frames. UART cooked-mode would
		// mangle them (docs/NET.md §8.2). Host side stays a chardev socket, so
		// the proxy bridge is unchanged.
		"-chardev", "socket,id=data,path=" + dataPath + ",server=on,wait=off",
		"-device", "virtio-serial-pci,id=vser0",
		// nr=1: port 0 on a virtio-serial bus is reserved for the console, so
		// the first data port is deterministically /dev/vport0p1 in-guest.
		"-device", "virtserialport,chardev=data,name=wash.data,nr=1",
	}
	// Managed NICs for NetworkManager (docs/NET.md §5/B4). Several eth devices so
	// the backend can be tested configuring them different ways — eth0 for
	// baseline connectivity (qemu user-net: NAT'd, built-in DHCP + 10.0.2.2
	// gateway, so the commit-confirm VERIFY has something real), eth1..eth3 as
	// fodder for bridging and VLANs. All in-band; the control plane stays
	// out-of-band on the serial.
	for n := 0; n < 4; n++ {
		args = append(args,
			"-netdev", fmt.Sprintf("user,id=net%d", n),
			"-device", fmt.Sprintf("virtio-net-pci,netdev=net%d,id=nic%d", n, n),
		)
	}
	// Caller passthrough (washvm-run `-- …`): appended last so it overrides/adds.
	args = append(args, o.Extra...)
	// qemu's lifetime is the VM's lifetime — until Close() — NOT the caller's
	// boot context. The passed ctx bounds the dial/handshake below (which is
	// what a "boot timeout" should cap); binding qemu itself to it would
	// SIGKILL a healthy VM the moment that timeout elapses, so a browser that
	// connected even a minute after boot would find a dead data/ctl plane.
	qemuCtx, cancel := context.WithCancel(context.Background())
	vm.cancel = cancel
	vm.cmd = exec.CommandContext(qemuCtx, o.QEMU, args...)
	setPdeathsig(vm.cmd) // qemu dies with us — no orphan VMs on SIGKILL/panic/interrupt
	vm.cmd.Stderr = vm.stderr
	if err := vm.cmd.Start(); err != nil {
		cancel()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("vm: start qemu: %w", err)
	}

	conn, err := vm.dialCtl(ctx, ctlPath, 15*time.Second)
	if err != nil {
		vm.Close()
		return nil, fmt.Errorf("vm: connect control plane: %w\nqemu stderr:\n%s", err, vm.stderr.String())
	}
	vm.ctl = conn

	dconn, err := vm.dialCtl(ctx, dataPath, 15*time.Second)
	if err != nil {
		vm.Close()
		return nil, fmt.Errorf("vm: connect data plane: %w", err)
	}
	vm.data = dconn
	vm.dataT = wire.NewStreamTransport(dconn)
	return vm, nil
}

func (vm *VM) dialCtl(ctx context.Context, path string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if vm.cmd.ProcessState != nil && vm.cmd.ProcessState.Exited() {
			return nil, fmt.Errorf("qemu exited before control plane was ready")
		}
		if conn, err := net.Dial("unix", path); err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Include the console tail like WaitReady does — a boot that never
	// brings up the control plane explains itself on the console.
	return nil, fmt.Errorf("timeout after %s waiting for control plane\nconsole:\n%s", timeout, vm.ConsoleLog())
}

// WaitReady blocks until the guest agent's hello arrives, or ctx/timeout
// elapses. The agent sends hello once it has opened and rawed the control port
// after boot; the host must receive it before sending any request (startup
// FIFO-overflow protection).
func (vm *VM) WaitReady(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if vm.ctl == nil {
		return fmt.Errorf("vm: control plane not connected")
	}
	_ = vm.ctl.SetReadDeadline(deadline)
	var hello proto.Response
	if err := proto.ReadFrame(vm.ctl, &hello); err != nil {
		return fmt.Errorf("vm: agent not ready: %w\nconsole:\n%s", err, vm.ConsoleLog())
	}
	if hello.Out != "hello" {
		return fmt.Errorf("vm: unexpected hello %q", hello.Out)
	}
	return nil
}

// Exec runs a shell command in the guest over the control plane and returns its
// result. One command is in flight at a time.
func (vm *VM) Exec(ctx context.Context, cmd string) (proto.Response, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if vm.ctl == nil {
		return proto.Response{}, fmt.Errorf("vm: control plane not connected")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(15 * time.Second)
	}
	_ = vm.ctl.SetDeadline(deadline)

	vm.nextID++
	id := vm.nextID
	if err := proto.WriteFrame(vm.ctl, proto.Request{ID: id, Cmd: cmd}); err != nil {
		return proto.Response{}, fmt.Errorf("vm: write: %w", err)
	}
	var resp proto.Response
	if err := proto.ReadFrame(vm.ctl, &resp); err != nil {
		return proto.Response{}, fmt.Errorf("vm: read: %w", err)
	}
	return resp, nil
}

// ConsoleLog returns the captured guest console (ttyS0) so far.
func (vm *VM) ConsoleLog() string {
	b, _ := os.ReadFile(vm.logPath)
	return string(b)
}

// Close terminates the VM and cleans up.
func (vm *VM) Close() error {
	if vm.ctl != nil {
		vm.ctl.Close()
	}
	if vm.data != nil {
		vm.data.Close()
	}
	if vm.cancel != nil {
		vm.cancel()
	}
	if vm.cmd != nil && vm.cmd.Process != nil {
		vm.cmd.Process.Kill()
		vm.cmd.Wait()
	}
	if vm.dir != "" {
		os.RemoveAll(vm.dir)
	}
	return nil
}
