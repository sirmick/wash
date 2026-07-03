package router

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// inheritedAppEnv returns the router's environment with host GUI display
// handles stripped. wash-display republishes its own sockets as WASH_* hints;
// leaking the router process's DISPLAY/WAYLAND_DISPLAY makes GUI clients typed
// in wash-term open on the host desktop instead of inside wash.
func inheritedAppEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "DISPLAY", "WAYLAND_DISPLAY", "WAYLAND_SOCKET", "XAUTHORITY":
			continue
		default:
			out = append(out, kv)
		}
	}
	return out
}

// Spawn launches an app binary. The child dials the router's wash
// socket (passed via WASH_DISPLAY env) and sends an Identity frame.
// The router matches incoming Identity to a pending-attach record
// by pid and hands the conn back to the spawn caller as the app's
// transport.
//
// Caller owns the returned SpawnResult: it must wait on the matching
// pendingAttach channel for the real AppInstance, and reap the
// process via Cmd.Wait() when the app exits.
//
// instance_id is assigned by the router in IdentityAck, not at
// spawn time — apps read it from there, not from env.
//
// Stdout/stderr are tee'd: the router's own stdout/stderr still see
// every line (so a developer's terminal log is unchanged), AND a
// per-spawn ring buffer captures the tail for crash reporting. On
// abnormal exit the router pulls the tail out of LogTail() and
// ships it to the shell as a ShellAppCrashed event.
func Spawn(binary, appID, display string, extraEnv, extraArgs []string) (*SpawnResult, error) {
	if display == "" {
		return nil, fmt.Errorf("spawn %s: WASH_DISPLAY (control socket) is required — was the router started with --control-socket none?", appID)
	}
	// extraArgs are appended to argv (e.g. ["--open", path] for open.request).
	cmd := exec.Command(binary, extraArgs...)
	// Apps inherit the router's environment (HOME, PATH, $SHELL, …)
	// so a terminal can run real shell sessions and a launched
	// program can find its own files. The wash-specific env vars
	// are layered on top. Probe.go uses its own stripped env.
	cmd.Env = append(inheritedAppEnv(),
		"WASH_DISPLAY="+display,
		"WASH_PROTO=1",
		"WASH_APP_ID="+appID,
		// Spawn apps with full goroutine-stack tracebacks so a
		// panic in any worker (not just the panicking goroutine)
		// appears in the captured log, which is what the crash
		// dialog renders. Override by setting GOTRACEBACK in
		// extraEnv if a specific app needs different behaviour.
		"GOTRACEBACK=all",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	res := &SpawnResult{
		Cmd:    cmd,
		logBuf: newRingBuffer(crashLogCap),
	}
	cmd.Stdout = io.MultiWriter(os.Stdout, res.logBuf)
	cmd.Stderr = io.MultiWriter(os.Stderr, res.logBuf)
	// Because Stdout/Stderr are io.Writers (not *os.File), exec routes them
	// through an OS pipe with a copy goroutine, and cmd.Wait blocks until the
	// pipe's write end is closed by EVERY inheritor — including a grandchild
	// that outlives the app. Without a bound, such a grandchild pins Wait, and
	// every tearDown gated on it, indefinitely: the window lingers and the
	// singleton slot stays claimed (REVIEW-RECONNECT L3). WaitDelay caps the
	// post-exit wait — once the process itself exits (its own output already
	// flushed), Wait force-closes the pipes and returns after spawnWaitDelay.
	cmd.WaitDelay = spawnWaitDelay
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", binary, err)
	}
	return res, nil
}

// spawnWaitDelay bounds how long Cmd.Wait blocks after a spawned app's
// process has exited, waiting for the tee'd stdout/stderr pipes to close.
// It caps the grandchild-holds-the-pipe teardown hang (REVIEW-RECONNECT L3).
// A package var so tests can shorten it. Generous by default so a normal
// app's final output isn't truncated — the wait only starts once the
// process has already exited.
var spawnWaitDelay = 10 * time.Second

// crashLogCap is the per-spawn ring-buffer size. Big enough for a
// Go panic with GOTRACEBACK=all on a normal app (~dozens of
// goroutines) without being so large that a chatty stdout app burns
// memory for nothing.
const crashLogCap = 16 * 1024

// SpawnResult bundles the spawned cmd with its captured output tail.
// Callers should treat LogTail() as a snapshot — it returns whatever
// the ring buffer currently holds, with no guarantee that more lines
// won't arrive before cmd.Wait() returns.
type SpawnResult struct {
	Cmd    *exec.Cmd
	logBuf *ringBuffer
}

// LogTail returns the most-recent crashLogCap bytes of the spawn's
// stdout+stderr (interleaved in the order they arrived).
func (r *SpawnResult) LogTail() string {
	if r == nil || r.logBuf == nil {
		return ""
	}
	return r.logBuf.String()
}
