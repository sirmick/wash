package remote

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"

	"github.com/sirmick/wash/internal/sdk"
)

// defaultRemotePort is the loopback port wash-router binds on the remote
// host (and the far end of the ssh -L forward) when connect doesn't
// specify one.
const defaultRemotePort = 11000

// supervisor owns one ssh process per host and mirrors their state into
// the StateService the FE subscribes to.
type supervisor struct {
	svc      *sdk.StateService[State]
	sshPath  string                 // injectable for tests
	freePort func() (int, error)    // injectable for tests

	mu    sync.Mutex
	procs map[string]context.CancelFunc
}

func newSupervisor(svc *sdk.StateService[State]) *supervisor {
	return &supervisor{
		svc:      svc,
		sshPath:  "ssh",
		freePort: freeLocalPort,
		procs:    map[string]context.CancelFunc{},
	}
}

// buildSSHArgs builds the argv for the bring-up ssh: forward a local port
// to the remote loopback router port, then run wash-router there bound to
// that port with cross-origin allowed, no desktop, and no token gate — the
// remote router is loopback-only and reached solely through this tunnel,
// so SSH is the access boundary (docs/REMOTE.md §10).
func buildSSHArgs(host string, localPort, remotePort int) []string {
	return []string{
		"-o", "BatchMode=yes",            // never block on an interactive prompt
		"-o", "ExitOnForwardFailure=yes", // fail fast if the -L bind can't be set up
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort),
		host,
		"wash-router",
		"--listen", fmt.Sprintf("127.0.0.1:%d", remotePort),
		"--allow-cross-origin",
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
	localPort, err := s.freePort()
	if err != nil {
		s.mu.Unlock()
		s.setHost(host, HostState{Host: host, Origin: host, Status: StatusDown, Error: "no free local port: " + err.Error()})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.procs[host] = cancel
	s.mu.Unlock()

	s.setHost(host, HostState{Host: host, Origin: host, Status: StatusStarting})
	go s.run(ctx, host, localPort, remotePort)
}

func (s *supervisor) run(ctx context.Context, host string, localPort, remotePort int) {
	defer func() {
		s.mu.Lock()
		delete(s.procs, host)
		s.mu.Unlock()
	}()

	cmd := exec.CommandContext(ctx, s.sshPath, buildSSHArgs(host, localPort, remotePort)...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		s.setHost(host, HostState{Host: host, Origin: host, Status: StatusDown, Error: "ssh start: " + err.Error()})
		return
	}

	endpoint := fmt.Sprintf("ws://127.0.0.1:%d/ws", localPort)
	up := make(chan struct{}, 1)
	// The remote wash-router logs "listening on <addr>" once bound; ssh
	// forwards that to our stdout/stderr. That line is our readiness
	// signal — tunnel + router are live, the shell can attach.
	scan := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "listening on ") {
				select {
				case up <- struct{}{}:
				default:
				}
			}
		}
	}
	go scan(stdout)
	go scan(stderr)

	go func() {
		select {
		case <-up:
			s.setHost(host, HostState{Host: host, Origin: host, Status: StatusUp, LocalEndpoint: endpoint})
		case <-ctx.Done():
		}
	}()

	err := cmd.Wait()
	select {
	case <-ctx.Done():
		return // user-initiated disconnect; disconnect() already removed the host
	default:
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.setHost(host, HostState{Host: host, Origin: host, Status: StatusDown, Error: msg})
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

func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
