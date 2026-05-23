package router

import (
	"fmt"
	"os"
	"os/exec"
)

// Spawn launches an app binary. The child dials the router's wash
// socket (passed via WASH_DISPLAY env) and sends an Identity frame.
// The router matches incoming Identity to a pending-attach record
// by pid and hands the conn back to the spawn caller as the app's
// transport.
//
// Caller owns the returned Cmd: it must wait on the matching
// pendingAttach channel for the real AppInstance, and reap the
// process via Cmd.Wait() when the app exits.
//
// instance_id is assigned by the router in IdentityAck, not at
// spawn time — apps read it from there, not from env.
func Spawn(binary, appID, display string, extraEnv []string) (*exec.Cmd, error) {
	if display == "" {
		return nil, fmt.Errorf("spawn %s: WASH_DISPLAY (control socket) is required — was the router started with --control-socket none?", appID)
	}
	cmd := exec.Command(binary)
	// Apps inherit the router's environment (HOME, PATH, $SHELL, …)
	// so a terminal can run real shell sessions and a launched
	// program can find its own files. The wash-specific env vars
	// are layered on top. Probe.go uses its own stripped env.
	cmd.Env = append(os.Environ(),
		"WASH_DISPLAY="+display,
		"WASH_PROTO=1",
		"WASH_APP_ID="+appID,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", binary, err)
	}
	return cmd, nil
}
