package router

import (
	"context"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// TestResync_StaysBehindStaleBulk — (REVIEW-DATAPATH F4) resync must not
// overtake same-channel Bulk frames still queued in the scheduler. Those
// stale frames were accepted before the wedge; if the resync reset (Control)
// and snapshot (Interactive) jump ahead of them via strict priority, the FE
// sees reset → snapshot → STALE duplicates, redrawing bytes the snapshot
// already contained. Emitting reset + snapshot at ClassBulk keeps them FIFO
// behind the stale tail, so the reset lands AFTER the stale frames (which it
// then clears on the FE) and nothing stale drains after the reset.
//
// Driven at the scheduler level (no drainLoop) so the drain order is
// deterministic: Next() returns frames in exactly the order the FE would see.
func TestResync_StaysBehindStaleBulk(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	sess := &ShellSession{
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
	}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	const channelID = 21
	b := &channelBinding{
		channelID: channelID,
		kind:      wire.ChannelKindGeneric,
		shell:     sess,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)

	// Stale same-channel Bulk frames accepted before the wedge, still
	// undrained in the scheduler.
	stale := []string{"STALE-1", "STALE-2"}
	for _, p := range stale {
		f := wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: []byte(p)}.WithClass(wire.ClassBulk)
		if !sess.scheduler.TrySubmit(f) {
			t.Fatalf("enqueue stale %q", p)
		}
	}

	// Seed the ring so the resync snapshot is identifiable; mark behind.
	const snapshot = "SNAPSHOT-BYTES"
	b.buf.Write([]byte(snapshot))
	b.shellMu.Lock()
	b.behind = true
	b.shellMu.Unlock()

	// Trigger recovery. resyncChannel enqueues reset + snapshot at ClassBulk.
	r.resyncChannel(b)

	// Drain in priority order and record the FE-visible sequence.
	var order []string
	for i := 0; i < len(stale)+2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		f, err := sess.scheduler.Next(ctx)
		cancel()
		if err != nil {
			t.Fatalf("drain %d: %v", i, err)
		}
		switch f.Channel {
		case ChannelControl:
			msg, err := wire.DecodeCtrl(f.Payload)
			if err != nil {
				t.Fatalf("decode ctrl: %v", err)
			}
			if rs, ok := msg.(wire.ShellChannelResync); ok && rs.ChannelID == channelID {
				order = append(order, "RESET")
			} else {
				t.Fatalf("unexpected ctrl frame: %T", msg)
			}
		case channelID:
			order = append(order, string(f.Payload))
		default:
			t.Fatalf("unexpected channel %d", f.Channel)
		}
	}

	want := []string{"STALE-1", "STALE-2", "RESET", snapshot}
	if len(order) != len(want) {
		t.Fatalf("drain order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("drain order = %v, want %v (index %d)", order, want, i)
		}
	}

	// Explicit F4 property: no stale byte drains AFTER the reset (which would
	// be a duplicate the FE renders past the snapshot).
	resetAt := -1
	for i, o := range order {
		if o == "RESET" {
			resetAt = i
		}
	}
	for i := resetAt + 1; i < len(order); i++ {
		for _, s := range stale {
			if order[i] == s {
				t.Fatalf("stale frame %q drained after the reset (index %d) — F4 regression", s, i)
			}
		}
	}
}
