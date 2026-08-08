package remote

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/sdk"
)

// remoteSockPath returns a unique B-side socket path for one connection.
// On B the raw router binds it and chmods it 0600 (uid-only); a per-
// connection name avoids collisions between concurrent sessions.
func remoteSockPath() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "/tmp/wash-relay-" + hex.EncodeToString(b[:]) + ".sock"
}

// supervisor owns one ssh process per host and mirrors their state into
// the StateService the FE subscribes to. It also registers each host's
// local relay socket with A's router (so a browser can attach it).
type supervisor struct {
	// svc is the published-state mutator. Kept as the narrow stateMutator
	// interface (like the discoverer) so a test can inject a fake that
	// reproduces StateService's shallow-snapshot-then-marshal and prove the
	// mutators below are copy-on-write safe.
	svc     stateMutator
	conn    *sdk.Conn // for RegisterPeer/UnregisterPeer with A's router
	sshPath string    // injectable for tests
	sockDir string    // dir holding per-host ssh -L unix sockets

	mu    sync.Mutex
	procs map[string]context.CancelFunc
}

func newSupervisor(svc *sdk.StateService[State], conn *sdk.Conn) *supervisor {
	// Per-instance dir for the ssh -L unix sockets. Same user as A's
	// router (this is its child), so the router can dial them.
	dir, err := os.MkdirTemp("", "wash-remote-")
	if err != nil {
		dir = os.TempDir()
	}
	return &supervisor{
		svc:     svc,
		conn:    conn,
		sshPath: "ssh",
		sockDir: dir,
		procs:   map[string]context.CancelFunc{},
	}
}

// sockPath returns this host's ssh -L unix socket path (deterministic per
// host, short enough to stay under the ~108-byte sun_path limit).
func (s *supervisor) sockPath(host string) string {
	sum := sha256.Sum256([]byte(host))
	return filepath.Join(s.sockDir, hex.EncodeToString(sum[:8])+".sock")
}

// ingressSockPath is the second forwarded socket for host: B's --listen-
// ingress HTTP endpoint (remote ingress, issue #15). Sibling of sockPath.
func (s *supervisor) ingressSockPath(host string) string {
	sum := sha256.Sum256([]byte(host))
	return filepath.Join(s.sockDir, hex.EncodeToString(sum[:8])+".i.sock")
}

// buildSSHArgs builds the argv for the bring-up ssh: forward a LOCAL UNIX
// SOCKET to host B's loopback router port, then run wash-router there in
// raw-wire mode (--listen-raw, no HTTP/WebSocket) bound to that port. A's
// router dials the local socket and splices a browser's muxed channel to
// it (the "one port" relay, docs/REMOTE.md) — nothing binds a TCP port on
// A's loopback, and B's router is reached solely through this tunnel, so
// SSH is the access boundary (§10).
//
// A second -L forwards B's --listen-ingress socket (remote ingress, §17 /
// issue #15): B's router serves its /app/ registry as plain HTTP there, and
// A's router proxies locally-unknown ingress tokens to it, so a remote
// app's iframe (vscode et al) loads through A's origin. Both forwards ride
// ONE ssh — one auth, one lifetime, one teardown.
func buildSSHArgs(host, localSock, remoteSock, localIngressSock, remoteIngressSock string, port int) []string {
	args := []string{
		"-o", "BatchMode=yes", // never block on an interactive prompt
		"-o", "ExitOnForwardFailure=yes", // fail fast if the -L bind can't be set up
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10", // fail fast on an unreachable host so the reconnect loop stays responsive (vs. a multi-minute TCP timeout)
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
	// A non-default SSH port (e.g. a discovered peer advertising 2222) goes
	// via -p; ssh's positional target can't carry "host:port".
	if port != 0 && port != defaultSSHPort {
		args = append(args, "-p", strconv.Itoa(port))
	}
	args = append(args,
		// Forward a local unix socket to a REMOTE UNIX SOCKET (not a loopback
		// TCP port): B's raw router chmods it 0600, so only the ssh'd uid on B
		// can reach it. A loopback TCP listener would be reachable by ANY
		// local user on B — a full unauthenticated wash session (docs/REMOTE.md
		// §10). The unix socket makes "reaching it requires the SSH session"
		// actually true.
		"-L", fmt.Sprintf("%s:%s", localSock, remoteSock),
		"-L", fmt.Sprintf("%s:%s", localIngressSock, remoteIngressSock),
		host,
		"wash-router",
		"--listen-raw", "unix:"+remoteSock,
		"--listen-ingress", "unix:"+remoteIngressSock,
		"--no-session",
		"--no-auth",
		// A UNIQUE control socket (not the default /tmp/wash-<uid>.sock, which
		// would collide with B's own desktop router). It's required: a spawned
		// app dials it (WASH_DISPLAY) to attach back to the router, so launches
		// on B fail without one.
		"--control-socket", remoteSock+".ctl",
	)
	return args
}

// connect brings up (or no-ops if already up) a connection to host. host is
// the connect identity AND the preferred ssh dial target — passing a NAME (not
// a pre-resolved IP) is deliberate: ssh then consults the user's ~/.ssh/config
// (IdentityFile, HostName, User) exactly as `ssh <name>` would. addr is the
// mDNS-announced IP, kept only as a fallback for a plain LAN where the name
// doesn't resolve. port is the SSH port (0 == default 22); a discovered peer
// advertising a non-default port carries it here so the dial actually reaches it.
func (s *supervisor) connect(host, addr string, port int) {
	if host == "" {
		host = addr
	}
	if host == "" {
		return
	}
	s.mu.Lock()
	if _, ok := s.procs[host]; ok {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.procs[host] = cancel
	s.mu.Unlock()

	s.setHost(host, HostState{Host: host, Origin: host, Status: StatusStarting})
	go s.run(ctx, host, addr, port)
}

// shutdown cancels every live host connection's context, killing its ssh
// tunnel (and, with it, host B's --listen-raw router, which has no idle
// timeout). Registered on sdk.OnTerminate so a router SIGTERM (Settings
// restart, devreload) or conn-close teardown doesn't orphan the ssh
// processes to PID 1 — one live B-side session would otherwise accumulate
// per A-side restart (REVIEW-RECONNECT H5).
func (s *supervisor) shutdown() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.procs))
	for host, cancel := range s.procs {
		cancels = append(cancels, cancel)
		delete(s.procs, host)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// run owns one host's connection for its whole lifetime, auto-reconnecting
// across unexpected drops so a connected host is *solid* — a network blip or
// a momentary B-side restart heals itself (status → reconnecting → up) instead
// of going dead until the user clicks Reconnect. It stops only on a user
// disconnect (ctx cancel) or an auth failure (a key refusal won't fix itself
// on retry — the user must authenticate, which re-issues connect).
func (s *supervisor) run(ctx context.Context, host, addr string, port int) {
	sock := s.sockPath(host) // A side: in a 0700 temp dir; reused across attempts
	ingressSock := s.ingressSockPath(host)
	defer func() {
		s.mu.Lock()
		delete(s.procs, host)
		s.mu.Unlock()
		_ = os.Remove(sock)
		_ = os.Remove(ingressSock)
		if s.conn != nil {
			_ = s.conn.UnregisterPeer(host)
		}
	}()

	const (
		maxBackoff = 30 * time.Second
		// A connection up at least this long counts as healthy: a later drop
		// is a fresh incident, so backoff restarts from the floor. Only a fast
		// re-drop (a flap) keeps escalating — this is the throttle the SFTP-
		// mount audit (TODO-sftp-mount-bugs.md, "reconnect storm") calls for:
		// never reset backoff on a dial that didn't prove healthy.
		minHealthy = 30 * time.Second
	)
	backoff := time.Second
	// Prefer dialing the name (host) so ssh reads ~/.ssh/config; addr is the
	// mDNS-announced IP we fall back to once if the name never connects (a
	// plain LAN with no DNS / no config entry for it).
	dial := host
	canFallback := addr != "" && addr != host
	for {
		start := time.Now()
		code, msg := s.runOnce(ctx, host, dial, port, sock, ingressSock)
		if ctx.Err() != nil {
			return // user-initiated disconnect; disconnect() removed the host
		}
		healthy := time.Since(start) >= minHealthy
		if healthy {
			backoff = time.Second // the connection was healthy; a fresh drop reconnects fast
		}
		// The name never came up and we have an announced IP to try: it most
		// likely doesn't resolve here. Switch to the IP and retry immediately,
		// without burning a backoff on the unresolvable name. Auth failures are
		// handled below (a wrong/missing key won't be fixed by the IP).
		if code != "auth" && !healthy && canFallback && dial == host {
			dial = addr
			s.setHost(host, HostState{Host: host, Origin: host, Status: StatusStarting, Error: msg})
			continue
		}
		if code == "auth" {
			// A credential refusal won't change on retry — surface it (the FE
			// offers the ssh-add widget) and stop. Authenticate re-issues
			// connect, starting a fresh run.
			hs := HostState{Host: host, Origin: host, Status: StatusDown, Code: "auth", Error: msg}
			if hs.Error == "" {
				hs.Error = "ssh authentication failed"
			}
			s.setHost(host, hs)
			return
		}
		// Unexpected drop. Detach the dead peer so the FE drops its now-stale
		// remote windows, surface "reconnecting…", then retry after a capped
		// backoff. The host stays in s.procs throughout, so a duplicate
		// connect() remains a no-op while we recover.
		if s.conn != nil {
			_ = s.conn.UnregisterPeer(host)
		}
		s.setHost(host, HostState{Host: host, Origin: host, Status: StatusReconnecting, Error: msg})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// runOnce performs one ssh bring-up attempt: forward the relay socket, start
// B's raw router, register the peer with A's router when B reports ready, and
// block until the ssh process exits. Returns ("auth", msg) for a credential
// refusal, ("", msg) for any other drop, or ("", "") when ctx was cancelled
// (user disconnect). The caller decides whether to reconnect.
// dial is the ssh target for this attempt — the name (so ssh reads
// ~/.ssh/config) or, on fallback, the announced IP. host stays the connect
// identity used for peer registration and published state.
func (s *supervisor) runOnce(ctx context.Context, host, dial string, port int, sock, ingressSock string) (code, errMsg string) {
	remoteSock := remoteSockPath() // B side: unique per attempt, chmod 0600 there
	remoteIngressSock := remoteSock + ".i"

	// ssh -L refuses to bind a unix socket whose file already exists.
	_ = os.Remove(sock)
	_ = os.Remove(ingressSock)

	cmd := exec.CommandContext(ctx, s.sshPath, buildSSHArgs(dial, sock, remoteSock, ingressSock, remoteIngressSock, port)...)
	// Backstop the ctx cancel: if wash-remote dies abruptly (SIGKILL, panic)
	// before OnTerminate/ctx can reap it, Pdeathsig has the kernel SIGTERM the
	// ssh — and with it B's --listen-raw router — instead of orphaning the
	// tunnel to PID 1 (REVIEW-RECONNECT H5).
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	log.Printf("wash-remote: connect host=%s dial=%s port=%d local=%s remote=%s", host, dial, port, sock, remoteSock)
	if err := cmd.Start(); err != nil {
		msg := formatSSHAttemptError(dial, port, fmt.Errorf("start: %w", err), nil, false)
		log.Printf("wash-remote: connect host=%s dial=%s start failed: %s", host, dial, msg)
		return "", msg
	}

	up := make(chan struct{}, 1)
	// tail accumulates the last few stderr lines so an exit-with-error
	// can be classified (auth refusal vs. anything else). Guarded — the
	// two scan goroutines write it concurrently.
	var tailMu sync.Mutex
	var tail []string
	ready := false
	// The remote wash-router logs "listening on <addr>" once bound; ssh
	// forwards that to our stdout/stderr. That line is our readiness
	// signal — tunnel + router are live, the shell can attach.
	scan := func(stream string, r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			log.Printf("wash-remote: connect host=%s dial=%s %s: %s", host, dial, stream, line)
			if strings.Contains(line, "listening on ") {
				tailMu.Lock()
				ready = true
				tailMu.Unlock()
				select {
				case up <- struct{}{}:
				default:
				}
				continue
			}
			tailMu.Lock()
			tail = append(tail, stream+": "+line)
			if len(tail) > 10 {
				tail = tail[len(tail)-10:]
			}
			tailMu.Unlock()
		}
		if err := sc.Err(); err != nil {
			tailMu.Lock()
			tail = append(tail, stream+" read: "+err.Error())
			if len(tail) > 10 {
				tail = tail[len(tail)-10:]
			}
			tailMu.Unlock()
			log.Printf("wash-remote: connect host=%s dial=%s %s scan: %v", host, dial, stream, err)
		}
	}
	go scan("stdout", stdout)
	go scan("stderr", stderr)

	// The up-watcher must not outlive this attempt, or a late "up" from a
	// dying connection could mark the host up after we've moved on.
	upCtx, upCancel := context.WithCancel(ctx)
	defer upCancel()
	go func() {
		select {
		case <-up:
			// Register the relay socket with A's router BEFORE reporting up,
			// so the FE (which attaches on seeing "up") finds the
			// registration when its peer.attach arrives (the state push that
			// triggers attach is sent after this, on the same conn — causal
			// order holds). The FE attaches via the relay; no ws endpoint.
			if s.conn != nil {
				if err := s.conn.RegisterPeer(host, "unix", sock, ingressSock); err != nil {
					s.setHost(host, HostState{Host: host, Origin: host, Status: StatusDown, Error: "register peer: " + err.Error()})
					return
				}
			}
			s.setHost(host, HostState{Host: host, Origin: host, Status: StatusUp})
		case <-upCtx.Done():
		}
	}()

	err := cmd.Wait()
	if ctx.Err() != nil {
		return "", "" // user-initiated disconnect
	}
	tailMu.Lock()
	stderrTail := strings.Join(tail, "\n")
	wasReady := ready
	tailCopy := append([]string(nil), tail...)
	tailMu.Unlock()
	errMsg = formatSSHAttemptError(dial, port, err, tailCopy, wasReady)
	// Classify auth refusal so wash-connect can offer the ssh-add widget
	// (docs/REMOTE.md §6.1). BatchMode never prompts, so a missing/locked
	// key surfaces as "Permission denied (publickey…)" and ssh exits 255.
	if isAuthFailure(stderrTail) {
		log.Printf("wash-remote: connect host=%s dial=%s auth failure ready=%t err=%v detail=%q", host, dial, wasReady, err, errMsg)
		return "auth", errMsg
	}
	log.Printf("wash-remote: connect host=%s dial=%s ended ready=%t err=%v detail=%q", host, dial, wasReady, err, errMsg)
	return "", errMsg
}

func formatSSHAttemptError(dial string, port int, err error, tail []string, ready bool) string {
	target := dial
	if port != 0 && port != defaultSSHPort {
		target = fmt.Sprintf("%s port %d", target, port)
	}
	var summary string
	switch {
	case err != nil:
		summary = fmt.Sprintf("ssh connection to %s failed: %v", target, err)
	case !ready:
		summary = fmt.Sprintf("ssh connection to %s ended before wash-router became ready", target)
	default:
		summary = fmt.Sprintf("ssh connection to %s closed", target)
	}
	clean := make([]string, 0, len(tail))
	for _, line := range tail {
		line = strings.TrimSpace(line)
		if line != "" {
			clean = append(clean, line)
		}
	}
	if len(clean) == 0 {
		return summary
	}
	return summary + "\n" + strings.Join(clean, "\n")
}

// isAuthFailure reports whether ssh's stderr indicates an authentication
// refusal (vs. a network/host error). These are the BatchMode signatures
// for "no usable credential" — the case the ssh-add widget fixes.
func isAuthFailure(stderr string) bool {
	for _, sig := range []string{
		"Permission denied",
		"No more authentication methods",
		"Too many authentication failures",
		"Host key verification failed",
	} {
		if strings.Contains(stderr, sig) {
			return true
		}
	}
	return false
}

// disconnect tears down a host's ssh process (which takes the remote
// router + the tunnel with it) and drops it from the published state.
func (s *supervisor) disconnect(host string) {
	s.mu.Lock()
	cancel := s.procs[host]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.removeHost(host)
}

// setHost upserts a host's state (keyed by Host) and publishes.
//
// Copy-on-write: a prior Mutate's snapshot is a SHALLOW copy (StateService
// doc) that may still be marshaling this slice's backing array on another
// goroutine, so we rebuild the slice rather than write st.Hosts[i] in place —
// an in-place write would data-race that concurrent marshal. Discovery's
// publish already does this; setHost/removeHost/setMount/removeMount must too.
func (s *supervisor) setHost(host string, hs HostState) {
	s.svc.Mutate(func(st *State) {
		out := make([]HostState, len(st.Hosts), len(st.Hosts)+1)
		copy(out, st.Hosts)
		replaced := false
		for i := range out {
			if out[i].Host == host {
				out[i] = hs
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, hs)
		}
		st.Hosts = out
	})
}

func (s *supervisor) removeHost(host string) {
	s.svc.Mutate(func(st *State) {
		out := make([]HostState, 0, len(st.Hosts))
		for _, h := range st.Hosts {
			if h.Host != host {
				out = append(out, h)
			}
		}
		st.Hosts = out
	})
}
