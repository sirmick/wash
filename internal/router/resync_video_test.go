package router

import (
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
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
