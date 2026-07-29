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
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	osuser "os/user"
	"strconv"
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

	// agentMu guards the OSC 7770 tee (agentosc.go): the handler and
	// the resumable scanner. Nil handler = no agent parsing at all, so
	// a PTY host that doesn't care (wash-edit's embedded terminal)
	// pays one nil check per read.
	// SetAgentHandler / feedAgent / agentTee live in agentosc.go.
	agentMu   sync.Mutex
	agentFn   func(AgentEvent)
	agentScan *agentOSCScanner
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

// ForegroundUser describes the program currently in the PTY's
// FOREGROUND process group — i.e. what the user is actually looking
// at, not the idle login shell. wash-term polls this ~1Hz to paint a
// per-tab badge (normal/root/ssh) and a status line.
//
// The classification follows the foreground program, so it flips the
// instant someone runs `sudo`/`ssh` and flips back when that command
// exits. State is one of:
//
//   - "root" — the foreground program's effective uid is 0. This also
//     covers `sudo`/`su` while they run, since those are setuid-root.
//   - "ssh"  — the foreground program is an ssh client. The remote
//     user is invisible locally; Target is the ssh destination host.
//   - "user" — anything else; User is the login name of its euid.
type ForegroundUser struct {
	State  string // "user" | "root" | "ssh"
	User   string // login name of the foreground program's euid
	Target string // ssh destination host, when State == "ssh"
	// Agent is the coding-agent slug ("claude", "codex", …) when the
	// foreground program is one — tier T0 of docs/AGENT_TERM.md §2.
	// Empty for everything else, which is most things: a shell, vi, a
	// build. Independent of State (an agent can run as root).
	Agent string
}

// sshComms are the foreground program names treated as an outbound ssh
// session. autossh/sshpass are wrappers that exec ssh but can sit in
// the foreground long enough to be what the user means by "ssh'd out".
var sshComms = map[string]bool{
	"ssh": true, "mosh-client": true, "sshpass": true, "autossh": true,
}

// ForegroundUser inspects /proc for the PTY's foreground process group
// and classifies it. Everything is best-effort: an unreadable /proc
// entry degrades to State "user" with an empty name rather than
// failing. No syscalls on the pty fd — the foreground pgrp is read
// from the shell's /proc/<pid>/stat tpgid field, so the live io.Copy
// loop on the master is never disturbed.
func (s *Session) ForegroundUser() ForegroundUser {
	out := ForegroundUser{State: "user"}
	if s.cmd == nil || s.cmd.Process == nil {
		return out
	}
	shellPid := s.cmd.Process.Pid
	// The foreground pgrp leader's pid == the pgid TIOCGPGRP would
	// return; reading it from the shell's tpgid avoids touching the fd.
	fg := foregroundPgrp(shellPid)
	if fg <= 0 {
		fg = shellPid
	}
	euid := procEUID(fg)
	out.User = userName(euid)
	comm := procComm(fg)
	switch {
	case euid == 0:
		out.State = "root"
	case sshComms[comm]:
		out.State = "ssh"
		out.Target = sshTarget(fg)
	}
	// T0 agent detection rides the same /proc read: argv is only
	// fetched for the handful of interpreter comms that need it.
	if _, ok := agentComms[comm]; ok {
		out.Agent = agentComms[comm]
	} else if runtimeComms[comm] {
		out.Agent = matchAgent(comm, procCmdline(fg))
	}
	return out
}

// procCmdline reads /proc/<pid>/cmdline as argv (NUL-separated).
func procCmdline(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
}

// foregroundPgrp returns the foreground process group of pid's
// controlling terminal — field 8 (tpgid) of /proc/<pid>/stat. The comm
// field (2) is parenthesized and may itself contain spaces and ')', so
// we slice past the LAST ')' before splitting; after it the fields are
// state ppid pgrp session tty_nr tpgid …, making tpgid index 5.
func foregroundPgrp(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1
	}
	rp := strings.LastIndexByte(string(data), ')')
	if rp < 0 {
		return -1
	}
	f := strings.Fields(string(data)[rp+1:])
	if len(f) < 6 {
		return -1
	}
	n, err := strconv.Atoi(f[5])
	if err != nil {
		return -1
	}
	return n
}

// procEUID reads the effective uid (2nd field of the Uid: line) from
// /proc/<pid>/status. Returns -1 when unavailable. The Uid: line stays
// world-readable even for a setuid-root child (sudo), so the root case
// is detected without privilege.
func procEUID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 3 {
			if n, err := strconv.Atoi(f[2]); err == nil {
				return n
			}
		}
		break
	}
	return -1
}

// procComm reads /proc/<pid>/comm (the program's basename).
func procComm(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// userName resolves uid → login name, falling back to the numeric uid
// (and "" for an unknown/negative uid).
func userName(uid int) string {
	if uid < 0 {
		return ""
	}
	if u, err := osuser.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}

// sshTarget extracts the destination host from an ssh client's
// /proc/<pid>/cmdline (NUL-separated argv). It skips option flags and
// the values of the single-letter flags that take one, returns the
// first positional argument, and strips a leading user@. Best-effort:
// an exotic invocation just yields "".
func sshTarget(pid int) string {
	return parseSSHTarget(procCmdline(pid))
}

// parseSSHTarget pulls the destination host out of an ssh argv: skip
// option flags (and the values of single-letter flags that take one),
// return the first positional argument, and strip a leading user@.
// Pure so it can be unit-tested without a live process.
func parseSSHTarget(args []string) string {
	if len(args) < 2 {
		return ""
	}
	// ssh(1) single-letter options that consume the following argv.
	const valFlags = "bcDEeFIiJLlmOopQRSWw"
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			// "-p 2222" consumes the next arg; "-p2222" (value attached)
			// and long/no-value flags consume nothing extra.
			if len(a) == 2 && strings.ContainsRune(valFlags, rune(a[1])) {
				i++
			}
			continue
		}
		host := a
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		return host
	}
	return ""
}

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
	// Bulk class (docs/QOS.md): terminal output rides the credit / behind /
	// resync path and yields to interactive traffic, instead of sharing the
	// Interactive queue with window ops and other apps (REVIEW-DATAPATH F1).
	ch, err := conn.OpenChannelBulk(ctx, windowID)
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

	// pty → channel, teed through the OSC 7770 agent scanner (inert
	// until someone calls SetAgentHandler).
	go func() {
		_, copyErr := io.Copy(ch, &agentTee{r: f, s: s})
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
