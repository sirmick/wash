package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fakes for the reconnect state machine (M2e): a scripted attempt in place
// of ssh, an instant backoff clock that records its delays, a fake health
// clock the script advances, and recorders for published state and peer
// registration. run() is driven synchronously — no ssh, no real sleeps.

type recMutator struct {
	mu       sync.Mutex
	st       State
	statuses []string // every status published, in order
}

func (m *recMutator) Mutate(fn func(*State)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.st)
	for _, h := range m.st.Hosts {
		m.statuses = append(m.statuses, h.Status+":"+h.Code)
	}
}

func (m *recMutator) last(host string) (HostState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.st.Hosts {
		if h.Host == host {
			return h, true
		}
	}
	return HostState{}, false
}

type recPeers struct {
	mu           sync.Mutex
	registered   []string
	unregistered int
	reg          chan struct{}
}

func (p *recPeers) RegisterPeer(origin, network, addr, ingressAddr string) error {
	p.mu.Lock()
	p.registered = append(p.registered, addr)
	p.mu.Unlock()
	if p.reg != nil {
		select {
		case p.reg <- struct{}{}:
		default:
		}
	}
	return nil
}

func (p *recPeers) UnregisterPeer(string) error {
	p.mu.Lock()
	p.unregistered++
	p.mu.Unlock()
	return nil
}

// step is one scripted attempt: how long it "stayed up" and how it ended.
type step struct {
	lasted    time.Duration
	code, msg string
}

type harness struct {
	s       *supervisor
	st      *recMutator
	peers   *recPeers
	mu      sync.Mutex
	specs   []attemptSpec
	delays  []time.Duration
	clock   time.Time
	scripts []step
}

// newHarness scripts the attempts; once the script runs out, an attempt
// blocks like a live connection until ctx is cancelled.
func newHarness(scripts ...step) *harness {
	h := &harness{st: &recMutator{}, peers: &recPeers{}, clock: time.Unix(1_000_000, 0), scripts: scripts}
	h.s = &supervisor{
		svc:     h.st,
		conn:    h.peers,
		sockDir: os.TempDir(),
		procs:   map[string]*hostProc{},
		now: func() time.Time {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.clock
		},
		after: func(d time.Duration) <-chan time.Time {
			h.mu.Lock()
			h.delays = append(h.delays, d)
			h.clock = h.clock.Add(d)
			h.mu.Unlock()
			c := make(chan time.Time, 1)
			c <- time.Time{}
			return c
		},
	}
	h.s.attempt = func(ctx context.Context, a attemptSpec) (string, string) {
		h.mu.Lock()
		h.specs = append(h.specs, a)
		if len(h.scripts) == 0 {
			h.mu.Unlock()
			<-ctx.Done()
			return "", ""
		}
		st := h.scripts[0]
		h.scripts = h.scripts[1:]
		h.clock = h.clock.Add(st.lasted)
		h.mu.Unlock()
		return st.code, st.msg
	}
	return h
}

// runUntilParked runs the loop until the script is exhausted (the next
// attempt is parked on ctx), then cancels and waits for run to return.
func (h *harness) runUntilParked(t *testing.T, host, addr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.s.run(ctx, nil, host, addr, 0)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		parked := len(h.scripts) == 0
		h.mu.Unlock()
		if parked {
			break
		}
		select {
		case <-done:
			cancel()
			return // the loop ended on its own (auth)
		case <-time.After(time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("run never consumed its script")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

const quick = time.Second

// TestRunReconnectsWithCappedBackoff: a flapping host (every attempt drops
// fast) re-dials on an exponential backoff capped at 30s, reports
// "reconnecting" each time, detaches the dead peer, and re-dials the SAME
// B-side relay socket every time — that stable name is what lets B's
// detached router be found and reused instead of started again.
func TestRunReconnectsWithCappedBackoff(t *testing.T) {
	var drops []step
	for i := 0; i < 8; i++ {
		drops = append(drops, step{lasted: quick, msg: "ssh connection to b closed"})
	}
	h := newHarness(drops...)
	h.runUntilParked(t, "b", "")

	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	if len(h.delays) != len(want) {
		t.Fatalf("delays = %v, want %d of them", h.delays, len(want))
	}
	for i, d := range want {
		if h.delays[i] != d*time.Second {
			t.Errorf("delay[%d] = %s, want %s (all: %v)", i, h.delays[i], d*time.Second, h.delays)
		}
	}
	if len(h.specs) != 9 {
		t.Fatalf("attempts = %d, want 9", len(h.specs))
	}
	for i, sp := range h.specs {
		if sp.remoteSock != h.specs[0].remoteSock {
			t.Errorf("attempt %d dialed remote socket %q, want the connection's stable %q", i, sp.remoteSock, h.specs[0].remoteSock)
		}
		if sp.legacy {
			t.Errorf("attempt %d ran legacy (no --detach) without cause", i)
		}
	}
	n := 0
	for _, s := range h.st.statuses {
		if s == StatusReconnecting+":" {
			n++
		}
	}
	if n != 8 {
		t.Errorf("published reconnecting %d times, want 8: %v", n, h.st.statuses)
	}
	// One unregister per drop, plus the final one on teardown.
	if h.peers.unregistered != 9 {
		t.Errorf("UnregisterPeer calls = %d, want 9", h.peers.unregistered)
	}
}

// TestRunHealthyConnectionResetsBackoff: a drop after a connection proved
// healthy (up >= 30s) is a fresh incident and re-dials from the 1s floor;
// only fast re-drops escalate.
func TestRunHealthyConnectionResetsBackoff(t *testing.T) {
	h := newHarness(
		step{lasted: quick},
		step{lasted: quick},
		step{lasted: 40 * time.Second},
		step{lasted: quick},
	)
	h.runUntilParked(t, "b", "")
	want := []time.Duration{time.Second, 2 * time.Second, time.Second, 2 * time.Second}
	if len(h.delays) != len(want) {
		t.Fatalf("delays = %v, want %v", h.delays, want)
	}
	for i := range want {
		if h.delays[i] != want[i] {
			t.Fatalf("delays = %v, want %v", h.delays, want)
		}
	}
}

// TestRunAuthFailureStops: a key refusal won't fix itself on retry — the
// loop publishes down/auth and returns without backing off.
func TestRunAuthFailureStops(t *testing.T) {
	h := newHarness(step{lasted: quick, code: "auth", msg: "Permission denied (publickey)."})
	h.runUntilParked(t, "b", "")
	if len(h.delays) != 0 {
		t.Errorf("auth failure backed off %v; it must stop", h.delays)
	}
	hs, ok := h.st.last("b")
	if !ok || hs.Status != StatusDown || hs.Code != "auth" {
		t.Errorf("final state = %+v, want down/auth", hs)
	}
}

// TestRunFallsBackToAnnouncedAddr: a name that never comes up is retried
// once against the mDNS address, immediately (no backoff spent on it).
func TestRunFallsBackToAnnouncedAddr(t *testing.T) {
	h := newHarness(step{lasted: quick, msg: "Could not resolve hostname"})
	h.runUntilParked(t, "b.local", "10.0.0.7")
	if len(h.specs) != 2 || h.specs[0].dial != "b.local" || h.specs[1].dial != "10.0.0.7" {
		t.Fatalf("dials = %+v, want b.local then 10.0.0.7", h.specs)
	}
	if len(h.delays) != 0 {
		t.Errorf("fallback waited %v; it should retry at once", h.delays)
	}
	if h.specs[1].remoteSock != h.specs[0].remoteSock {
		t.Error("fallback dial must target the same B relay socket")
	}
}

// TestRunLegacyBRetriesWithoutDetach: a B whose wash-router predates
// --detach rejects the flag; the supervisor retries at once without it
// rather than flapping forever against an older host.
func TestRunLegacyBRetriesWithoutDetach(t *testing.T) {
	h := newHarness(step{lasted: quick, msg: "ssh connection to b failed: exit status 2\nstderr: flag provided but not defined: -detach"})
	h.runUntilParked(t, "b", "")
	if len(h.specs) != 2 || h.specs[0].legacy || !h.specs[1].legacy {
		t.Fatalf("attempts = %+v, want detach then legacy", h.specs)
	}
	if len(h.delays) != 0 {
		t.Errorf("legacy retry waited %v; it should retry at once", h.delays)
	}
	if args := strings.Join(buildSSHArgs("b", "/a", "/r", "/ai", "/ri", 0, true), " "); strings.Contains(args, "--detach") {
		t.Errorf("legacy args still carry --detach: %s", args)
	}
}

// TestDisconnectEndsRun: disconnect cancels a live connection (the parked
// attempt), the loop unwinds and frees its slot, and the host leaves the
// published state.
func TestDisconnectEndsRun(t *testing.T) {
	h := newHarness()
	h.s.connect("b", "", 0)
	h.s.mu.Lock()
	p := h.s.procs["b"]
	h.s.mu.Unlock()
	if p == nil {
		t.Fatal("connect did not register a run")
	}
	h.s.connect("b", "", 0) // duplicate connect is a no-op
	h.s.disconnect("b")
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not end on disconnect")
	}
	h.s.mu.Lock()
	n := len(h.s.procs)
	h.s.mu.Unlock()
	if n != 0 {
		t.Errorf("procs = %d after disconnect, want 0", n)
	}
	if _, ok := h.st.last("b"); ok {
		t.Error("host still published after disconnect")
	}
	h.mu.Lock()
	attempts := len(h.specs)
	h.mu.Unlock()
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (duplicate connect must not dial)", attempts)
	}
}

// fakeSSH writes a stand-in ssh: it announces readiness like B's keeper,
// then records the first stdin line (or EOF) to $FAKE_SSH_OUT. mode
// "drop" exits right after the readiness line instead — an ssh blip.
func fakeSSH(t *testing.T, mode string) (path, out string) {
	t.Helper()
	dir := t.TempDir()
	out = filepath.Join(dir, "stdin.txt")
	path = filepath.Join(dir, "ssh")
	script := "#!/bin/sh\necho 'listening on unix:///tmp/x (detached relay router, started)'\n"
	if mode == "drop" {
		script += "exit 255\n"
	} else {
		script += "if read line; then echo \"$line\" > \"$FAKE_SSH_OUT\"; else echo EOF > \"$FAKE_SSH_OUT\"; fi\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SSH_OUT", out)
	return path, out
}

// TestRunOnceStopsRemoteOnDisconnect: a deliberate cancel writes "stop" to
// the ssh's stdin — the line B's keeper relays to shut the detached router
// down instead of leaving it to linger — and the attempt reports no error.
func TestRunOnceStopsRemoteOnDisconnect(t *testing.T) {
	ssh, out := fakeSSH(t, "hold")
	peers := &recPeers{reg: make(chan struct{}, 1)}
	s := &supervisor{svc: &recMutator{}, conn: peers, sshPath: ssh, sockDir: t.TempDir(), procs: map[string]*hostProc{}}
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan [2]string, 1)
	go func() {
		code, msg := s.runOnce(ctx, attemptSpec{host: "b", dial: "b", sock: s.sockPath("b"), ingressSock: s.ingressSockPath("b"), remoteSock: "/tmp/wash-relay-test.sock"})
		res <- [2]string{code, msg}
	}()
	select {
	case <-peers.reg:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness line never registered the peer")
	}
	cancel()
	select {
	case r := <-res:
		if r != [2]string{"", ""} {
			t.Errorf("cancelled attempt = %q, want (\"\", \"\")", r)
		}
	case <-time.After(stopGrace + 2*time.Second):
		t.Fatal("runOnce did not return after cancel")
	}
	got, _ := os.ReadFile(out)
	if strings.TrimSpace(string(got)) != "stop" {
		t.Errorf("ssh stdin got %q, want \"stop\"", got)
	}
}

// TestRunOnceReportsDropAfterReady: an ssh exit after readiness is a plain
// drop for the reconnect loop, not an auth failure. (That stdin is held
// open — the keeper reads EOF as "session gone" — is pinned by the stop
// test above: its fake ssh would read EOF and exit before the cancel.)
func TestRunOnceReportsDropAfterReady(t *testing.T) {
	ssh, _ := fakeSSH(t, "drop")
	s := &supervisor{svc: &recMutator{}, conn: &recPeers{}, sshPath: ssh, sockDir: t.TempDir(), procs: map[string]*hostProc{}}
	code, msg := s.runOnce(context.Background(), attemptSpec{host: "b", dial: "b", sock: s.sockPath("b"), ingressSock: s.ingressSockPath("b"), remoteSock: "/tmp/wash-relay-test.sock"})
	if code != "" || !strings.Contains(msg, "exit status 255") {
		t.Errorf("drop = (%q, %q), want a non-auth drop", code, msg)
	}
}
