// Package pty wraps creack/pty + a wash raw channel into a single
// "shell session" primitive. wash-term and wash-edit both fork PTYs
// the same way; this package is the one implementation both use.
//
// A Session owns one PTY, one *exec.Cmd, and one *sdk.RawChannel.
// Open spawns argv (defaulting to $SHELL when argv is empty), wires
// pty↔channel with two io.Copy goroutines, and reaps the process
// when either side EOFs. Close kills the shell and tears the channel
// down; Resize updates the PTY winsize.
package pty

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	creackpty "github.com/creack/pty"
	"github.com/sirmick/wash/internal/sdk"
)

// Session is one PTY + raw-channel pair. Methods are safe to call
// from any goroutine.
type Session struct {
	pty *os.File
	cmd *exec.Cmd
	ch  *sdk.RawChannel

	// Shell is the basename of the program running in the PTY. Useful
	// for tab labels (wash-term uses it).
	Shell string

	// sizeMu guards cols/rows — the last geometry applied to the
	// PTY. A reattaching FE asks for it (via wash-term's
	// list_sessions) so the restored xterm can open at the same
	// grid the scrollback replay was rendered for, then reflow.
	sizeMu sync.Mutex
	cols   uint16
	rows   uint16

	closeOnce sync.Once
	onClose   func(s *Session, reason string)
}

// Size returns the last cols/rows applied to the PTY.
func (s *Session) Size() (cols, rows uint16) {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	return s.cols, s.rows
}

// ID returns the wash channel id for the session — every PTY has a
// unique channel, so this is also the natural session key.
func (s *Session) ID() uint32 { return s.ch.ID() }

// Cmd exposes the wrapped *exec.Cmd for callers that need pid or
// the OS process handle (signaling, etc).
func (s *Session) Cmd() *exec.Cmd { return s.cmd }

// Open spawns argv inside a fresh PTY and binds it to a new raw
// channel on conn's window. argv[0] is exec.Command-resolved against
// $PATH; pass nil/empty argv to default to the user's $SHELL.
//
// envFn lets the caller adjust the inherited environment (e.g.
// wash-term prepending WASH_BIN_DIR to PATH and pinning TERM). Pass
// nil to inherit os.Environ() unchanged.
//
// onClose fires once when the session ends (pty EOF or explicit
// Close). The callback receives the Session (so the caller can look
// up its channel id without capturing it before Open returns) and
// the reason — "pty eof" on natural exit, or whatever the caller
// passed to CloseWithReason.
//
// Must NOT be called from an SDK callback — OpenChannel can't run on
// the read goroutine. Callers should `go pty.Open(...)`.
func Open(ctx context.Context, conn *sdk.Conn, windowID uint32, cols, rows uint16, argv []string, envFn func([]string) []string, onClose func(s *Session, reason string)) (*Session, error) {
	ch, err := conn.OpenChannel(ctx, windowID)
	if err != nil {
		return nil, err
	}

	var shellPath string
	var cmd *exec.Cmd
	if len(argv) > 0 {
		cmd = exec.Command(argv[0], argv[1:]...)
		shellPath = argv[0]
	} else {
		shellPath = userShell()
		cmd = exec.Command(shellPath)
	}
	env := os.Environ()
	if envFn != nil {
		env = envFn(env)
	}
	cmd.Env = env

	f, startErr := creackpty.StartWithSize(cmd, &creackpty.Winsize{Cols: cols, Rows: rows})
	if startErr != nil {
		_ = ch.Close()
		return nil, startErr
	}

	s := &Session{
		pty:     f,
		cmd:     cmd,
		ch:      ch,
		Shell:   shellPath,
		cols:    cols,
		rows:    rows,
		onClose: onClose,
	}

	// pty → channel
	go func() {
		_, copyErr := io.Copy(ch, f)
		if !isPtyTerm(copyErr) {
			// Real I/O error, not the normal EOF/EIO of a closing pty —
			// without this line the session just goes dark.
			log.Printf("pty: win=%d shell=%s pty→channel copy: %v", windowID, shellPath, copyErr)
		}
		s.closeWithReason("pty eof")
	}()
	// channel → pty
	go func() {
		_, copyErr := io.Copy(f, ch)
		if !isPtyTerm(copyErr) {
			log.Printf("pty: win=%d shell=%s channel→pty copy: %v", windowID, shellPath, copyErr)
		}
		// Channel torn down — kill the shell so the other goroutine
		// sees EOF on the pty fd.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	// Reaper goroutine: cmd.Wait() so the OS doesn't carry a zombie
	// once the shell exits. The io.Copy goroutines see EOF on pty
	// closure next. The exit status is the one fact a "my terminal
	// died" report needs, so it is always logged.
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("pty: win=%d shell=%s exited: %v", windowID, shellPath, err)
		} else {
			log.Printf("pty: win=%d shell=%s exited cleanly", windowID, shellPath)
		}
	}()

	return s, nil
}

// Resize updates the PTY's winsize. The shell inside receives
// SIGWINCH automatically.
func (s *Session) Resize(cols, rows uint16) error {
	s.sizeMu.Lock()
	s.cols, s.rows = cols, rows
	s.sizeMu.Unlock()
	return creackpty.Setsize(s.pty, &creackpty.Winsize{Cols: cols, Rows: rows})
}

// Close kills the shell and tears down the raw channel. Idempotent;
// safe to call multiple times. Calling Close fires onClose with
// reason "closed" (or whatever the user passes).
func (s *Session) Close() error {
	s.closeWithReason("closed")
	return nil
}

// CloseWithReason is Close with a caller-supplied reason — wash-term
// uses "pty eof" and "user requested" depending on the trigger.
func (s *Session) CloseWithReason(reason string) {
	s.closeWithReason(reason)
}

func (s *Session) closeWithReason(reason string) {
	s.closeOnce.Do(func() {
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		_ = s.pty.Close()
		_ = s.ch.Close()
		if s.onClose != nil {
			s.onClose(s, reason)
		}
	})
}

// isPtyTerm reports whether err is the kind of error you see when a
// pty's slave side has gone away. On Linux, reading the master after
// the slave closes (process exit / pty.Close) returns EIO; on Close,
// further reads can also be ErrClosed. Both mean "session ended,"
// not "something went wrong."
func isPtyTerm(err error) bool {
	return err == nil || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)
}

// userShell returns $SHELL, defaulting to /bin/bash. Both wash-term
// and wash-edit need this; it lives here so they don't redefine it.
func userShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/bash"
}

// WithWashEnv returns env with two wash-specific tweaks applied:
//
//   - TERM=xterm-256color (always — the shell side renders via xterm.js)
//   - PATH prefixed with $WASH_BIN_DIR when set, deduped — so the user
//     can run sibling wash CLIs (wash-launch, wash-fm, …) without an
//     absolute path. The router publishes WASH_BIN_DIR; if absent,
//     PATH is left alone.
//
// Used by wash-term and any future PTY-hosting app that wants its
// interactive shell to feel like a wash session.
func WithWashEnv(env []string) []string {
	binDir := lookupEnv(env, "WASH_BIN_DIR")
	out := make([]string, 0, len(env)+1)
	termSet := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") && binDir != "" {
			cur := kv[len("PATH="):]
			out = append(out, "PATH="+prependPath(cur, binDir))
			continue
		}
		if strings.HasPrefix(kv, "TERM=") {
			out = append(out, "TERM=xterm-256color")
			termSet = true
			continue
		}
		out = append(out, kv)
	}
	if !termSet {
		out = append(out, "TERM=xterm-256color")
	}
	if binDir != "" && lookupEnv(out, "PATH") == "" {
		out = append(out, "PATH="+binDir)
	}
	out = mapDisplayEnv(out)
	return out
}

// mapDisplayEnv turns the router's WASH_*-namespaced display hints (set
// by Router.spawnEnv from an env.publish, see docs/DISPLAY_ENV.md) into
// the real DISPLAY / WAYLAND_DISPLAY / XDG_RUNTIME_DIR a GUI client
// needs — so typing `xclock` or a Wayland app in a wash terminal just
// works. A pre-existing real var (the user exported their own) is never
// clobbered. Only wash-term applies this, via WithWashEnv: wash's own
// apps keep the inert WASH_* names and never become display clients.
func mapDisplayEnv(env []string) []string {
	maybeSet := func(env []string, src, dst string) []string {
		v := lookupEnv(env, src)
		if v == "" || lookupEnv(env, dst) != "" {
			return env
		}
		return append(env, dst+"="+v)
	}
	env = maybeSet(env, "WASH_X_DISPLAY", "DISPLAY")
	env = maybeSet(env, "WASH_WAYLAND_DISPLAY", "WAYLAND_DISPLAY")
	env = maybeSet(env, "WASH_XDG_RUNTIME_DIR", "XDG_RUNTIME_DIR")
	return env
}

// PinTerm returns env with TERM forced to xterm-256color but PATH
// untouched. wash-edit's embedded terminal uses this — it doesn't
// run user shell scripts that need wash CLIs on PATH, so the extra
// dance from WithWashEnv would be noise.
func PinTerm(env []string) []string {
	out := make([]string, 0, len(env)+1)
	termSet := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			out = append(out, "TERM=xterm-256color")
			termSet = true
			continue
		}
		out = append(out, kv)
	}
	if !termSet {
		out = append(out, "TERM=xterm-256color")
	}
	return out
}

func lookupEnv(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

func prependPath(path, dir string) string {
	if path == "" {
		return dir
	}
	sep := string(os.PathListSeparator)
	parts := strings.Split(path, sep)
	out := []string{dir}
	for _, p := range parts {
		if p != dir {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
