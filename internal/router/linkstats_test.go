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
	l.recordDisplayTx(4096)
	l.recordDisplayTx(2048)
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
	if s.DisplayTxBytes != 6144 || s.DisplayTxFrames != 2 {
		t.Fatalf("display tx = %d bytes / %d frames, want 6144/2", s.DisplayTxBytes, s.DisplayTxFrames)
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

// Session running totals: each finished connection is folded into the
// router's banked totals (sum), the queue watermark takes the max, and the
// live connection adds on top via Plus (with the live instantaneous depth).
func TestLinkStatsSessionFold(t *testing.T) {
	var totals LinkStats
	totals.add(wire.LinkStatsSnapshot{
		TxBytes:         [4]uint64{0, 1000, 0, 0},
		Dropped:         [4]uint64{0, 2, 0, 0},
		DepthHi:         [4]uint64{0, 5, 0, 0},
		RxBytes:         300,
		CreditStalls:    1,
		DisplayTxBytes:  400,
		DisplayTxFrames: 4,
	})
	totals.add(wire.LinkStatsSnapshot{
		TxBytes:         [4]uint64{0, 500, 0, 0},
		Dropped:         [4]uint64{0, 1, 0, 0},
		DepthHi:         [4]uint64{0, 3, 0, 0}, // lower than 5 — watermark must stay 5
		RxBytes:         200,
		CreditStalls:    2,
		DisplayTxBytes:  200,
		DisplayTxFrames: 2,
	})
	banked := totals.snapshot([numClasses]int{})
	if banked.TxBytes[wire.ClassBulk] != 1500 {
		t.Fatalf("banked bulk tx = %d, want 1500", banked.TxBytes[wire.ClassBulk])
	}
	if banked.Dropped[wire.ClassBulk] != 3 {
		t.Fatalf("banked bulk drops = %d, want 3", banked.Dropped[wire.ClassBulk])
	}
	if banked.DepthHi[wire.ClassBulk] != 5 {
		t.Fatalf("banked bulk depth_hi = %d, want 5 (max not sum)", banked.DepthHi[wire.ClassBulk])
	}
	if banked.RxBytes != 500 || banked.CreditStalls != 3 {
		t.Fatalf("banked rx=%d stalls=%d, want 500/3", banked.RxBytes, banked.CreditStalls)
	}
	if banked.DisplayTxBytes != 600 || banked.DisplayTxFrames != 6 {
		t.Fatalf("banked display tx=%d frames=%d, want 600/6", banked.DisplayTxBytes, banked.DisplayTxFrames)
	}

	// Session total = banked + live current connection.
	live := wire.LinkStatsSnapshot{
		TxBytes:         [4]uint64{0, 250, 0, 0},
		Depth:           [4]uint64{0, 7, 0, 0},
		DisplayTxBytes:  100,
		DisplayTxFrames: 1,
	}
	sess := banked.Plus(live)
	if sess.TxBytes[wire.ClassBulk] != 1750 {
		t.Fatalf("session bulk tx = %d, want 1750", sess.TxBytes[wire.ClassBulk])
	}
	if sess.Depth[wire.ClassBulk] != 7 {
		t.Fatalf("session bulk depth = %d, want 7 (live instantaneous)", sess.Depth[wire.ClassBulk])
	}
	if sess.DisplayTxBytes != 700 || sess.DisplayTxFrames != 7 {
		t.Fatalf("session display tx=%d frames=%d, want 700/7", sess.DisplayTxBytes, sess.DisplayTxFrames)
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
