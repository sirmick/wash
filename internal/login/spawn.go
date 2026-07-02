package login

// Spawn per-user wash-router processes.
//
// Per docs/MULTIUSER.md: wash-login is the supervisor. fork →
// setuid → exec wash-router with the right argv. Per-uid flock at
// /run/wash/<uid>/spawn.lock prevents concurrent-first-spawn races
// (two browser tabs landing on /ws at the same instant for a uid
// that has no sessions yet — without locking each would spawn a
// router).
//
// Setuid is skipped when target uid == wash-login's own uid (dev /
// CI / personal-box deployments). Production wash-login runs as a
// dedicated wash-system uid with CAP_SETUID and spawns user routers
// under their actual uid/gid.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Spawner forks new wash-router processes. Construct one per
// wash-login instance; safe for concurrent use across goroutines.
type Spawner struct {
	// RouterBinary is the path to wash-router. If empty, "wash-router"
	// is resolved via PATH; production installs should pass an
	// absolute path.
	RouterBinary string

	// AllowUID is the uid the spawned router will accept SCM_RIGHTS
	// handoffs from (via SO_PEERCRED). Always wash-login's own uid.
	AllowUID uint32

	// RunRoot overrides the /run/wash root. Empty means "/run/wash".
	// Tests / non-FHS deployments override.
	RunRoot string

	// AppsDir is forwarded to the spawned router as --apps-dir.
	// Empty means "let the router default to the dir of its own
	// binary," which is correct for both dev (out/) and production
	// installs that colocate wash-router + per-app binaries.
	AppsDir string

	// IdleTimeout is forwarded to the spawned router as
	// --idle-timeout. Zero forwards no flag (router default 30m).
	IdleTimeout time.Duration

	// SocketWaitTimeout bounds how long Spawn waits for the
	// child's ctl socket to appear before declaring failure.
	// Zero defaults to 2 seconds.
	SocketWaitTimeout time.Duration

	// SocketPollInterval is the polling cadence while waiting for
	// the ctl socket. Zero defaults to 25ms.
	SocketPollInterval time.Duration

	// MaxPerUID caps concurrent live sessions per uid. Zero
	// disables the cap (unbounded). Spawn returns ErrSessionCap
	// when the cap is hit; the picker surfaces this as
	// "End an existing session first." Embedded deployments
	// typically set this to 1 to lock down to single-session.
	MaxPerUID int

	// Sessions is consulted for the live-session count when
	// MaxPerUID > 0. Optional; nil disables cap enforcement
	// regardless of MaxPerUID.
	Sessions SessionRegistry
}

// ErrSessionCap is returned by Spawn when the per-uid live-session
// cap would be exceeded.
var ErrSessionCap = fmt.Errorf("session cap reached for this user")

// Spawn forks a new wash-router process for (id.UID, name). Returns
// the resulting Session — including the running pid and the ctl
// socket path — once the socket is accepting connections, or an
// error if anything along the path fails.
//
// On success the caller owns the running process: wash-login does
// not waitpid it (the router self-exits on idle, and SIGCHLD goes
// to PID 1 if wash-login restarts). The Session's Pid is for
// SIGTERM / /proc lookup purposes only.
func (s *Spawner) Spawn(id Identity, name string) (Session, error) {
	if name == "" {
		name = id.Name
	}
	if s.MaxPerUID > 0 && s.Sessions != nil {
		existing, err := s.Sessions.List(id.UID)
		if err == nil && len(existing) >= s.MaxPerUID {
			return Session{}, ErrSessionCap
		}
	}
	sessid, err := generateSessID()
	if err != nil {
		return Session{}, fmt.Errorf("generate sessid: %w", err)
	}

	// Per-uid run directory + sessions subdir layout.
	//
	// These are created by the spawned router IN THE TARGET UID's setuid
	// context (the router MkdirAll's its --listen-unix parent and --log-file
	// parent as itself), NOT here. wash-login runs as the unprivileged
	// wash-system user without CAP_CHOWN, so it cannot create a dir and hand
	// it to another uid — an os.Chown to the target would fail silently and
	// leave the dir wash-system-owned and unwritable by the router. Instead:
	//
	//   - The runtime root (/run/wash) is wash-system:wash mode 2770 (setgid).
	//     The setgid bit propagates group=wash AND the setgid bit down to every
	//     dir the router MkdirAll's under it, so the per-uid dir, sessions/, and
	//     the ctl socket all land group=wash automatically — wash-login (in
	//     group wash) can dial them, no chown anywhere.
	//   - The router is granted group wash as a SUPPLEMENTARY group (below), so
	//     it can create its dirs under the group-writable root while its primary
	//     gid stays the user's own (user files keep the user's group).
	//   - Squat-safety: regular users aren't in group wash, so they cannot write
	//     /run/wash and pre-create /run/wash/<other-uid>; only routers wash-login
	//     grants group wash (via CAP_SETGID) can.
	//
	// Dev / single-user (target uid == wash-login's own uid, typically no wash
	// group): no setuid, the router runs as the same user, and the run root is a
	// user-owned $XDG_RUNTIME_DIR/wash or /tmp/wash-<uid> the router writes to
	// directly.
	uidDir := filepath.Join(s.runRoot(), strconv.FormatUint(uint64(id.UID), 10))
	sessionsDir := filepath.Join(uidDir, "sessions")
	washGID, gerr := LookupGroupGID(WashGroupName)
	if err := s.ensureRunRoot(gerr == nil, washGID); err != nil {
		return Session{}, err
	}

	// Per-uid flock around the spawn so concurrent /ws hits don't both decide
	// to spawn. It lives directly under the run root (which wash-login owns /
	// can write), NOT under the per-uid dir — that dir is created later by the
	// target-uid router, so it doesn't exist yet at lock time. The lock is held
	// only for the spawn itself; once the ctl socket exists it's dropped and
	// follow-up attaches go through normally.
	lockPath := filepath.Join(s.runRoot(), fmt.Sprintf("spawn-%d.lock", id.UID))
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Session{}, fmt.Errorf("open spawn lock: %w", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return Session{}, fmt.Errorf("flock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	sock := filepath.Join(sessionsDir, sessid+".sock")

	bin := s.RouterBinary
	if bin == "" {
		bin = "wash-router"
	}
	// Per-session router log. The router opens it itself (--log-file) under its
	// own uid, so it lands owned by the target user (readable without
	// privilege) and its parent dir is created in the setuid context alongside
	// the socket dir — wash-login never touches it (it couldn't chown it
	// anyway). A failure to open is best-effort inside the router (output then
	// falls back to the inherited fds → wash-login's journal).
	logPath := filepath.Join(uidDir, "router-"+sessid+".log")
	args := []string{
		"--listen-unix", sock,
		"--name", name,
		"--allow-uid", strconv.FormatUint(uint64(s.AllowUID), 10),
		"--log-file", logPath,
	}
	if s.AppsDir != "" {
		args = append(args, "--apps-dir", s.AppsDir)
	}
	if s.IdleTimeout > 0 {
		args = append(args, "--idle-timeout", s.IdleTimeout.String())
	}

	cmd := exec.Command(bin, args...)
	// Until the router opens its --log-file, its earliest startup output
	// inherits wash-login's stderr (→ journald under systemd) rather than
	// /dev/null, so a crash-before-logging is still diagnosable.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// Re-point HOME/USER/LOGNAME at the authed user. wash-login runs as
	// wash-system (HOME=/var/lib/wash); without this the router — though
	// spawned under the target uid below — inherits wash-system's HOME and
	// resolves ~/.config/wash, ~/.cache, and the fm/edit start dir to a path
	// the user can't write (perm denied; wash-vscode's code-server download
	// was the visible failure). Mirrors the vmlogin launcher's childEnv.
	cmd.Env = childEnv(id, s.runRoot())

	// Setuid only when target differs from self. Avoids the
	// "Operation not permitted" surface in dev where wash-login
	// runs unprivileged and target == self.
	if uint32(os.Geteuid()) != id.UID {
		cred := &syscall.Credential{
			Uid: id.UID,
			Gid: id.GID,
		}
		// Grant group wash as a supplementary group (not the primary gid, so
		// the user's own files keep the user's group) so the router can create
		// its dirs under the group-writable, setgid /run/wash and dial nothing
		// — the setgid bit lands everything group=wash for wash-login to reach.
		// Requires CAP_SETGID, which wash-system has.
		if gerr == nil {
			cred.Groups = []uint32{washGID}
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: cred,
			// Detach from wash-login's controlling tty / session so
			// signals to wash-login don't fan out to all child
			// routers. Each router is its own session leader.
			Setsid: true,
		}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}

	if err := cmd.Start(); err != nil {
		return Session{}, fmt.Errorf("start wash-router bin=%s uid=%d gid=%d: %w", bin, id.UID, id.GID, err)
	}
	pid := cmd.Process.Pid

	// Run cmd.Wait() in the background so the kernel auto-reaps
	// the child when it exits. Without this, on SIGTERM the router
	// exits but persists as a Z (zombie) in /proc until wash-login
	// terminates — eating a slot in the process table, confusing
	// ps/top, and breaking liveness probes that count zombies as
	// alive. The goroutine doesn't need to do anything with the
	// exit status; running .Wait() to completion is the reaping.
	go func() { _ = cmd.Wait() }()

	// Poll for the ctl socket to appear. If it never does, the
	// child probably crashed; clean up.
	wait := s.SocketWaitTimeout
	if wait == 0 {
		wait = 2 * time.Second
	}
	poll := s.SocketPollInterval
	if poll == 0 {
		poll = 25 * time.Millisecond
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		fi, err := os.Stat(sock)
		if err == nil && fi.Mode()&os.ModeSocket != 0 {
			return Session{
				Pid:    pid,
				UID:    id.UID,
				SessID: sessid,
				Name:   name,
				Sock:   sock,
			}, nil
		}
		// Detect early child exit so we don't burn the full timeout
		// waiting for a socket that will never exist.
		if !processAlive(pid) {
			return Session{}, fmt.Errorf("wash-router (pid %d) exited before ctl socket appeared", pid)
		}
		time.Sleep(poll)
	}
	// Timed out; best-effort kill the child and surface the failure.
	_ = syscall.Kill(pid, syscall.SIGTERM)
	return Session{}, fmt.Errorf("wash-router (pid %d) did not bind %s within %s", pid, sock, wait)
}

// runRoot returns the /run/wash root, honouring an override for tests.
func (s *Spawner) runRoot() string {
	if s.RunRoot != "" {
		return s.RunRoot
	}
	return "/run/wash"
}

// ensureRunRoot makes the runtime root exist with the perms the target-uid
// routers need to create their per-uid dirs beneath it.
//
// Production (wash group present): mode 2770 group=wash. Group-writable so a
// router granted supplementary group wash can mkdir /run/wash/<uid>; setgid so
// that dir, sessions/, and the ctl socket all inherit group=wash for wash-login
// to reach. The owner stays whoever we run as (wash-system). In a packaged
// install the dir is provisioned first by systemd RuntimeDirectory / the OpenRC
// initd, so MkdirAll is a no-op and we just normalise the mode; the chmod/chown
// are best-effort (we may not own a foreign-provisioned root). chgrp to wash
// needs only group membership (we're in group wash), not CAP_CHOWN.
//
// Dev / single-user (no wash group): a plain 0700 owner dir — the router runs
// as the same user and writes it directly.
func (s *Spawner) ensureRunRoot(hasWashGroup bool, washGID uint32) error {
	root := s.runRoot()
	// NB: Go encodes setgid as os.ModeSetgid (a high bit), NOT the octal 0o2000
	// — passing a literal 0o2770 to Chmod/Mkdir silently sets plain 0770 and
	// drops setgid. Use os.ModeSetgid|0o770 so the bit actually lands; without
	// it the per-uid dirs + ctl socket don't inherit group wash and wash-login
	// can't dial them.
	mode := os.FileMode(0o700)
	if hasWashGroup {
		mode = os.ModeSetgid | 0o770
	}
	if err := os.MkdirAll(root, mode); err != nil {
		return fmt.Errorf("mkdir run-root %s: %w", root, err)
	}
	if hasWashGroup {
		_ = os.Chmod(root, os.ModeSetgid|0o770) // umask strips setgid; a pre-existing dir keeps its mode
		_ = os.Chown(root, -1, int(washGID))
	}
	return nil
}

// generateSessID returns "s-XXXXXXXX" with 8 random hex chars
// (32 bits of entropy — collision odds negligible at any plausible
// concurrent-session count).
func generateSessID() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "s-" + hex.EncodeToString(buf[:]), nil
}

// childEnv is wash-login's environment with HOME/USER/LOGNAME re-pointed at the
// authed user, for the spawned wash-router. wash-login runs as wash-system, so
// its os.Environ() carries HOME=/var/lib/wash; the router is setuid to the
// target uid but env is NOT uid-derived, so without this it would resolve ~ to
// wash-system's home — unwritable by the user. The home comes from the user's
// passwd entry, falling back to /home/<name>. Mirrors vmlogin's childEnv.
func childEnv(id Identity, runRoot string) []string {
	home := ""
	if u, err := user.Lookup(id.Name); err == nil && u.HomeDir != "" {
		home = u.HomeDir
	} else if id.Name != "" {
		home = "/home/" + id.Name
	}
	// Per-uid XDG_RUNTIME_DIR. wash-login runs as wash-system, so its own
	// XDG_RUNTIME_DIR (or none, under a system service) would otherwise pass
	// through to the setuid router and every user's wash-display would try to
	// bind its wayland socket in wash-system's runtime dir — or, unset, fail to
	// start at all (libwayland requires XDG_RUNTIME_DIR), so there's no display
	// layer behind wash-login, and a shared dir lets sessions dial each other's
	// compositors (REVIEW-X11-WAYLAND #4). Point it at a per-uid path; the
	// router (running AS the target uid) creates it 0700-owned-by-user in its
	// setuid context, the same way it creates the sessions dir.
	xdgRuntime := PerUserRuntimeDir(runRoot, id.UID)
	parent := os.Environ()
	env := make([]string, 0, len(parent)+4)
	for _, kv := range parent {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "USER=") ||
			strings.HasPrefix(kv, "LOGNAME=") || strings.HasPrefix(kv, "XDG_RUNTIME_DIR=") {
			continue // replaced below with the authed user's values
		}
		env = append(env, kv)
	}
	if home != "" {
		env = append(env, "HOME="+home)
	}
	if id.Name != "" {
		env = append(env, "USER="+id.Name, "LOGNAME="+id.Name)
	}
	env = append(env, "XDG_RUNTIME_DIR="+xdgRuntime)
	return env
}

// PerUserRuntimeDir is the per-uid XDG_RUNTIME_DIR wash-login points a spawned
// router at: <runRoot>/<uid>/xdg. The router creates it 0700 as the target uid
// (see ensureXDGRuntimeDir), so it satisfies libwayland's same-uid 0700
// requirement and isolates each user's wayland/compositor sockets.
func PerUserRuntimeDir(runRoot string, uid uint32) string {
	return filepath.Join(runRoot, strconv.FormatUint(uint64(uid), 10), "xdg")
}

// processAlive reports whether pid exists in the process table.
// Used to short-circuit the socket-wait loop when the child has
// already exited.
func processAlive(pid int) bool {
	// kill(pid, 0) returns nil if the process exists and we can
	// signal it; ESRCH if it doesn't; EPERM if it exists but we
	// can't. EPERM still means "alive" for our purposes.
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return !errors.Is(err, syscall.ESRCH)
}
