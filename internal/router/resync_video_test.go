package router

import (
	"testing"

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
