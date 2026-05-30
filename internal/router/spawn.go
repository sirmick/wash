package router

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

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
func Spawn(binary, appID, display string, extraEnv []string) (*SpawnResult, error) {
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
		logBuf: newRingBuf(crashLogCap),
	}
	cmd.Stdout = io.MultiWriter(os.Stdout, res.logBuf)
	cmd.Stderr = io.MultiWriter(os.Stderr, res.logBuf)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", binary, err)
	}
	return res, nil
}

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
	logBuf *ringBuf
}

// LogTail returns the most-recent crashLogCap bytes of the spawn's
// stdout+stderr (interleaved in the order they arrived).
func (r *SpawnResult) LogTail() string {
	if r == nil || r.logBuf == nil {
		return ""
	}
	return r.logBuf.String()
}

// ringBuf is a fixed-capacity tail buffer: writes never block, never
// fail, and silently drop the oldest bytes when full. Concurrent
// writes (stdout pipe + stderr pipe) are serialised via mu.
type ringBuf struct {
	mu    sync.Mutex
	buf   []byte
	cap   int
	full  bool
	start int
}

func newRingBuf(cap int) *ringBuf {
	return &ringBuf{cap: cap, buf: make([]byte, 0, cap)}
}

func (r *ringBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// ringBuf never rejects input — it accepts every byte and drops
	// the oldest when full — so Write always reports len(p) consumed.
	// (io.MultiWriter raises ErrShortWrite if we return anything else.)
	total := len(p)
	n := total
	if n == 0 {
		return 0, nil
	}
	// If p is larger than the whole buffer, only the last cap bytes
	// matter — drop the prefix.
	if n >= r.cap {
		copy(r.buf[:cap(r.buf)], p[n-r.cap:])
		r.buf = r.buf[:r.cap]
		r.full = true
		r.start = 0
		return n, nil
	}
	if !r.full {
		// Still room: append until we either hit cap or stop.
		room := r.cap - len(r.buf)
		if n <= room {
			r.buf = append(r.buf, p...)
			return n, nil
		}
		r.buf = append(r.buf, p[:room]...)
		r.full = true
		r.start = 0
		p = p[room:]
		n -= room
	}
	// Full: overwrite starting at start, wrapping.
	for n > 0 {
		end := r.start + n
		if end > r.cap {
			copied := r.cap - r.start
			copy(r.buf[r.start:], p[:copied])
			p = p[copied:]
			n -= copied
			r.start = 0
		} else {
			copy(r.buf[r.start:end], p)
			r.start = end % r.cap
			n = 0
		}
	}
	return total, nil
}

// String returns the buffered bytes in arrival order.
func (r *ringBuf) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return string(r.buf)
	}
	var out bytes.Buffer
	out.Grow(r.cap)
	out.Write(r.buf[r.start:])
	out.Write(r.buf[:r.start])
	return out.String()
}
