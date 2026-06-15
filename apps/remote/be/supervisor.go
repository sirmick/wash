package remote

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sirmick/wash/internal/sdk"
)

// defaultRemotePort is the loopback port wash-router binds on the remote
// host (and the far end of the ssh -L forward) when connect doesn't
// specify one.
const defaultRemotePort = 11000

// supervisor owns one ssh process per host and mirrors their state into
// the StateService the FE subscribes to. It also registers each host's
// local relay socket with A's router (so a browser can attach it).
type supervisor struct {
	svc     *sdk.StateService[State]
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

// buildSSHArgs builds the argv for the bring-up ssh: forward a LOCAL UNIX
// SOCKET to host B's loopback router port, then run wash-router there in
// raw-wire mode (--listen-raw, no HTTP/WebSocket) bound to that port. A's
// router dials the local socket and splices a browser's muxed channel to
// it (the "one port" relay, docs/REMOTE.md) — nothing binds a TCP port on
// A's loopback, and B's router is reached solely through this tunnel, so
// SSH is the access boundary (§10).
func buildSSHArgs(host, localSock string, remotePort int) []string {
	return []string{
		"-o", "BatchMode=yes",            // never block on an interactive prompt
		"-o", "ExitOnForwardFailure=yes", // fail fast if the -L bind can't be set up
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-L", fmt.Sprintf("%s:127.0.0.1:%d", localSock, remotePort),
		host,
		"wash-router",
		"--listen-raw", fmt.Sprintf("tcp:127.0.0.1:%d", remotePort),
		"--no-session",
		"--no-auth",
	}
}

// connect brings up (or no-ops if already up) a connection to host.
func (s *supervisor) connect(host string, remotePort int) {
	if remotePort == 0 {
		remotePort = defaultRemotePort
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
	go s.run(ctx, host, remotePort)
}

func (s *supervisor) run(ctx context.Context, host string, remotePort int) {
	sock := s.sockPath(host)
	defer func() {
		s.mu.Lock()
		delete(s.procs, host)
		s.mu.Unlock()
		_ = os.Remove(sock)
		if s.conn != nil {
			_ = s.conn.UnregisterPeer(host)
		}
	}()

	// ssh -L refuses to bind a unix socket whose file already exists.
	_ = os.Remove(sock)

	cmd := exec.CommandContext(ctx, s.sshPath, buildSSHArgs(host, sock, remotePort)...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		s.setHost(host, HostState{Host: host, Origin: host, Status: StatusDown, Error: "ssh start: " + err.Error()})
		return
	}

	up := make(chan struct{}, 1)
	// tail accumulates the last few stderr lines so an exit-with-error
	// can be classified (auth refusal vs. anything else). Guarded — the
	// two scan goroutines write it concurrently.
	var tailMu sync.Mutex
	var tail []string
	// The remote wash-router logs "listening on <addr>" once bound; ssh
	// forwards that to our stdout/stderr. That line is our readiness
	// signal — tunnel + router are live, the shell can attach.
	scan := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, "listening on ") {
				select {
				case up <- struct{}{}:
				default:
				}
				continue
			}
			tailMu.Lock()
			tail = append(tail, line)
			if len(tail) > 10 {
				tail = tail[len(tail)-10:]
			}
			tailMu.Unlock()
		}
	}
	go scan(stdout)
	go scan(stderr)

	go func() {
		select {
		case <-up:
			// Register the relay socket with A's router BEFORE reporting up,
			// so the FE (which attaches on seeing "up") finds the
			// registration when its peer.attach arrives (the state push that
			// triggers attach is sent after this, on the same conn — causal
			// order holds). The FE attaches via the relay; no ws endpoint.
			if s.conn != nil {
				if err := s.conn.RegisterPeer(host, "unix", sock); err != nil {
					s.setHost(host, HostState{Host: host, Origin: host, Status: StatusDown, Error: "register peer: " + err.Error()})
					return
				}
			}
			s.setHost(host, HostState{Host: host, Origin: host, Status: StatusUp})
		case <-ctx.Done():
		}
	}()

	err := cmd.Wait()
	select {
	case <-ctx.Done():
		return // user-initiated disconnect; disconnect() already removed the host
	default:
	}
	tailMu.Lock()
	stderrTail := strings.Join(tail, "\n")
	tailMu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	hs := HostState{Host: host, Origin: host, Status: StatusDown, Error: msg}
	// Classify auth refusal so wash-connect can offer the ssh-add widget
	// (docs/REMOTE.md §6.1). BatchMode never prompts, so a missing/locked
	// key surfaces as "Permission denied (publickey…)" and ssh exits 255.
	if isAuthFailure(stderrTail) {
		hs.Code = "auth"
		if msg == "" {
			hs.Error = "ssh authentication failed"
		}
	}
	s.setHost(host, hs)
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
func (s *supervisor) setHost(host string, hs HostState) {
	s.svc.Mutate(func(st *State) {
		for i := range st.Hosts {
			if st.Hosts[i].Host == host {
				st.Hosts[i] = hs
				return
			}
		}
		st.Hosts = append(st.Hosts, hs)
	})
}

func (s *supervisor) removeHost(host string) {
	s.svc.Mutate(func(st *State) {
		out := st.Hosts[:0]
		for _, h := range st.Hosts {
			if h.Host != host {
				out = append(out, h)
			}
		}
		st.Hosts = out
	})
}
