package router

import (
	"context"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// drainAll pulls every currently-queued frame out of the scheduler in
// priority order, stopping when no frame arrives within a short window.
// Class-agnostic so the assertions don't care which class resync rides.
func drainAll(t *testing.T, sched *Scheduler) []wire.Frame {
	t.Helper()
	var out []wire.Frame
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		f, err := sched.Next(ctx)
		cancel()
		if err != nil {
			return out
		}
		out = append(out, f)
	}
}

// TestBehindWatchdog_RecoversWithoutCreditGrant — (REVIEW-RECONNECT M1 /
// REVIEW-DATAPATH F2) the non-self-healing wedge: a channel goes behind
// because the shared Bulk scheduler queue is full even though credit
// remains. With output suppressed the FE absorbs nothing on that channel, so
// its absorption-driven credit grant never crosses the threshold and no
// grant — hence no credit-triggered resync — ever arrives. The per-shell
// behind watchdog (resyncBehindChannels) must recover it anyway once the
// queue drains, with no input and no credit grant.
func TestBehindWatchdog_RecoversWithoutCreditGrant(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	sess := &ShellSession{
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
	}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	inst := &AppInstance{AppID: "com.wash.term", InstanceID: "i-term", router: r}
	const channelID = 31
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindGeneric,
		app:       inst,
		shell:     sess,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(1 << 20), // plenty of credit remaining
	}
	r.registerChannel(b)

	// Fill the shared Bulk queue so the terminal forward's TrySubmit fails
	// even though credit is available — the scheduler-full wedge.
	fill := 0
	for sess.scheduler.TrySubmit(wire.Frame{Flags: wire.FlagEnd, Channel: 999, Payload: []byte{0}}.WithClass(wire.ClassBulk)) {
		fill++
		if fill > 4096 {
			t.Fatal("bulk queue never reported full")
		}
	}

	// Output arrives while the queue is full → suppressed, held in the ring,
	// channel marked behind. No torn stream shipped.
	payload := []byte("watch-only output\n")
	if err := inst.dispatch(wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: payload}.WithClass(wire.ClassBulk)); err != nil {
		t.Fatalf("forward: %v", err)
	}
	b.shellMu.Lock()
	behind := b.behind
	b.shellMu.Unlock()
	if !behind {
		t.Fatal("precondition: channel should be behind after a full-queue forward")
	}

	// FE catches up: the queue drains, but the suppressed channel produced no
	// bytes for the FE to absorb, so it sends NO credit grant. Drain the queue
	// (and discard) to model that.
	drainAll(t, sess.scheduler)

	// The watchdog tick fires — with no input and no credit grant.
	r.resyncBehindChannels(sess)

	// Assert the channel recovered: a channel.resync reset + the snapshot
	// replay were enqueued, and behind cleared.
	sawResync, sawReplay := false, false
	for _, f := range drainAll(t, sess.scheduler) {
		switch f.Channel {
		case ChannelControl:
			msg, derr := wire.DecodeCtrl(f.Payload)
			if derr != nil {
				t.Fatalf("decode ctrl: %v", derr)
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
		t.Error("watchdog did not send a channel.resync — behind channel stays dark")
	}
	if !sawReplay {
		t.Error("watchdog resync did not replay the snapshot")
	}
	b.shellMu.Lock()
	behind = b.behind
	b.shellMu.Unlock()
	if behind {
		t.Error("behind not cleared by the watchdog resync")
	}
}
