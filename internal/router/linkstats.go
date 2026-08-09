// Per-connection WS link-health counters (docs/QOS.md). Surfaces the
// "is the link healthy?" picture the boot splash and a future Settings →
// Network panel report: per-class throughput, queue saturation, credit
// stalls, and (once the asset path compresses) the wire-vs-raw ratio.
//
// All counters are monotonic since connect and collected with cheap
// atomics at seams that already exist on the egress hot path: the
// scheduler (Submit/TrySubmit), the FE-bound writer (drainLoop), and the
// per-channel credit ledger. The FE differences successive snapshots into
// instantaneous + peak rates, so the router keeps no rate state itself.

package router

import (
	"sync/atomic"

	"github.com/sirmick/wash/pkg/wire"
)

// numClasses is the QoS priority-class count (wire.Class is 0..3). Every
// per-class array is indexed by the class value directly:
// Interactive=0, Bulk=1, Background=2, Control=3.
const numClasses = 4

// LinkStats accumulates one connection's link-health counters. Safe for
// concurrent producers (the per-app reader goroutines, control emitters,
// and the single drain writer) — every field is an atomic.
type LinkStats struct {
	txBytes  [numClasses]atomic.Uint64 // payload bytes put on the wire (drainLoop)
	txFrames [numClasses]atomic.Uint64

	queueFull [numClasses]atomic.Uint64 // Submit had to block: class queue at capacity
	dropped   [numClasses]atomic.Uint64 // TrySubmit / try-bulk refused (FE behind)
	depthHi   [numClasses]atomic.Uint64 // per-class queue high-watermark

	creditStalls atomic.Uint64 // ChannelCredit.Reserve had to block (credit exhausted)
	creditWaitNs atomic.Uint64 // cumulative ns producers spent blocked on credit

	rxBytes  atomic.Uint64 // ingress raw payload bytes (FE → router)
	rxFrames atomic.Uint64

	rawBytes  atomic.Uint64 // pre-compression asset bytes  (set once step (c) lands)
	wireBytes atomic.Uint64 // post-compression asset bytes  (set once step (c) lands)

	displayTxBytes  atomic.Uint64 // video + video-popup payload bytes
	displayTxFrames atomic.Uint64
}

// discardLinkStats is a sink for ShellSessions constructed without a
// scheduler (isolated unit tests) so the record* helpers stay nil-safe.
var discardLinkStats = &LinkStats{}

// statsLink returns the live LinkStats for this session, or the discard
// sink when there's no scheduler (test harnesses).
func (s *ShellSession) statsLink() *LinkStats {
	if s.scheduler == nil {
		return discardLinkStats
	}
	return s.scheduler.Stats
}

func classIndex(c wire.Class) int {
	if int(c) < 0 || int(c) >= numClasses {
		// Unknown class is treated as Interactive — the safe default the
		// scheduler itself falls back to (QOS.md §3).
		return int(wire.ClassInteractive)
	}
	return int(c)
}

// recordTx counts one frame of n payload bytes leaving for the wire.
func (l *LinkStats) recordTx(c wire.Class, n int) {
	i := classIndex(c)
	l.txBytes[i].Add(uint64(n))
	l.txFrames[i].Add(1)
}

// recordQueueFull notes that Submit found class c's queue at capacity and
// had to block the producer (the upstream backpressure signal).
func (l *LinkStats) recordQueueFull(c wire.Class) { l.queueFull[classIndex(c)].Add(1) }

// recordDrop notes a non-blocking submit refused for class c (TrySubmit
// false, or the "FE behind → suppress" path in tryWriteRawBulk).
func (l *LinkStats) recordDrop(c wire.Class) { l.dropped[classIndex(c)].Add(1) }

// recordRx counts one inbound raw frame of n payload bytes.
func (l *LinkStats) recordRx(n int) {
	l.rxBytes.Add(uint64(n))
	l.rxFrames.Add(1)
}

// recordCreditStall notes one credit-exhaustion wait of ns nanoseconds.
func (l *LinkStats) recordCreditStall(ns int64) {
	l.creditStalls.Add(1)
	if ns > 0 {
		l.creditWaitNs.Add(uint64(ns))
	}
}

// recordCompression accumulates the raw vs on-the-wire byte counts for an
// asset/bundle transfer (step (c)).
func (l *LinkStats) recordCompression(raw, onWire int) {
	l.rawBytes.Add(uint64(raw))
	l.wireBytes.Add(uint64(onWire))
}

// recordDisplayTx counts one FE-bound wash-display video/popup frame.
func (l *LinkStats) recordDisplayTx(n int) {
	l.displayTxBytes.Add(uint64(n))
	l.displayTxFrames.Add(1)
}

// sampleDepth raises class c's high-watermark to d if d is larger.
func (l *LinkStats) sampleDepth(c wire.Class, d int) {
	if d <= 0 {
		return
	}
	i := classIndex(c)
	for {
		cur := l.depthHi[i].Load()
		if uint64(d) <= cur {
			return
		}
		if l.depthHi[i].CompareAndSwap(cur, uint64(d)) {
			return
		}
	}
}

// snapshot copies the live counters into a plain wire value (the type the
// link.stats message carries), folding in the instantaneous per-class
// queue depths the caller read from the scheduler.
func (l *LinkStats) snapshot(depth [numClasses]int) wire.LinkStatsSnapshot {
	var s wire.LinkStatsSnapshot
	for i := 0; i < numClasses; i++ {
		s.TxBytes[i] = l.txBytes[i].Load()
		s.TxFrames[i] = l.txFrames[i].Load()
		s.QueueFull[i] = l.queueFull[i].Load()
		s.Dropped[i] = l.dropped[i].Load()
		s.DepthHi[i] = l.depthHi[i].Load()
		if depth[i] > 0 {
			s.Depth[i] = uint64(depth[i])
		}
	}
	s.CreditStalls = l.creditStalls.Load()
	s.CreditWaitNs = l.creditWaitNs.Load()
	s.RxBytes = l.rxBytes.Load()
	s.RxFrames = l.rxFrames.Load()
	s.RawBytes = l.rawBytes.Load()
	s.WireBytes = l.wireBytes.Load()
	s.DisplayTxBytes = l.displayTxBytes.Load()
	s.DisplayTxFrames = l.displayTxFrames.Load()
	return s
}

// add folds a snapshot into these counters — used to accumulate each
// finished connection into the router's session-lifetime running totals.
// Cumulative fields sum; the queue-depth watermark takes the max.
func (l *LinkStats) add(s wire.LinkStatsSnapshot) {
	for i := 0; i < numClasses; i++ {
		l.txBytes[i].Add(s.TxBytes[i])
		l.txFrames[i].Add(s.TxFrames[i])
		l.queueFull[i].Add(s.QueueFull[i])
		l.dropped[i].Add(s.Dropped[i])
		l.sampleDepth(wire.Class(i), int(s.DepthHi[i]))
	}
	l.creditStalls.Add(s.CreditStalls)
	l.creditWaitNs.Add(s.CreditWaitNs)
	l.rxBytes.Add(s.RxBytes)
	l.rxFrames.Add(s.RxFrames)
	l.rawBytes.Add(s.RawBytes)
	l.wireBytes.Add(s.WireBytes)
	l.displayTxBytes.Add(s.DisplayTxBytes)
	l.displayTxFrames.Add(s.DisplayTxFrames)
}

func isDisplayChannelKind(kind string) bool {
	return kind == wire.ChannelKindVideo || kind == wire.ChannelKindVideoPopup
}
