package router

import (
	"net"
	"testing"

	"github.com/sirmick/wash/internal/wire"
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
