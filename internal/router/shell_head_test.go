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

// headNotices collects the shell.head / shell.superseded frames a test
// shell's FE receives, as "head" / "superseded".
func headNotices(fe wire.FrameTransport) <-chan string {
	out := make(chan string, 16)
	go func() {
		for {
			f, err := fe.ReadFrame()
			if err != nil {
				return
			}
			var p struct {
				T string `json:"t"`
			}
			if f.Channel != ChannelControl || json.Unmarshal(f.Payload, &p) != nil {
				continue
			}
			switch p.T {
			case wire.TShellHead:
				out <- "head"
			case wire.TShellSuperseded:
				out <- "superseded"
			}
		}
	}()
	return out
}

func nextNotice(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case n := <-ch:
		return n
	case <-time.After(2 * time.Second):
		t.Fatal("no head notice")
		return ""
	}
}

func claim(t *testing.T, s *ShellSession, ifGen uint64) {
	t.Helper()
	payload, err := json.Marshal(wire.ShellClaimHead{T: wire.TShellClaimHead, IfGen: ifGen})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.dispatch(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: payload}); err != nil {
		t.Fatalf("dispatch claim_head: %v", err)
	}
}

// TestAttachLeavesALiveHeadAlone: a shell attaching while another holds the
// head is told it is not the head, and the terminals stay where they are.
// Every attach used to take the head, so a background tab that the
// read-idle reaper kept closing took the terminals off the tab in use on
// each redial, about every 95s.
func TestAttachLeavesALiveHeadAlone(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	s1, fe1, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	s2, fe2, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()
	n1, n2 := headNotices(fe1), headNotices(fe2)

	r.registerShell(s1)
	r.attachShell(s1)
	if got := nextNotice(t, n1); got != "head" {
		t.Fatalf("first attach got %q, want head", got)
	}
	term := &channelBinding{channelID: 42, kind: wire.ChannelKindGeneric, shell: s1, buf: newRingBuffer(ChannelScrollbackBytes)}
	r.registerChannel(term)

	r.registerShell(s2)
	r.attachShell(s2)
	if got := nextNotice(t, n2); got != "superseded" {
		t.Fatalf("second attach got %q, want superseded", got)
	}
	if got := r.headShellOrAny(); got != s1 {
		t.Fatalf("head = %p, want s1 %p kept", got, s1)
	}
	term.shellMu.Lock()
	owner := term.shell
	term.shellMu.Unlock()
	if owner != s1 {
		t.Errorf("terminal moved to %p on attach, want left on s1 %p", owner, s1)
	}

	// The user touches the second tab: an unconditional claim takes it.
	claim(t, s2, 0)
	if got := r.headShellOrAny(); got != s2 {
		t.Fatalf("after claim, head = %p, want s2 %p", got, s2)
	}
	if got := nextNotice(t, n1); got != "superseded" {
		t.Errorf("old head got %q, want superseded", got)
	}
}

// TestRedialClaimIsConditional: a tab redialing after holding the head
// gets it back only if nobody took it in between — its stale socket may
// still hold it, which is fine — and a tab that lost it while dark does not
// take it back from the one in use.
func TestRedialClaimIsConditional(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	old, _, c1 := newTestShellSession(t)
	old.router = r
	defer c1()
	redial, feR, c2 := newTestShellSession(t)
	redial.router = r
	defer c2()
	other, feO, c3 := newTestShellSession(t)
	other.router = r
	defer c3()
	nR, nO := headNotices(feR), headNotices(feO)

	// The tab's old socket holds the head; its redial arrives alongside.
	r.registerShell(old)
	r.attachShell(old)
	held := r.headGen.Load()
	r.registerShell(redial)
	r.attachShell(redial)
	if got := nextNotice(t, nR); got != "superseded" {
		t.Fatalf("redial attach got %q, want superseded", got)
	}
	claim(t, redial, held)
	if got := r.headShellOrAny(); got != redial {
		t.Fatalf("redial claim at the held gen refused: head = %p, want %p", got, redial)
	}
	if got := nextNotice(t, nR); got != "head" {
		t.Fatalf("redial got %q, want head", got)
	}

	// Another tab's redial claims a generation it lost long ago: refused.
	r.registerShell(other)
	r.attachShell(other)
	if got := nextNotice(t, nO); got != "superseded" {
		t.Fatalf("other attach got %q, want superseded", got)
	}
	claim(t, other, held)
	if got := r.headShellOrAny(); got != redial {
		t.Fatalf("stale claim took the head: head = %p, want %p kept", got, redial)
	}
	if got := nextNotice(t, nO); got != "superseded" {
		t.Errorf("stale claimant got %q, want superseded", got)
	}

	// The head claiming again is confirmed without a takeover.
	gen := r.headGen.Load()
	claim(t, redial, 0)
	if r.headGen.Load() != gen {
		t.Errorf("the head's own claim bumped the generation %d -> %d", gen, r.headGen.Load())
	}
	if got := nextNotice(t, nR); got != "head" {
		t.Errorf("head re-claim got %q, want head", got)
	}
}

// TestRedialClaimWinsOverPromotion: the head's socket closes cleanly, so the
// other tab is promoted at once; the head's redial still claims the head it
// held. Nobody chose the promoted tab, and refusing would hand the session
// to whichever box was left open on every network blip.
func TestRedialClaimWinsOverPromotion(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	other, _, c1 := newTestShellSession(t)
	other.router = r
	other.connID = 1
	defer c1()
	old, _, c2 := newTestShellSession(t)
	old.router = r
	old.connID = 2
	defer c2()
	redial, feR, c3 := newTestShellSession(t)
	redial.router = r
	redial.connID = 3
	defer c3()
	nR := headNotices(feR)

	r.registerShell(other)
	r.attachShell(other)
	r.registerShell(old)
	r.attachShell(old)
	claim(t, old, 0) // the user is using this tab
	held := r.headGen.Load()

	r.unregisterShell(old)
	deadline := time.Now().Add(2 * time.Second)
	for r.headShellOrAny() != other || r.headGen.Load() == held {
		if time.Now().After(deadline) {
			t.Fatal("the other tab was never promoted")
		}
		time.Sleep(5 * time.Millisecond)
	}

	r.registerShell(redial)
	r.attachShell(redial)
	if got := nextNotice(t, nR); got != "superseded" {
		t.Fatalf("redial attach got %q, want superseded", got)
	}
	claim(t, redial, held)
	if got := r.headShellOrAny(); got != redial {
		t.Fatalf("redial claim refused over a promotion: head = %p, want %p", got, redial)
	}
}
