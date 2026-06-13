//go:build linux

package vm

import (
	"os/exec"
	"syscall"
)

// setPdeathsig asks the kernel to SIGKILL the child the instant THIS process
// dies — even on our own SIGKILL, a panic, or a `make`/Ctrl-C interrupt that
// never runs deferred cleanup. So a qemu VM can't outlive the test that
// launched it, which is what prevented the orphan accumulation that eventually
// exhausts the inotify-instance ceiling. Belt to the context-cancel + Close()
// braces that already handle graceful exits.
func setPdeathsig(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
