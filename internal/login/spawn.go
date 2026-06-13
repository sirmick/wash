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
	"path/filepath"
	"strconv"
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

	// Lay out the per-uid run directory + sessions subdir.
	//
	// When the wash group exists (production installs), the
	// sessions/ dir gets mode 02750 with group=wash. The setgid bit
	// makes child sockets inherit the group automatically — the
	// router doesn't need to know about groups, and wash-login (in
	// group wash) can dial the per-user sockets. Single-user / dev
	// installs without the wash group fall back to 0700 owner-only;
	// wash-login runs as the same user in that mode so peer-cred
	// + ownership both pass.
	uidDir := filepath.Join(s.runRoot(), strconv.FormatUint(uint64(id.UID), 10))
	sessionsDir := filepath.Join(uidDir, "sessions")
	sessionsMode := os.FileMode(0o700)
	uidDirMode := os.FileMode(0o700)
	washGID, gerr := LookupGroupGID(WashGroupName)
	if gerr == nil {
		sessionsMode = 0o2750 // setgid bit so socket children inherit group
		uidDirMode = 0o750
	}
	if err := os.MkdirAll(uidDir, uidDirMode); err != nil {
		return Session{}, fmt.Errorf("mkdir %s: %w", uidDir, err)
	}
	if err := os.MkdirAll(sessionsDir, sessionsMode); err != nil {
		return Session{}, fmt.Errorf("mkdir %s: %w", sessionsDir, err)
	}
	// Apply the mode explicitly: MkdirAll honours umask, which on
	// most systems strips group bits we want here.
	_ = os.Chmod(uidDir, uidDirMode)
	_ = os.Chmod(sessionsDir, sessionsMode)
	// Ownership: when wash-login isn't the target user, the dirs
	// need to be owned by the target user (so the spawned router can
	// write under them) AND group=wash (so wash-login can enter).
	if uint32(os.Geteuid()) != id.UID {
		group := int(id.GID)
		if gerr == nil {
			group = int(washGID)
		}
		_ = os.Chown(uidDir, int(id.UID), group)
		_ = os.Chown(sessionsDir, int(id.UID), group)
	}

	// Per-uid flock around the spawn so concurrent /ws hits don't
	// both decide to spawn. The lock is held only for the spawn
	// itself; once the ctl socket exists, the lock can be dropped
	// and follow-up attaches go through normally.
	lockPath := filepath.Join(uidDir, "spawn.lock")
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
	args := []string{
		"--listen-unix", sock,
		"--name", name,
		"--allow-uid", strconv.FormatUint(uint64(s.AllowUID), 10),
	}
	if s.AppsDir != "" {
		args = append(args, "--apps-dir", s.AppsDir)
	}
	if s.IdleTimeout > 0 {
		args = append(args, "--idle-timeout", s.IdleTimeout.String())
	}

	cmd := exec.Command(bin, args...)
	// Don't inherit wash-login's stdout/stderr by default — for dev
	// it's useful, for production noisy. Punt: forward to a logger
	// in a follow-up. For now: pipe to /dev/null so the spawn
	// doesn't pollute wash-login's stderr.
	cmd.Stdout = nil
	cmd.Stderr = nil

	// Setuid only when target differs from self. Avoids the
	// "Operation not permitted" surface in dev where wash-login
	// runs unprivileged and target == self.
	if uint32(os.Geteuid()) != id.UID {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{
				Uid: id.UID,
				Gid: id.GID,
			},
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
