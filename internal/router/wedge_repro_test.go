package router

import (
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// wedge_repro_test.go — deterministic reproductions of the ways a wash
// terminal can hang (docs/PTY_ROBUST.md). Each test names a wedge and
// pins the robust invariant that the corresponding milestone delivers.
// "At times it hangs" becomes "here is the test that hangs."

// setHead forces the router's foreground head, bypassing the full
// reattach/migration pass so a test can exercise dispatch's ownership
// logic in isolation (reattachChannelsToShell would migrate the
// channel and mask the race we want to pin).
func setHead(r *Router, s *ShellSession) {
	r.mu.Lock()
	r.headShell = s
	r.mu.Unlock()
}

// observableApp returns an AppInstance whose raw-frame writes can be
// observed on the returned FE-style transport, plus a cleanup.
func observableApp(t *testing.T, r *Router) (*AppInstance, wire.FrameTransport, func()) {
	t.Helper()
	pp := wiretest.NewPipePair()
	inst := &AppInstance{
		AppID:      "com.wash.term",
		InstanceID: "i-term",
		Transport:  pp.EndA(),
		router:     r,
	}
	cleanup := func() {
		_ = pp.EndA().Close()
		_ = pp.EndB().Close()
	}
	return inst, pp.EndB(), cleanup
}

// readWithin reads one frame from tr or fails if none arrives within d.
func readWithin(t *testing.T, tr wire.FrameTransport, d time.Duration) wire.Frame {
	t.Helper()
	type res struct {
		f   wire.Frame
		err error
	}
	ch := make(chan res, 1)
	go func() {
		f, err := tr.ReadFrame()
		ch <- res{f, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("ReadFrame: %v", r.err)
		}
		return r.f
	case <-time.After(d):
		t.Fatalf("no frame within %s — terminal input was black-holed", d)
		return wire.Frame{}
	}
}

// TestWedge_HeadInputNotDropped (M1, Fix A) — the input-drop race.
// A terminal channel is left owned by a stale/zombie shell (s1) while
// the foreground head is s2 (e.g. a reconnect whose migration hasn't
// reached this channel, or a lingering WS). The user types into the
// terminal they are looking at — s2, the head. That keystroke MUST
// reach the app; today dispatch sees owner==s1 (non-nil) and silently
// drops it ("owned by another shell"), leaving the terminal dead to
// input.
func TestWedge_HeadInputNotDropped(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	s1, _, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	s2, _, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()
	r.registerShell(s1)
	r.registerShell(s2)

	app, feApp, cApp := observableApp(t, r)
	defer cApp()

	// Terminal channel owned by the stale shell s1, bound to the app.
	const channelID = 42
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindGeneric,
		shell:     s1,
		app:       app,
		buf:       newRingBuffer(ChannelScrollbackBytes),
	}
	r.registerChannel(b)

	// s2 is the foreground head (what the user is looking at).
	setHead(r, s2)

	// The user types into s2's terminal.
	if err := s2.dispatch(wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: []byte("ls\n")}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Invariant: the head's keystroke reaches the app, and the head
	// has adopted the channel (so a stale owner can never strand it).
	got := readWithin(t, feApp, 500*time.Millisecond)
	if string(got.Payload) != "ls\n" {
		t.Errorf("delivered payload = %q, want %q", got.Payload, "ls\n")
	}
	b.shellMu.Lock()
	owner := b.shell
	b.shellMu.Unlock()
	if owner != s2 {
		t.Errorf("after head input, channel owner = %p, want head s2 %p", owner, s2)
	}
}

// TestWedge_BackgroundShellInputDropped (M1, Fix A — negative) — the
// other side of authoritative ownership: input from a NON-head
// background shell (a stale second tab) on a channel the head drives
// is correctly dropped. The head, not the cached pointer, decides.
func TestWedge_BackgroundShellInputDropped(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	s1, _, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	s2, _, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()
	r.registerShell(s1)
	r.registerShell(s2)

	app, feApp, cApp := observableApp(t, r)
	defer cApp()

	const channelID = 7
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindGeneric,
		shell:     s1, // head owns it
		app:       app,
		buf:       newRingBuffer(ChannelScrollbackBytes),
	}
	r.registerChannel(b)
	setHead(r, s1) // s1 is the head; s2 is a background tab

	// Background s2 types — must NOT reach the app, must NOT steal
	// ownership from the head.
	if err := s2.dispatch(wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: []byte("rm -rf /\n")}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Nothing should have been delivered.
	type res struct {
		f   wire.Frame
		err error
	}
	ch := make(chan res, 1)
	go func() { f, err := feApp.ReadFrame(); ch <- res{f, err} }()
	select {
	case r := <-ch:
		t.Fatalf("background-shell input leaked to app: payload=%q err=%v", r.f.Payload, r.err)
	case <-time.After(100 * time.Millisecond):
		// good — dropped
	}
	b.shellMu.Lock()
	owner := b.shell
	b.shellMu.Unlock()
	if owner != s1 {
		t.Errorf("background input stole ownership: owner = %p, want head s1 %p", owner, s1)
	}
}

// TestWedge_OutputToWedgedFE — (M2, Fix B) the credit wedge. Forwarding
// terminal OUTPUT to an FE that has stopped granting credit (wedged but
// socket-alive) must not block the producing path forever — today
// WriteRawFrame's Reserve(context.Background()) does exactly that,
// back-pressuring into the child shell. Pinned here; unskipped when the
// resync-frame output path lands.
func TestWedge_OutputToWedgedFE(t *testing.T) {
	t.Skip("M2 (Fix B): non-blocking resync output not yet implemented — see docs/PTY_ROBUST.md")
}

// TestWedge_SlowClientHeadOfLine — (M3, Fix C) one never-reading WS
// client must not block the single per-shell drainLoop and thereby hang
// every other terminal on that shell. Needs a write deadline on the
// FE-bound transport. Pinned here; unskipped when the deadline lands.
func TestWedge_SlowClientHeadOfLine(t *testing.T) {
	t.Skip("M3 (Fix C): drainLoop write deadline not yet implemented — see docs/PTY_ROBUST.md")
}
