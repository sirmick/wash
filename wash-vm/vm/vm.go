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
}

func (o *Opts) defaults() {
	if o.Mem == "" {
		o.Mem = "256M"
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
		"-serial", "chardev:ctl", // ttyS1: control plane
		"-chardev", "socket,id=data,path=" + dataPath + ",server=on,wait=off",
		"-serial", "chardev:data", // ttyS2: data plane (wash wire frames)
	}
	vm.cmd = exec.CommandContext(ctx, o.QEMU, args...)
	vm.cmd.Stderr = vm.stderr
	if err := vm.cmd.Start(); err != nil {
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
	return nil, fmt.Errorf("timeout after %s", timeout)
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
	if vm.cmd != nil && vm.cmd.Process != nil {
		vm.cmd.Process.Kill()
		vm.cmd.Wait()
	}
	if vm.dir != "" {
		os.RemoveAll(vm.dir)
	}
	return nil
}
