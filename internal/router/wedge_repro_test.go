package router

import (
	"context"
	"io"
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
// socket-alive) must NOT block the producing path — a blocking Reserve
// on the per-app read goroutine back-pressures into the child shell and
// hangs the terminal. The non-blocking forward returns promptly, holds
// the bytes byte-exact in the ring, and marks the channel "behind" so no
// torn live stream is shipped; recovery is a resync.
func TestWedge_OutputToWedgedFE(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	// A wedged FE: attached, head, but never reads/grants credit.
	s, _, cleanup := newTestShellSession(t)
	s.router = r
	defer cleanup()
	r.registerShell(s)
	setHead(r, s)

	inst := &AppInstance{AppID: "com.wash.term", InstanceID: "i-term", router: r}
	const channelID = 9
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindGeneric,
		app:       inst,
		shell:     s,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0), // FE has granted nothing
	}
	r.registerChannel(b)

	// The child shell produces output. The forward (per-app read
	// goroutine) MUST return promptly — blocking here is the hang.
	payload := []byte("hello from the shell\n")
	done := make(chan error, 1)
	go func() {
		done <- inst.dispatch(wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: payload}.WithClass(wire.ClassBulk))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("forward returned error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("forward BLOCKED on a wedged FE — the child shell would hang")
	}

	// Bytes preserved byte-exact in the ring (recoverable on resync);
	// channel marked behind so no torn live stream is shipped.
	b.shellMu.Lock()
	behind := b.behind
	b.shellMu.Unlock()
	if !behind {
		t.Error("channel not marked behind after FE wedge")
	}
	if got := b.buf.Snapshot(); string(got) != string(payload) {
		t.Errorf("ring snapshot = %q, want %q", got, payload)
	}
}

// TestWedge_BehindClearedOnReattach — (M2, Fix B) a reattach recovers a
// wedged channel: its Bind + realigned replay is a clean resync, so the
// behind flag clears and live forwarding resumes.
func TestWedge_BehindClearedOnReattach(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	s1, _, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	r.registerShell(s1)

	b := &channelBinding{
		channelID: 11,
		kind:      wire.ChannelKindGeneric,
		shell:     s1,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)
	b.shellMu.Lock()
	b.behind = true // wedged under s1
	b.shellMu.Unlock()

	// A fresh shell attaches and reattaches channels — the replay it
	// receives is a clean resync.
	s2, fe2, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()
	go func() { // drain s2's FE side so reattach writes don't block
		for {
			if _, err := fe2.ReadFrame(); err != nil {
				return
			}
		}
	}()
	r.registerShell(s2)
	r.reattachChannelsToShell(s2)

	b.shellMu.Lock()
	behind := b.behind
	owner := b.shell
	b.shellMu.Unlock()
	if behind {
		t.Error("behind not cleared by reattach — channel stays frozen after recovery")
	}
	if owner != s2 {
		t.Errorf("channel owner = %p, want reattached s2 %p", owner, s2)
	}
}

// TestWedge_SlowClientHeadOfLine — (M3, Fix C) one never-reading WS

// TestWedge_ResyncOnCreditGrant — (M2, Fix B) a wedged terminal recovers
// without a reload: once the FE grants credit again, the router sends a
// channel.resync (FE resets the xterm) followed by the realigned
// scrollback snapshot, and clears behind so live output resumes — no
// torn stream, no manual reattach.
func TestWedge_ResyncOnCreditGrant(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	s, fe, cleanup := newTestShellSession(t)
	s.router = r
	defer cleanup()
	r.registerShell(s)
	setHead(r, s)

	inst := &AppInstance{AppID: "com.wash.term", InstanceID: "i-term", router: r}
	const channelID = 13
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindGeneric,
		app:       inst,
		shell:     s,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0), // FE behind from the start
	}
	r.registerChannel(b)

	// Output arrives while the FE is wedged → suppressed, held in ring.
	payload := []byte("\x1b[32mgreen\x1b[0m prompt$ ")
	if err := inst.dispatch(wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: payload}.WithClass(wire.ClassBulk)); err != nil {
		t.Fatalf("forward: %v", err)
	}
	b.shellMu.Lock()
	behind := b.behind
	b.shellMu.Unlock()
	if !behind {
		t.Fatal("precondition: channel should be behind after wedge")
	}

	// FE catches up and grants credit → triggers a resync.
	if err := s.handleChannelCredit(wire.NewShellChannelCredit(channelID, 4096)); err != nil {
		t.Fatalf("credit grant: %v", err)
	}

	// Expect a channel.resync control then the snapshot replay.
	sawResync, sawReplay := false, false
	for i := 0; i < 4 && !(sawResync && sawReplay); i++ {
		f := readWithin(t, fe, 500*time.Millisecond)
		switch f.Channel {
		case ChannelControl:
			msg, err := wire.DecodeCtrl(f.Payload)
			if err != nil {
				t.Fatalf("decode ctrl: %v", err)
			}
			if rs, ok := msg.(wire.ShellChannelResync); ok && rs.ChannelID == channelID {
				sawResync = true
			}
		case channelID:
			if string(f.Payload) == string(payload) {
				sawReplay = true
			}
		}
	}
	if !sawResync {
		t.Error("no channel.resync control sent after credit grant")
	}
	if !sawReplay {
		t.Error("snapshot not replayed after resync")
	}
	b.shellMu.Lock()
	behind = b.behind
	b.shellMu.Unlock()
	if behind {
		t.Error("behind not cleared after resync — live output stays suppressed")
	}
}

// failWriteTransport fails every FE-bound write — the state a dead-but-
// open client reaches once wsWriteTimeout trips its stalled write.
type failWriteTransport struct{}

func (failWriteTransport) ReadFrame() (wire.Frame, error) { return wire.Frame{}, io.EOF }
func (failWriteTransport) WriteFrame(f wire.Frame) error  { return io.ErrClosedPipe }
func (failWriteTransport) Close() error                   { return nil }

// TestWedge_SlowClientHeadOfLine — (Fix C) when an FE-bound write fails,
// the single per-shell drainLoop must tear down (close the scheduler) so
// blocked producers unblock instead of every terminal on that shell
// hanging behind one wedged client. In production wsWriteTimeout turns a
// *stalled* write (dead-but-open client) into exactly this failure — see
// TestWSWriteTimeout for the deadline itself.
func TestWedge_SlowClientHeadOfLine(t *testing.T) {
	sess := &ShellSession{
		Transport:   failWriteTransport{},
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sess.drainLoop(ctx)

	// A frame to write → drainLoop attempts it, the write fails, and the
	// scheduler is closed on the way out.
	_ = sess.scheduler.Submit(context.Background(),
		wire.Frame{Flags: wire.FlagEnd, Channel: 5, Payload: []byte("x")}.WithClass(wire.ClassInteractive))

	select {
	case <-sess.drainerDone:
		// good — drainLoop tore down on the failed write
	case <-time.After(2 * time.Second):
		t.Fatal("drainLoop did not tear down after a failed FE write — producers would hang")
	}

	// Teardown closed the scheduler, so producers blocked in Submit
	// unblock with ErrSchedulerClosed rather than pinning forever.
	select {
	case <-sess.scheduler.closed:
		// good — scheduler closed
	default:
		t.Error("scheduler not closed after drainLoop teardown")
	}
}
