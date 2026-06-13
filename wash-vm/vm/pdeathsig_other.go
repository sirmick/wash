//go:build !linux

package vm

import "os/exec"

// Pdeathsig is a Linux-only knob; on other platforms (macOS dev builds) a qemu
// VM's lifetime is bounded by the context-cancel + Close() in vm.go.
func setPdeathsig(*exec.Cmd) {}
