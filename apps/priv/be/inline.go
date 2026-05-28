package priv

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// inlineProc is one running sudo+argv subprocess for a wash-sudo
// invocation. It exposes the lifecycle controls wash-sudo cares
// about (write to stdin, signal EOF on stdin, kill the whole thing)
// and runs goroutines that fan stdout / stderr to a caller-supplied
// callback in chunks.
//
// Two flavors: pipe-mode (stdin/stdout/stderr are os pipes — wash-sudo
// CLI bridge) and pty-mode (a single PTY pair carries everything —
// packages-class apps embedding xterm). The flavor is set at start
// time and never changes; WriteStdin / Resize / Cancel branch on
// which fields are populated.
//
// One inlineProc per inline Request. Stored in State.inlineProcs.
type inlineProc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser // pipe mode: child's stdin
	pty     *os.File       // pty mode: master fd; writes reach the child as TTY input
	stdinMu sync.Mutex     // serialises Writes; CloseStdin sets closed
	closed  bool

	doneCh chan int // delivers exit code exactly once

	// echoTimer re-enables PTY echo ~100ms after password injection.
	// Cancel stops it so a process killed during that window doesn't
	// leave a pending timer around (and doesn't call setEcho on a
	// closed fd, even though that would silently fail).
	echoTimer *time.Timer

	cancelOnce sync.Once
}

// startInlineRun forks sudo wrapping argv, attaches the cached
// password to its stdin first, then keeps stdin open for the
// wrapped child to read user input. Returns once cmd.Start succeeds
// — caller drives the lifecycle via the returned *inlineProc.
//
// The stdout/stderr callback is invoked from goroutines; it must
// be safe to call concurrently. Chunks are NOT linewise — the
// callback gets whatever the os pipe surfaces, typically the full
// pipe buffer up to 4 KiB.
func startInlineRun(sudoBin string, argv []string, cwd string, callerEnv map[string]string, pw []byte, onStream func(stream string, b []byte)) (*inlineProc, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("argv is empty")
	}
	cmdArgs := []string{"-S", "-k"}
	// --preserve-env: combine our WASH_* allowlist with the caller-
	// requested vars. sudo's --preserve-env accepts a comma list.
	preserve := []string{"WASH_DISPLAY", "WASH_PROTO", "WASH_CONTROL_SOCKET", "WASH_BIN_DIR"}
	for k := range callerEnv {
		preserve = append(preserve, k)
	}
	sort.Strings(preserve)
	cmdArgs = append(cmdArgs, "--preserve-env="+strings.Join(preserve, ","))
	cmdArgs = append(cmdArgs, "--")
	cmdArgs = append(cmdArgs, argv...)
	c := exec.Command(sudoBin, cmdArgs...)
	c.Dir = cwd
	c.Env = mergeEnv(os.Environ(), callerEnv)

	// Stdin pipe. We write pw + "\n" up front so sudo -S grabs it;
	// subsequent bytes go to the wrapped child (sudo forwards
	// trailing stdin after auth). Keep the writer open so wash-sudo
	// can continue feeding input.
	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("start sudo: %w", err)
	}
	// Inject the password synchronously before returning. The
	// caller wipes its pw buffer on our return, so we MUST get the
	// bytes into sudo's stdin before that — a goroutine racing
	// the wipe would write zeros. Stdin stays open after; sudo
	// forwards subsequent bytes to the child.
	pwLine := make([]byte, 0, len(pw)+1)
	pwLine = append(pwLine, pw...)
	pwLine = append(pwLine, '\n')
	_, _ = stdin.Write(pwLine)
	wipeBytes(pwLine)
	p := &inlineProc{
		cmd:    c,
		stdin:  stdin,
		doneCh: make(chan int, 1),
	}
	// stdout / stderr fan-out.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyStream(stdout, "stdout", onStream) }()
	go func() { defer wg.Done(); copyStream(stderr, "stderr", onStream) }()
	go func() {
		err := c.Wait()
		wg.Wait()
		exit := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			} else {
				exit = -1
			}
		}
		p.doneCh <- exit
		close(p.doneCh)
	}()
	return p, nil
}

// copyStream reads chunks from r and calls onStream(name, chunk).
// Stops on EOF or error. The buffer size matches the Linux pipe
// PAGE_SIZE; larger writes are split by the kernel anyway.
func copyStream(r io.Reader, name string, onStream func(stream string, b []byte)) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			// Copy the slice — onStream may dispatch async (e.g. send
			// via SDK) and we're about to reuse buf.
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			onStream(name, chunk)
		}
		if err != nil {
			return
		}
	}
}

// WriteStdin pushes bytes from the requester into the subprocess's
// input. Routes to the stdin pipe (pipe mode) or PTY master (pty
// mode). Safe to call concurrently with CloseStdin — after close,
// further writes are silently dropped.
func (p *inlineProc) WriteStdin(b []byte) {
	if len(b) == 0 {
		return
	}
	p.stdinMu.Lock()
	closed := p.closed
	stdin := p.stdin
	pty := p.pty
	p.stdinMu.Unlock()
	if closed {
		return
	}
	switch {
	case pty != nil:
		_, _ = pty.Write(b)
	case stdin != nil:
		_, _ = stdin.Write(b)
	}
}

// CloseStdin shuts down the subprocess's input. In pipe mode this
// is the standard EOF signal a program uses to know "no more input."
// In pty mode we don't close the master — closing it kills the
// session, which Cancel does explicitly.
func (p *inlineProc) CloseStdin() {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
}

// Resize updates the PTY winsize. No-op in pipe mode.
func (p *inlineProc) Resize(cols, rows uint16) {
	if p.pty == nil || cols == 0 || rows == 0 {
		return
	}
	_ = ptySetsize(p.pty, cols, rows)
}

// Cancel kills the subprocess. Best-effort; the Wait goroutine will
// observe the kill and complete normally with a non-zero exit code.
func (p *inlineProc) Cancel() {
	p.cancelOnce.Do(func() {
		if p.echoTimer != nil {
			p.echoTimer.Stop()
		}
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		if p.pty != nil {
			_ = p.pty.Close()
		}
	})
}

// Wait blocks until the subprocess exits and returns the exit code.
func (p *inlineProc) Wait() int {
	return <-p.doneCh
}

// wipeBytes zeroes a byte slice. Local copy of the queue.go helper
// — keeps inline.go's import list small.
func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// mergeEnv overlays callerEnv on top of base. Used so the user can
// add specific env vars via --preserve-env without dropping any of
// the inherited WASH_* env vars.
func mergeEnv(base []string, overlay map[string]string) []string {
	if len(overlay) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overlay))
	seen := map[string]bool{}
	for _, kv := range base {
		idx := bytes.IndexByte([]byte(kv), '=')
		if idx < 0 {
			out = append(out, kv)
			continue
		}
		key := kv[:idx]
		if v, ok := overlay[key]; ok {
			out = append(out, key+"="+v)
			seen[key] = true
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overlay {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}
