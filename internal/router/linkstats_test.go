package router

import (
	"context"
	"testing"

	"github.com/sirmick/wash/internal/wire"
)

// frameOf (class, ch, tag) is shared with qos_test.go.

func TestLinkStatsRecordAndSnapshot(t *testing.T) {
	var l LinkStats
	l.recordTx(wire.ClassBulk, 100)
	l.recordTx(wire.ClassBulk, 50)
	l.recordTx(wire.ClassInteractive, 10)
	l.recordRx(200)
	l.recordCreditStall(1500)
	l.recordCompression(3000, 800)
	l.sampleDepth(wire.ClassBulk, 7)
	l.sampleDepth(wire.ClassBulk, 3) // lower — must not lower the watermark

	s := l.snapshot([numClasses]int{})
	if got := s.TxBytes[wire.ClassBulk]; got != 150 {
		t.Fatalf("bulk tx_bytes = %d, want 150", got)
	}
	if got := s.TxFrames[wire.ClassBulk]; got != 2 {
		t.Fatalf("bulk tx_frames = %d, want 2", got)
	}
	if got := s.TxBytes[wire.ClassInteractive]; got != 10 {
		t.Fatalf("interactive tx_bytes = %d, want 10", got)
	}
	if s.RxBytes != 200 || s.RxFrames != 1 {
		t.Fatalf("rx = %d bytes / %d frames, want 200/1", s.RxBytes, s.RxFrames)
	}
	if s.CreditStalls != 1 || s.CreditWaitNs != 1500 {
		t.Fatalf("credit = %d stalls / %d ns, want 1/1500", s.CreditStalls, s.CreditWaitNs)
	}
	if s.RawBytes != 3000 || s.WireBytes != 800 {
		t.Fatalf("compression = %d raw / %d wire, want 3000/800", s.RawBytes, s.WireBytes)
	}
	if s.DepthHi[wire.ClassBulk] != 7 {
		t.Fatalf("bulk depth_hi = %d, want 7 (later lower sample must not reduce it)", s.DepthHi[wire.ClassBulk])
	}
}

// A non-blocking submit onto a full queue is a drop, and it's counted.
func TestSchedulerTrySubmitDropCounted(t *testing.T) {
	s := NewScheduler()
	cap := ClassQueueSize[wire.ClassBackground]
	for i := 0; i < cap; i++ {
		if !s.TrySubmit(frameOf(wire.ClassBackground, 5, 0)) {
			t.Fatalf("TrySubmit %d/%d unexpectedly refused before capacity", i, cap)
		}
	}
	if s.TrySubmit(frameOf(wire.ClassBackground, 5, 0)) {
		t.Fatalf("TrySubmit past capacity should refuse")
	}
	snap := s.StatsSnapshot()
	if snap.Dropped[wire.ClassBackground] != 1 {
		t.Fatalf("dropped = %d, want 1", snap.Dropped[wire.ClassBackground])
	}
	if snap.Depth[wire.ClassBackground] != uint64(cap) {
		t.Fatalf("depth = %d, want %d", snap.Depth[wire.ClassBackground], cap)
	}
	if snap.DepthHi[wire.ClassBackground] != uint64(cap) {
		t.Fatalf("depth_hi = %d, want %d", snap.DepthHi[wire.ClassBackground], cap)
	}
}

// A blocking Submit onto a full queue records the stall before it parks;
// a pre-canceled ctx lets us observe the count without a draining goroutine.
func TestSchedulerSubmitQueueFullCounted(t *testing.T) {
	s := NewScheduler()
	cap := ClassQueueSize[wire.ClassBackground]
	for i := 0; i < cap; i++ {
		if !s.TrySubmit(frameOf(wire.ClassBackground, 5, 0)) {
			t.Fatalf("fill %d/%d refused", i, cap)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Submit(ctx, frameOf(wire.ClassBackground, 5, 0)); err == nil {
		t.Fatalf("Submit onto full queue with canceled ctx should error")
	}
	if got := s.StatsSnapshot().QueueFull[wire.ClassBackground]; got != 1 {
		t.Fatalf("queue_full = %d, want 1", got)
	}
}
