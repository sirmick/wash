package router

import (
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// TestResync_VideoKindSkipsRingReplay — (REVIEW-X11-WAYLAND #6) a video
// channel's ring is a concatenation of framed WebP payloads; replaying it
// through realignReplay (terminal-escape trimming) would corrupt it. resync
// for a video kind must send the reset ONLY — no raw ring bytes — and the FE
// clears its canvas on the reset. (drainAll is defined in
// behind_watchdog_test.go.)
func TestResync_VideoKindSkipsRingReplay(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	sess := &ShellSession{
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
	}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	const channelID = 41
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindVideo,
		shell:     sess,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)
	// Seed the ring with bytes that would be replayed for a terminal channel.
	b.buf.Write([]byte("\x00WEBP-FRAME-BYTES-that-must-not-be-replayed"))
	b.shellMu.Lock()
	b.behind = true
	b.shellMu.Unlock()

	r.resyncChannel(b)

	sawResync := false
	for _, f := range drainAll(t, sess.scheduler) {
		if f.Channel == channelID {
			t.Fatalf("video resync replayed %d ring bytes on channel %d — must send reset only", len(f.Payload), channelID)
		}
		if f.Channel == ChannelControl {
			msg, err := wire.DecodeCtrl(f.Payload)
			if err != nil {
				t.Fatalf("decode ctrl: %v", err)
			}
			if rs, ok := msg.(wire.ShellChannelResync); ok && rs.ChannelID == channelID {
				sawResync = true
			}
		}
	}
	if !sawResync {
		t.Error("video resync did not send a channel.resync reset")
	}
	b.shellMu.Lock()
	behind := b.behind
	b.shellMu.Unlock()
	if behind {
		t.Error("behind not cleared after video resync")
	}
}

// TestResync_GenericKindStillReplays — the terminal path is unchanged: a
// generic channel's resync still replays the scrollback ring.
func TestResync_GenericKindStillReplays(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	sess := &ShellSession{
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
	}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	const channelID = 42
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindGeneric,
		shell:     sess,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)
	const scrollback = "prompt$ echo hi"
	b.buf.Write([]byte(scrollback))
	b.shellMu.Lock()
	b.behind = true
	b.shellMu.Unlock()

	r.resyncChannel(b)

	sawReplay := false
	for _, f := range drainAll(t, sess.scheduler) {
		if f.Channel == channelID && string(f.Payload) == scrollback {
			sawReplay = true
		}
	}
	if !sawReplay {
		t.Error("generic (terminal) resync must still replay the scrollback ring")
	}
}

// TestResync_VideoKindSendsForceFrame — (REVIEW-X11-WAYLAND #6, D5 part 2) a
// video resync sends the OWNING APP a window.force_frame event so the
// compositor clears its delta state and re-emits a whole frame (the FE cleared
// its canvas on the resync, and a delta stream can't recover on its own).
func TestResync_VideoKindSendsForceFrame(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	sess := &ShellSession{
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
	}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	app, feApp, cApp := observableApp(t, r)
	defer cApp()

	const channelID = 51
	const winID = 7
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindVideo,
		app:       app,
		shell:     sess,
		windowID:  winID,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)
	b.shellMu.Lock()
	b.behind = true
	b.shellMu.Unlock()

	r.resyncChannel(b)

	// The force-frame goes to the app on the event channel (on a goroutine).
	sawForce := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !sawForce {
		f := readWithin(t, feApp, 1*time.Second)
		if f.Channel != ChannelEvent {
			continue
		}
		msg, err := wire.DecodeEvt(f.Payload)
		if err != nil {
			t.Fatalf("decode evt: %v", err)
		}
		if ff, ok := msg.(wire.EvtWindowForceFrame); ok && ff.Win == winID {
			sawForce = true
		}
	}
	if !sawForce {
		t.Error("video resync did not send window.force_frame to the app")
	}
}

// TestResync_GenericKindNoForceFrame — a terminal (generic) resync must NOT
// send a force-frame (it's video-only).
func TestResync_GenericKindNoForceFrame(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	sess := &ShellSession{scheduler: NewScheduler(), drainerDone: make(chan struct{})}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	app, feApp, cApp := observableApp(t, r)
	defer cApp()

	const channelID = 52
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindGeneric,
		app:       app,
		shell:     sess,
		windowID:  8,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)
	b.buf.Write([]byte("hi"))
	b.shellMu.Lock()
	b.behind = true
	b.shellMu.Unlock()

	r.resyncChannel(b)

	// Read the app transport briefly; it must NOT carry a force-frame.
	type res struct {
		f   wire.Frame
		err error
	}
	ch := make(chan res, 1)
	go func() { f, err := feApp.ReadFrame(); ch <- res{f, err} }()
	select {
	case rr := <-ch:
		if rr.err == nil && rr.f.Channel == ChannelEvent {
			if msg, err := wire.DecodeEvt(rr.f.Payload); err == nil {
				if _, ok := msg.(wire.EvtWindowForceFrame); ok {
					t.Error("generic resync must not send window.force_frame")
				}
			}
		}
	case <-time.After(300 * time.Millisecond):
		// no frame — correct (generic resync sends nothing to the app)
	}
}

// A reattached or resynced terminal replays its whole ring — up to
// ChannelScrollbackMaxBytes. Sent as one frame that is 4 MiB of writer
// the scheduler cannot preempt once committed, which freezes every
// higher lane for its duration. It must arrive as small frames whose
// concatenation is still exactly the scrollback.
func TestResyncReplayIsOneFrame(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	sess := &ShellSession{
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
	}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	const channelID = 43
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindGeneric,
		shell:     sess,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)

	// Many times the frame cap, so a chunking writer would be obvious.
	// Newline-free: realignReplay trims to a line boundary and would make
	// the comparison below about that instead.
	scrollback := make([]byte, 5*maxChunkBytes+123)
	for i := range scrollback {
		scrollback[i] = byte('a' + i%26)
	}
	b.buf.Write(scrollback)
	b.shellMu.Lock()
	b.behind = true
	b.shellMu.Unlock()

	r.resyncChannel(b)

	var got []byte
	frames := 0
	for _, f := range drainAll(t, sess.scheduler) {
		if f.Channel != channelID {
			continue
		}
		frames++
		got = append(got, f.Payload...)
	}
	// ONE frame, however big. The FE feeds a replay into xterm's write
	// queue, and xterm keeps the viewport following its output only while
	// ydisp == ybase; split across frames, the follow breaks at a chunk
	// boundary and the terminal stops showing output it has already parsed
	// (measured: ydisp 1232, ybase 15183, after a 20k-line burst). The
	// atomicity is the fix, so it is what this asserts — a chunking writer
	// here regresses a terminal into looking hung.
	if frames != 1 {
		t.Fatalf("replay arrived in %d frames, want exactly 1 — a split replay detaches the FE viewport", frames)
	}
	if string(got) != string(scrollback) {
		t.Errorf("replay is %d bytes, want %d", len(got), len(scrollback))
	}
}

// TestVideo_NoCreditDropsWithoutBehind — a video frame that finds no FE
// credit is DROPPED: the channel must not go behind (behind → channel.resync
// makes the FE clear its canvas, which flashed a busy guest's window
// transparent several times a second). Once credit is back, the next frame is
// forwarded and the app is asked for a whole frame to repaint the rects the
// dropped frame left stale.
func TestVideo_NoCreditDropsWithoutBehind(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	sess := &ShellSession{scheduler: NewScheduler(), drainerDone: make(chan struct{})}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	app, feApp, cApp := observableApp(t, r)
	defer cApp()

	const channelID = 61
	const winID = 9
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindVideo,
		app:       app,
		shell:     sess,
		windowID:  winID,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)
	frame := func(p string) wire.Frame {
		return wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: []byte(p)}.WithClass(wire.ClassBulk)
	}

	// No credit: dropped, not behind, no resync, nothing forwarded.
	if err := app.dispatchFrame(frame("frame-1")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	b.shellMu.Lock()
	behind, needFull := b.behind, b.videoNeedFull
	b.shellMu.Unlock()
	if behind {
		t.Fatal("video channel went behind on a would-block — must drop the frame instead")
	}
	if !needFull {
		t.Fatal("dropped video frame did not mark the channel as needing a full frame")
	}
	for _, f := range drainAll(t, sess.scheduler) {
		if f.Channel == channelID {
			t.Fatalf("frame forwarded without credit: %q", f.Payload)
		}
		if f.Channel == ChannelControl {
			if msg, err := wire.DecodeCtrl(f.Payload); err == nil {
				if _, ok := msg.(wire.ShellChannelResync); ok {
					t.Fatal("video drop sent a channel.resync (the FE would clear its canvas)")
				}
			}
		}
	}

	// Credit back: the next frame is forwarded and a force-frame follows.
	if err := b.credit.Grant(1024); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := app.dispatchFrame(frame("frame-2")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	sawFrame := false
	for _, f := range drainAll(t, sess.scheduler) {
		if f.Channel == channelID && string(f.Payload) == "frame-2" {
			sawFrame = true
		}
	}
	if !sawFrame {
		t.Error("frame not forwarded once credit was back")
	}
	sawForce := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !sawForce {
		f := readWithin(t, feApp, 1*time.Second)
		if f.Channel != ChannelEvent {
			continue
		}
		if msg, err := wire.DecodeEvt(f.Payload); err == nil {
			if ff, ok := msg.(wire.EvtWindowForceFrame); ok && ff.Win == winID {
				sawForce = true
			}
		}
	}
	if !sawForce {
		t.Error("recovery after a dropped video frame did not send window.force_frame")
	}
	b.shellMu.Lock()
	needFull = b.videoNeedFull
	b.shellMu.Unlock()
	if needFull {
		t.Error("videoNeedFull still set after recovery")
	}
}
