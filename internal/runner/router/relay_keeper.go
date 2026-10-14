package routerrun

// Detached relay router + its keeper (docs/REMOTE.md §2/§9, M2e).
//
// Host B's relay router used to be the foreground command of A's
// `ssh B wash-router --listen-raw …`, so an ssh blip killed it and every
// app it had spawned. With --detach the ssh command is only a KEEPER: it
// finds the router already serving the (A-chosen, stable) relay socket or
// starts one in its own session, prints the "listening on" readiness line
// A greps for, and then just holds a connection to the router's keep
// socket. The router counts that hold as attached (Router.HoldOpen), so it
// is not reaped while A's session is up even with no browser looking.
//
// When ssh drops, the keeper sees stdin EOF and exits; the router stays,
// and its idle reaper (--idle-timeout) becomes the linger: A re-dials with
// the same socket path, a new keeper reuses the router, and B's apps are
// still there. A deliberate disconnect sends "stop" down stdin, which the
// keeper relays so the router exits now instead of lingering.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/router"
)

const (
	// keepSuffix / lockSuffix / logSuffix name the relay router's sibling
	// files next to its --listen-raw socket.
	keepSuffix = ".keep"
	lockSuffix = ".lock"
	logSuffix  = ".log"
)

var (
	// keeperReadyTimeout bounds how long a keeper waits for a router it
	// just spawned to bind its sockets.
	keeperReadyTimeout = 15 * time.Second
	// keeperStopWait bounds how long a keeper waits, after relaying
	// "stop", for the router to drop the keep connection (its exit).
	keeperStopWait = 5 * time.Second
	// errForeignRelay: a relay path exists but belongs to another uid.
	// /tmp is shared, so a squatter could pre-bind the predictable name;
	// we refuse rather than hand our session to their socket.
	errForeignRelay = errors.New("relay socket is owned by another user")
)

// serveKeepSocket binds path (0600) and holds the router open for every
// connected keeper. A "stop" line from a keeper calls stop (the raw
// listener's cancel). Each keeper connection is closed when ctx ends, which
// is how a keeper learns the router exited. Returns once bound.
func serveKeepSocket(ctx context.Context, r *router.Router, path string, stop func(), logf func(string, ...any)) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod keep socket: %w", err)
	}
	context.AfterFunc(ctx, func() {
		_ = ln.Close()
		_ = os.Remove(path)
		// The lock only serialises keepers starting THIS router; a later
		// one starts afresh, so don't leave it behind in /tmp.
		_ = os.Remove(strings.TrimSuffix(path, keepSuffix) + lockSuffix)
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go holdForKeeper(ctx, r, c, stop, logf)
		}
	}()
	return nil
}

func holdForKeeper(ctx context.Context, r *router.Router, c net.Conn, stop func(), logf func(string, ...any)) {
	release := r.HoldOpen()
	unwatch := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer func() {
		unwatch()
		_ = c.Close()
		release()
	}()
	logf("relay keeper attached")
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "stop" {
			logf("relay keeper asked to stop — shutting down")
			stop()
		}
	}
	logf("relay keeper gone — idle reaper owns this router's lifetime now")
}

// runKeeper is `wash-router --detach`: ensure the relay router for rawPath
// is up (reusing a live one), announce readiness on stdout, then hold it
// until stdin says otherwise. Exit 0 on stdin EOF/stop, 1 if the router
// died under us (so A's ssh ends and the supervisor re-dials).
func runKeeper(rawPath string, childArgv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	conn, reused, err := ensureRelayRouter(rawPath, func() (<-chan struct{}, error) {
		return spawnDetached(childArgv)
	})
	if err != nil {
		fmt.Fprintf(stderr, "wash-router: relay %s: %v\n", rawPath, err)
		return 1
	}
	defer conn.Close()
	how := "started"
	if reused {
		how = "reused"
	}
	// The readiness marker com.wash.remote greps for (supervisor.go) —
	// the detached router's own log goes to <rawPath>.log, not to ssh.
	fmt.Fprintf(stdout, "listening on unix://%s (detached relay router, %s)\n", rawPath, how)
	return keepUntil(conn, stdin, stderr)
}

// keepUntil blocks holding conn (a keep-socket connection) until stdin
// closes (0: the ssh session is gone, leave the router to linger), stdin
// says "stop" (0: relay it and wait for the router to go), or the router
// drops conn (1: it exited).
func keepUntil(conn net.Conn, stdin io.Reader, stderr io.Writer) int {
	gone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(gone)
	}()
	lines := make(chan string, 1)
	eof := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdin)
		for sc.Scan() {
			lines <- strings.TrimSpace(sc.Text())
		}
		close(eof)
	}()
	for {
		select {
		case <-gone:
			fmt.Fprintln(stderr, "wash-router: relay router exited")
			return 1
		case <-eof:
			return 0
		case l := <-lines:
			if l != "stop" {
				continue
			}
			_, _ = io.WriteString(conn, "stop\n")
			select {
			case <-gone:
			case <-time.After(keeperStopWait):
			}
			return 0
		}
	}
}

// ensureRelayRouter returns a keep connection to the relay router serving
// rawPath, starting one via spawn if none answers. reused reports whether
// it was already running. Starts are serialised by a flock on a sibling
// lock file so two keepers racing (A's re-dial while B's sshd still holds
// the old session) cannot both spawn — the second router would unlink the
// first's live socket.
func ensureRelayRouter(rawPath string, spawn func() (exited <-chan struct{}, err error)) (conn net.Conn, reused bool, err error) {
	if c, err := dialOwnRelay(rawPath); err == nil || errors.Is(err, errForeignRelay) {
		return c, err == nil, err
	}
	lock, err := os.OpenFile(rawPath+lockSuffix, os.O_RDONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("lock: %w", err)
	}
	defer lock.Close() // releases the flock
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, false, fmt.Errorf("flock: %w", err)
	}
	// Another keeper may have started it while we waited for the lock.
	if c, err := dialOwnRelay(rawPath); err == nil || errors.Is(err, errForeignRelay) {
		return c, err == nil, err
	}
	exited, err := spawn()
	if err != nil {
		return nil, false, fmt.Errorf("start relay router: %w", err)
	}
	deadline := time.Now().Add(keeperReadyTimeout)
	for {
		c, err := dialOwnRelay(rawPath)
		if err == nil || errors.Is(err, errForeignRelay) {
			return c, false, err
		}
		if time.Now().After(deadline) {
			return nil, false, fmt.Errorf("relay router not ready after %s (log: %s)", keeperReadyTimeout, rawPath+logSuffix)
		}
		select {
		case <-exited:
			return nil, false, fmt.Errorf("relay router exited during start-up (log: %s)", rawPath+logSuffix)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// dialOwnRelay connects to rawPath's keep socket, provided both it and the
// relay socket belong to this uid (file owner AND the listener's peer
// credentials). Any other failure means "no router here yet".
func dialOwnRelay(rawPath string) (net.Conn, error) {
	uid := uint32(os.Getuid())
	for _, p := range []string{rawPath, rawPath + keepSuffix} {
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != uid {
			return nil, fmt.Errorf("%w: %s (uid %d)", errForeignRelay, p, st.Uid)
		}
	}
	c, err := net.DialTimeout("unix", rawPath+keepSuffix, 2*time.Second)
	if err != nil {
		return nil, err
	}
	if puid, ok := peerUID(c); ok && puid != uid {
		_ = c.Close()
		return nil, fmt.Errorf("%w: keep socket served by uid %d", errForeignRelay, puid)
	}
	return c, nil
}

func peerUID(c net.Conn) (uint32, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || cerr != nil {
		return 0, false
	}
	return cred.Uid, true
}

// spawnDetached starts the relay router in its own session (setsid: no
// controlling tty, not in the ssh session's process group), stdio on
// /dev/null — it logs via the --log-file detachedArgv adds. exited closes
// when it does.
func spawnDetached(argv []string) (<-chan struct{}, error) {
	// /proc/self/exe re-runs exactly this binary; argv[0] is kept so the
	// multi-call `wash` dispatch lands back in the router.
	cmd := &exec.Cmd{
		Path:        "/proc/self/exe",
		Args:        argv,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	return exited, nil
}

// detachedArgv is the detached router's argv: this invocation minus
// --detach, plus a --log-file next to the socket unless one was given.
// args is Run's slice; whatever of os.Args precedes it (`wash-router`, or
// the multi-call `wash router`) is kept verbatim so the re-exec dispatches
// back into Run.
func detachedArgv(args []string, rawPath string) []string {
	n := len(os.Args) - len(args)
	if n < 1 {
		n = 1
	}
	out := append([]string(nil), os.Args[:n]...)
	hasLog := false
	for _, a := range args {
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") {
			switch name {
			case "detach":
				continue
			case "log-file":
				hasLog = true
			}
		}
		out = append(out, a)
	}
	if !hasLog {
		out = append(out, "--log-file", rawPath+logSuffix)
	}
	return out
}
