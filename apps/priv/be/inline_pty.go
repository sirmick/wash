package priv

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	creackpty "github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// startInlinePTY is the PTY-allocated cousin of startInlineRun.
// Output and input both flow through the PTY master; stdout/stderr
// are merged (a real terminal has one output stream). The caller's
// onStream is invoked with stream="stdout" for everything — a single
// label keeps the wire envelope identical to pipe-mode runs.
//
// Password handling: we disable ECHO on the PTY before writing the
// password line, then re-enable it after a short delay so sudo has
// time to consume the bytes without echoing them back to the
// requester's terminal widget. The window is ~100ms; an interactive
// caller racing the re-enable with their own keystrokes would see
// briefly-unechoed input but no security leak.
func startInlinePTY(sudoBin string, argv []string, cwd string, callerEnv map[string]string, pw []byte, cols, rows uint16, onStream func(stream string, b []byte)) (*inlineProc, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("argv is empty")
	}
	isRoot := os.Geteuid() == 0
	injectPassword := !isRoot && len(pw) > 0
	var c *exec.Cmd
	switch {
	case isRoot:
		// Passthrough: already root. Exec the target directly.
		// No sudo, no password injection, no ECHO toggling.
		c = exec.Command(argv[0], argv[1:]...)
	default:
		var cmdArgs []string
		if injectPassword {
			cmdArgs = []string{"-S", "-k"}
		} else {
			// Passwordless: -n short-circuits if auth would be
			// needed. We checked at startup that NOPASSWD covers
			// us; if sudo prompts here something's wrong with the
			// policy and the child won't start (audit-loud failure).
			cmdArgs = []string{"-n"}
		}
		preserve := []string{"WASH_DISPLAY", "WASH_PROTO", "WASH_CONTROL_SOCKET", "WASH_BIN_DIR"}
		for k := range callerEnv {
			preserve = append(preserve, k)
		}
		sort.Strings(preserve)
		cmdArgs = append(cmdArgs, "--preserve-env="+strings.Join(preserve, ","))
		cmdArgs = append(cmdArgs, "--")
		cmdArgs = append(cmdArgs, argv...)
		c = exec.Command(sudoBin, cmdArgs...)
	}
	c.Dir = cwd
	// PTY-allocated programs expect a real terminal name; pin TERM
	// so curses-class output (apt progress, dialog) renders. Caller
	// env can still override.
	env := mergeEnv(os.Environ(), callerEnv)
	env = ensureTerm(env, "xterm-256color")
	c.Env = env

	// Default size when the requester didn't supply one. apt's
	// progress bar uses the column count; 80×24 is a safe minimum.
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	ptmx, startErr := creackpty.StartWithSize(c, &creackpty.Winsize{Cols: cols, Rows: rows})
	if startErr != nil {
		return nil, fmt.Errorf("start inline (pty): %w", startErr)
	}

	p := &inlineProc{
		cmd:    c,
		pty:    ptmx,
		doneCh: make(chan int, 1),
	}

	if injectPassword {
		// Disable ECHO before injecting the password so the bytes don't
		// round-trip back to the requester's xterm. We re-enable on a
		// short delay — sudo consumes "password\n" from stdin promptly
		// and execs the child, after which normal TTY echo is what an
		// interactive user would expect.
		if err := setEcho(ptmx, false); err != nil {
			_ = ptmx.Close()
			_ = c.Process.Kill()
			return nil, fmt.Errorf("disable echo: %w", err)
		}
		pwLine := make([]byte, 0, len(pw)+1)
		pwLine = append(pwLine, pw...)
		pwLine = append(pwLine, '\n')
		_, _ = ptmx.Write(pwLine)
		wipeBytes(pwLine)

		// Re-enable ECHO after sudo has consumed the password line.
		// Stored on the proc so Cancel can stop it — a process killed
		// inside the 100ms window would otherwise leave the timer
		// pending until it fires on a closed fd.
		p.echoTimer = time.AfterFunc(100*time.Millisecond, func() {
			_ = setEcho(ptmx, true)
		})
	}

	// pty → onStream. One stream label since the PTY merges
	// stdout/stderr at the kernel level.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				onStream("stdout", chunk)
			}
			if err != nil {
				return
			}
		}
	}()

	go func() {
		err := c.Wait()
		// Close the master so the reader goroutine unblocks; ignore
		// the error since the close may race with Cancel().
		_ = ptmx.Close()
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

// setEcho toggles the ECHO termios flag on the PTY's line
// discipline. Used during password injection to hide bytes the
// caller has decrypted from the password cache.
func setEcho(f *os.File, on bool) error {
	fd := int(f.Fd())
	attrs, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	if on {
		attrs.Lflag |= unix.ECHO
	} else {
		attrs.Lflag &^= unix.ECHO
	}
	return unix.IoctlSetTermios(fd, unix.TCSETS, attrs)
}

// ptySetsize is a thin wrapper so inline.go (which doesn't import
// creack/pty) can issue the resize without pulling that dep into a
// file it otherwise has no reason to touch.
func ptySetsize(f *os.File, cols, rows uint16) error {
	return creackpty.Setsize(f, &creackpty.Winsize{Cols: cols, Rows: rows})
}

// ensureTerm sets/overrides TERM in env. PTY-aware children that
// don't see TERM fall back to "dumb" and skip color/cursor codes,
// which is wrong for an xterm-rendered widget.
func ensureTerm(env []string, term string) []string {
	out := make([]string, 0, len(env)+1)
	seen := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			out = append(out, "TERM="+term)
			seen = true
			continue
		}
		out = append(out, kv)
	}
	if !seen {
		out = append(out, "TERM="+term)
	}
	return out
}

