package router

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// newTestShellSession builds a ShellSession with its scheduler +
// drainer wired up the same way HandleShell does, but without the
// full router setup. Returns the session, the FE-side pipe end (for
// reading frames as the browser would), and a cleanup func.
func newTestShellSession(t *testing.T) (*ShellSession, wire.FrameTransport, func()) {
	t.Helper()
	pp := wiretest.NewPipePair()
	sess := &ShellSession{
		Transport:   pp.EndA(),
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	go sess.drainLoop(ctx)
	cleanup := func() {
		sess.scheduler.Close()
		cancel()
		<-sess.drainerDone
		_ = pp.EndA().Close()
		_ = pp.EndB().Close()
	}
	return sess, pp.EndB(), cleanup
}

// TestShellSchedulerControlBeatsBulk — verifies that a control-channel
// (Interactive) frame submitted after a flood of WriteRawFrame
// (Bulk) calls reaches the FE before the entire flood drains.
//
// Phase 6a's central promise: a single chatty channel must not
// block interactive lifecycle traffic from a different channel on
// the same WebSocket.
func TestShellSchedulerControlBeatsBulk(t *testing.T) {
	sess, fe, cleanup := newTestShellSession(t)
	defer cleanup()

	const bulkFrames = 1000
	const rawChannel = 5
	// Throttle the reader so frames accumulate at the scheduler/pipe
	// boundary — models a real-world FE that's a few ms behind.
	// Without this, the drainer empties the bulk queue faster than
	// the producer can fill it and there's no backlog for the
	// control frame to jump.
	const readerDelay = 50 * time.Microsecond

	type readResult struct {
		ch    uint32
		class wire.Class
	}
	results := make(chan readResult, bulkFrames+4)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			f, err := fe.ReadFrame()
			if err != nil {
				return
			}
			time.Sleep(readerDelay)
			results <- readResult{ch: f.Channel, class: f.Class()}
		}
	}()

	// Producer goroutine: spam Bulk frames on a raw channel.
	bulkSent := make(chan struct{})
	go func() {
		for i := 0; i < bulkFrames; i++ {
			if err := sess.WriteRawFrame(rawChannel, []byte{byte(i)}); err != nil {
				t.Errorf("WriteRawFrame %d: %v", i, err)
				return
			}
		}
		close(bulkSent)
	}()

	// Give the bulk producer a head start so the Bulk queue fills.
	time.Sleep(5 * time.Millisecond)

	// Submit one control-channel (Interactive) frame.
	type ctrlMsg struct {
		T  string `json:"t"`
		ID string `json:"id"`
	}
	if err := sess.WriteCtrl(ctrlMsg{T: "shell.log", ID: "marker"}); err != nil {
		t.Fatalf("WriteCtrl: %v", err)
	}

	// Find the first control-channel frame in the result stream
	// and count how many Bulk frames preceded it.
	bulkBefore := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case r := <-results:
			if r.ch == ChannelControl {
				goto gotControl
			}
			bulkBefore++
		case <-time.After(50 * time.Millisecond):
			// Keep polling.
		}
	}
	t.Fatalf("control frame never arrived (drained %d bulk frames)", bulkBefore)

gotControl:
	// The bulk queue size is 64. The pipe buffer adds another 32
	// frames already-in-flight before the drainer can prefer
	// Interactive. So the realistic upper bound is queue+pipe+jitter
	// — call it 3x the bulk queue.
	if bulkBefore > 3*ClassQueueSize[wire.ClassBulk] {
		t.Fatalf("control frame got %d bulk frames ahead of it (want ≤ %d)",
			bulkBefore, 3*ClassQueueSize[wire.ClassBulk])
	}

	<-bulkSent
}

// TestShellSchedulerFEDisconnectUnblocksProducers — when the FE
// transport write fails (closed pipe), the drainer closes the
// scheduler and any blocked producer returns. No goroutine leak.
func TestShellSchedulerFEDisconnectUnblocksProducers(t *testing.T) {
	// Tight bulk queue so we can force backpressure quickly.
	old := ClassQueueSize[wire.ClassBulk]
	ClassQueueSize[wire.ClassBulk] = 2
	defer func() { ClassQueueSize[wire.ClassBulk] = old }()

	sess, fe, cleanup := newTestShellSession(t)

	// Don't drain the FE side. Fill the bulk queue + drainer's
	// in-flight write + saturate the FE pipe.
	go func() {
		for i := 0; i < 10; i++ {
			_ = sess.WriteRawFrame(5, []byte{byte(i)})
		}
	}()

	// Let goroutines settle.
	time.Sleep(20 * time.Millisecond)

	// Close FE end to simulate browser disconnect. The drainer's
	// next WriteFrame should fail; it will close the scheduler.
	_ = fe.Close()

	// Producer goroutine should now have unblocked (or be about to).
	// We can't directly observe it; instead, verify cleanup
	// completes within a reasonable bound — proves no goroutine is
	// permanently stuck.
	done := make(chan struct{})
	go func() {
		cleanup()
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(1 * time.Second):
		t.Fatalf("cleanup hung — drainer or producer goroutine stuck")
	}
}

// TestShellSchedulerNilFallbackPath — pre-HandleShell writes (e.g.
// via direct construction in legacy code paths) MUST still work
// without panicking when scheduler is nil. Belt-and-braces.
func TestShellSchedulerNilFallbackPath(t *testing.T) {
	pp := wiretest.NewPipePair()
	defer func() {
		_ = pp.EndA().Close()
		_ = pp.EndB().Close()
	}()
	sess := &ShellSession{Transport: pp.EndA()}

	// Background reader so the FE write doesn't block.
	readDone := make(chan struct{})
	go func() {
		_, _ = pp.EndB().ReadFrame()
		close(readDone)
	}()

	type ctrlMsg struct {
		T string `json:"t"`
	}
	if err := sess.WriteCtrl(ctrlMsg{T: "shell.log"}); err != nil {
		t.Fatalf("WriteCtrl on nil-scheduler session: %v", err)
	}
	<-readDone
}

// TestShellSchedulerFramesCarryClassBits — sanity check that a
// frame submitted via WriteRawFrame arrives at the FE with the Bulk
// class bit set; via WriteCtrl arrives Interactive.
func TestShellSchedulerFramesCarryClassBits(t *testing.T) {
	sess, fe, cleanup := newTestShellSession(t)
	defer cleanup()

	// Drainer-side: collect frames + their class bits.
	var (
		got      []wire.Frame
		gotMu    sync.Mutex
		nReady   atomic.Int32
		drainCtx = make(chan struct{})
	)
	go func() {
		defer close(drainCtx)
		for {
			f, err := fe.ReadFrame()
			if err != nil {
				return
			}
			gotMu.Lock()
			got = append(got, f)
			gotMu.Unlock()
			nReady.Add(1)
		}
	}()

	type ctrlMsg struct {
		T string `json:"t"`
	}
	if err := sess.WriteCtrl(ctrlMsg{T: "shell.log"}); err != nil {
		t.Fatalf("WriteCtrl: %v", err)
	}
	if err := sess.WriteRawFrame(7, []byte("payload")); err != nil {
		t.Fatalf("WriteRawFrame: %v", err)
	}

	// Wait for both to arrive.
	deadline := time.Now().Add(500 * time.Millisecond)
	for nReady.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	gotMu.Lock()
	defer gotMu.Unlock()
	if len(got) < 2 {
		t.Fatalf("only %d frames received", len(got))
	}
	var ctrlFrame, rawFrame *wire.Frame
	for i := range got {
		if got[i].Channel == ChannelControl {
			ctrlFrame = &got[i]
		} else if got[i].Channel == 7 {
			rawFrame = &got[i]
		}
	}
	if ctrlFrame == nil {
		t.Fatalf("control frame missing in %d received", len(got))
	}
	if rawFrame == nil {
		t.Fatalf("raw frame missing in %d received", len(got))
	}
	if ctrlFrame.Class() != wire.ClassInteractive {
		t.Fatalf("control frame class=%v, want Interactive", ctrlFrame.Class())
	}
	if rawFrame.Class() != wire.ClassBulk {
		t.Fatalf("raw frame class=%v, want Bulk", rawFrame.Class())
	}
}
