package router

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Spawn fork+execs an app binary with a socketpair attached as fd 3
// (WIRE.md §1). It returns the parent end of the socket as an
// *os.File and the *exec.Cmd whose Wait() will reap the child.
//
// The caller owns both: it should Close() the file when done routing
// frames and call Cmd.Wait() to reap the process. extraEnv is
// appended to the minimum environment.
func Spawn(binary, appID, instanceID string, extraEnv []string) (*exec.Cmd, *os.File, error) {
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("socketpair: %w", err)
	}
	parent := os.NewFile(uintptr(pair[0]), binary+"#parent")
	child := os.NewFile(uintptr(pair[1]), binary+"#child")

	cmd := exec.Command(binary)
	cmd.ExtraFiles = []*os.File{child}
	// Apps inherit the router's environment (HOME, PATH, $SHELL, …)
	// so a terminal can run real shell sessions and a launched
	// program can find its own files. The wash-specific env vars
	// are layered on top. Probe.go uses its own stripped env.
	cmd.Env = append(os.Environ(),
		"WASH_PROTO=1",
		"WASH_APP_ID="+appID,
		"WASH_INSTANCE_ID="+instanceID,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		_ = parent.Close()
		_ = child.Close()
		return nil, nil, fmt.Errorf("start %s: %w", binary, err)
	}
	// The child has its own fd 3; we close our end so EOF propagates
	// when only one side remains.
	_ = child.Close()
	return cmd, parent, nil
}
