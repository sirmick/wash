package router

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// TestTerminalChannelMigratesToHeadShell: when a second shell attaches
// (a reconnect / stacked WS), it becomes the foreground head and a
// terminal (generic raw) channel owned by the previous shell migrates
// to it — so PTY output follows the connection the user is on instead
// of being written to a stale shell (the "terminal hangs, no prompt"
// bug). A live peer (remote-relay) channel must NOT be stolen.
func TestTerminalChannelMigratesToHeadShell(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	s1, _, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	s2, _, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()

	r.registerShell(s1)
	r.reattachChannelsToShell(s1) // s1 is the head for now

	// A terminal-style channel owned by s1.
	term := &channelBinding{
		channelID: 42,
		kind:      wire.ChannelKindGeneric,
		shell:     s1,
		buf:       newRingBuffer(ChannelScrollbackBytes),
	}
	r.registerChannel(term)

	// A peer (remote-relay) channel owned by s1 — must stay pinned.
	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()
	peer := &channelBinding{
		channelID: 7,
		kind:      "peer",
		shell:     s1,
		peerConn:  p1,
		noCredit:  true,
	}
	r.registerChannel(peer)

	// s2 attaches and becomes the head.
	r.registerShell(s2)
	r.reattachChannelsToShell(s2)

	if got := r.headShellOrAny(); got != s2 {
		t.Fatalf("headShellOrAny() = %p, want new head s2 %p", got, s2)
	}

	term.shellMu.Lock()
	termOwner := term.shell
	term.shellMu.Unlock()
	if termOwner != s2 {
		t.Errorf("terminal channel owner = %p, want migrated to head s2 %p", termOwner, s2)
	}

	peer.shellMu.Lock()
	peerOwner := peer.shell
	peer.shellMu.Unlock()
	if peerOwner != s1 {
		t.Errorf("peer channel owner = %p, want still pinned to s1 %p (relays must not migrate)", peerOwner, s1)
	}
}

// TestHeadShellFallback: headShellOrAny prefers the tracked head, falls
// back to any attached shell once the head detaches, and returns nil
// when none are attached.
func TestHeadShellFallback(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	if got := r.headShellOrAny(); got != nil {
		t.Fatalf("no shells: headShellOrAny() = %p, want nil", got)
	}

	s1, _, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	s2, _, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()

	r.registerShell(s1)
	r.registerShell(s2)
	r.reattachChannelsToShell(s2) // s2 is head

	if got := r.headShellOrAny(); got != s2 {
		t.Fatalf("headShellOrAny() = %p, want head s2 %p", got, s2)
	}

	// Head detaches → fall back to the remaining shell, not nil.
	r.unregisterShell(s2)
	if got := r.headShellOrAny(); got != s1 {
		t.Fatalf("after head detach, headShellOrAny() = %p, want fallback s1 %p", got, s1)
	}
}

// TestHeadLeavingPromotesSurvivor: when the head shell disconnects while an
// older tab is still attached, that tab gets the head back — its terminal
// channel rebinds to it and it is told (shell.head) — instead of sitting
// dark until the user clicks "Use here".
func TestHeadLeavingPromotesSurvivor(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	s1, fe1, c1 := newTestShellSession(t)
	s1.router = r
	s1.connID = 1
	defer c1()
	s2, _, c2 := newTestShellSession(t)
	s2.router = r
	s2.connID = 2
	defer c2()

	r.registerShell(s1)
	r.reattachChannelsToShell(s1)
	term := &channelBinding{
		channelID: 42,
		kind:      wire.ChannelKindGeneric,
		shell:     s1,
		buf:       newRingBuffer(ChannelScrollbackBytes),
	}
	r.registerChannel(term)
	r.registerShell(s2)
	r.reattachChannelsToShell(s2)

	gotHead := make(chan uint64, 4)
	go func() {
		for {
			f, err := fe1.ReadFrame()
			if err != nil {
				return
			}
			var p struct {
				T   string `json:"t"`
				Gen uint64 `json:"gen"`
			}
			if f.Channel == ChannelControl && json.Unmarshal(f.Payload, &p) == nil && p.T == wire.TShellHead {
				gotHead <- p.Gen
			}
		}
	}()
	<-gotHead // s1's own first attach

	r.unregisterShell(s2)

	select {
	case gen := <-gotHead:
		if gen != r.headGen.Load() {
			t.Errorf("shell.head gen = %d, want current %d", gen, r.headGen.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("survivor was never told it holds the head")
	}
	if got := r.headShellOrAny(); got != s1 {
		t.Fatalf("head = %p, want survivor s1 %p", got, s1)
	}
	term.shellMu.Lock()
	owner := term.shell
	term.shellMu.Unlock()
	if owner != s1 {
		t.Errorf("terminal channel owner = %p, want survivor s1 %p", owner, s1)
	}
}

// TestStaleSweepYieldsToNewerHead: a migration sweep for a head that has
// since been replaced must not move channels — the newer head's sweep owns
// them, and a stale one finishing late would steal them back.
func TestStaleSweepYieldsToNewerHead(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	s1, _, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	s2, _, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()

	r.registerShell(s1)
	r.registerShell(s2)
	r.reattachChannelsToShell(s1)
	stale := r.headGen.Load()
	r.reattachChannelsToShell(s2)

	term := &channelBinding{channelID: 7, kind: wire.ChannelKindGeneric, shell: s2}
	r.registerChannel(term)

	r.migrateToHead(s1, stale)

	term.shellMu.Lock()
	owner := term.shell
	term.shellMu.Unlock()
	if owner != s2 {
		t.Errorf("stale sweep moved the channel to %p, want it left on the head s2 %p", owner, s2)
	}
}

// TestOnlyHeadDrivesVideoApps: a superseded tab cannot see wash-display's
// windows (their pixels go to the head), so its input and display metrics
// must not reach the compositor. Other apps are unaffected.
func TestOnlyHeadDrivesVideoApps(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	s1, _, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	s2, _, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()

	display := &AppInstance{AppID: "com.wash.display", InstanceID: "i-1", router: r}
	other := &AppInstance{AppID: "com.wash.term", InstanceID: "i-2", router: r}
	r.registerChannel(&channelBinding{channelID: 9, kind: wire.ChannelKindVideo, app: display})
	if !display.streamsVideo.Load() {
		t.Fatal("opening a video channel did not mark the app as streaming video")
	}

	// Head set directly: a sweep would nudge this transport-less fake
	// for a frame.
	setHead := func(s *ShellSession) {
		r.mu.Lock()
		r.headShell = s
		r.mu.Unlock()
	}
	r.registerShell(s1)
	r.registerShell(s2)
	if !s1.mayDrive(display) {
		t.Error("with no head, a shell was refused the display")
	}
	setHead(s2)
	if s1.mayDrive(display) {
		t.Error("superseded shell may still drive the display")
	}
	if !s2.mayDrive(display) {
		t.Error("head shell was refused the display")
	}
	if !s1.mayDrive(other) {
		t.Error("superseded shell was refused a non-video app")
	}
}
