// Router-side QoS scheduler: a thin, transport-agnostic component
// that sits between frame producers (app readers, control message
// emitters, raw-channel splices) and the single FE-bound writer.
//
// Design (see docs/QOS.md §4):
//
//   producers ──Submit(class, frame)──▶ [queue per class]
//                                           │
//                                  strict priority drain
//                                           │
//                                   (consumer: writer goroutine)
//                                   for each f: transport.WriteFrame(f)
//
// Drain order: Control → Interactive → Bulk → Background.
// Strict priority is safe because Interactive volume is naturally
// small (mouse, keys, window events); it cannot starve Bulk in
// practice. When a class queue is full Submit blocks the producer —
// kernel SO_RCVBUF backpressure on app sockets then stops the
// upstream app.

package router

import (
	"context"
	"errors"
	"sync"

	"github.com/sirmick/wash/internal/wire"
)

// ClassQueueSize is the per-class outgoing frame queue capacity. The
// initial sizing balances memory (each slot holds one *wire.Frame
// pointer + header bytes) against latency under burst — too small
// and a normal interactive flurry blocks; too large and a runaway
// producer keeps buffering long after it should have backpressured.
//
// Tune in profiling. Initial guesses per QOS.md §4.
var ClassQueueSize = map[wire.Class]int{
	wire.ClassControl:     64,
	wire.ClassInteractive: 256,
	wire.ClassBulk:        64,
	wire.ClassBackground:  16,
}

// ErrSchedulerClosed is returned by Submit after Close. Producers
// treat it as a graceful "the transport is gone" — same shape as a
// closed-pipe write.
var ErrSchedulerClosed = errors.New("router: qos scheduler closed")

// Scheduler is a strict-priority frame scheduler for one consumer
// (the FE-bound writer goroutine). One Scheduler per ShellSession.
type Scheduler struct {
	// queues are per-class buffered channels. Submit blocks the
	// producer when the queue is full; the consumer drains in
	// strict priority order via Next.
	queues [4]chan wire.Frame

	// Stats accumulates link-health counters (queue saturation, drops,
	// throughput). Always non-nil after NewScheduler. The drain writer
	// records egress here too (see ShellSession.drainLoop).
	Stats *LinkStats

	closeOnce sync.Once
	closed    chan struct{}
}

// NewScheduler builds a Scheduler with per-class queue capacities
// from ClassQueueSize. The returned Scheduler must be Close()'d when
// the transport goes away to unblock any in-flight Submit calls.
func NewScheduler() *Scheduler {
	s := &Scheduler{closed: make(chan struct{}), Stats: &LinkStats{}}
	for c, n := range ClassQueueSize {
		if int(c) >= len(s.queues) {
			continue
		}
		s.queues[c] = make(chan wire.Frame, n)
	}
	return s
}

// Submit enqueues f for delivery at its class's priority. Blocks the
// caller if the class's queue is full — this is the upstream
// backpressure signal. ctx lets producers bail out (e.g. on shell
// teardown) instead of pinning a goroutine forever.
//
// The frame's class is read from f.Class() (the class bits in the
// flags byte set by the SDK or by router-side emitters).
func (s *Scheduler) Submit(ctx context.Context, f wire.Frame) error {
	c := f.Class()
	q := s.queues[c]
	if q == nil {
		// Defensive: an unknown class value has no queue. Treat as
		// Interactive — the default-safe fallback per QOS.md §3.
		c = wire.ClassInteractive
		q = s.queues[c]
	}
	// Fast path: enqueue without blocking, sampling the resulting depth
	// for the high-watermark.
	select {
	case q <- f:
		s.Stats.sampleDepth(c, len(q))
		return nil
	default:
	}
	// Queue at capacity — record the stall, then block (the upstream
	// backpressure signal). The count is the diagnostic: a climbing
	// queue_full means a producer is outrunning the FE on that class.
	s.Stats.recordQueueFull(c)
	select {
	case q <- f:
		s.Stats.sampleDepth(c, len(q))
		return nil
	case <-s.closed:
		return ErrSchedulerClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TrySubmit is the non-blocking variant: returns false if the queue
// is full. Useful for emitters that prefer drop to block (e.g.
// telemetry, future Background traffic).
func (s *Scheduler) TrySubmit(f wire.Frame) bool {
	c := f.Class()
	q := s.queues[c]
	if q == nil {
		c = wire.ClassInteractive
		q = s.queues[c]
	}
	select {
	case q <- f:
		s.Stats.sampleDepth(c, len(q))
		return true
	default:
		s.Stats.recordDrop(c)
		return false
	}
}

// SubmitTelemetry enqueues a router-originated telemetry frame
// (link.stats) without ever blocking and WITHOUT counting a drop on the
// per-class drop metric — telemetry must not pollute the app-traffic
// health figure (Background will carry real bulk traffic after the tc
// reclass). Best-effort: silently skipped if the class queue is full.
func (s *Scheduler) SubmitTelemetry(f wire.Frame) {
	q := s.queues[f.Class()]
	if q == nil {
		q = s.queues[wire.ClassInteractive]
	}
	select {
	case q <- f:
	default:
	}
}

// Next blocks until a frame is available at the highest currently-
// non-empty priority class, then returns it. Returns ErrSchedulerClosed
// after Close.
//
// Strict-priority order: Control → Interactive → Bulk → Background.
//
// The fast path scans queues in priority order with non-blocking
// receives, so when multiple frames are already queued, the highest
// priority always wins. The slow path waits on any queue; if it
// happens to fire on a lower-priority queue WHILE a higher-priority
// one is also arriving, the wakeup-loser is queued and will be
// served on the next call's fast path. This is "approximately
// strict" — at most one frame can be served "out of priority" per
// race window, and FIFO within each class is preserved.
//
// Deliberately we do NOT re-check higher-priority queues after a
// slow-path dequeue and put the lower-priority frame BACK on its
// queue: that would reorder the lower queue and violate FIFO. The
// approach below is both correct and easier to reason about.
func (s *Scheduler) Next(ctx context.Context) (wire.Frame, error) {
	// Fast-path: pick the highest-priority queue with a ready frame.
	for _, c := range drainOrder {
		select {
		case f := <-s.queues[c]:
			return f, nil
		default:
		}
	}
	// Slow-path: block on any queue (or close/cancel).
	select {
	case f := <-s.queues[wire.ClassControl]:
		return f, nil
	case f := <-s.queues[wire.ClassInteractive]:
		return f, nil
	case f := <-s.queues[wire.ClassBulk]:
		return f, nil
	case f := <-s.queues[wire.ClassBackground]:
		return f, nil
	case <-s.closed:
		return wire.Frame{}, ErrSchedulerClosed
	case <-ctx.Done():
		return wire.Frame{}, ctx.Err()
	}
}

// drainOrder is the strict priority order used by Next's fast path
// to pick which non-empty queue to drain first.
var drainOrder = []wire.Class{
	wire.ClassControl,
	wire.ClassInteractive,
	wire.ClassBulk,
	wire.ClassBackground,
}

// Close signals teardown. Pending Submit calls return
// ErrSchedulerClosed; Next returns ErrSchedulerClosed once the queues
// are drained (callers typically drain-then-stop or just stop).
// Idempotent.
func (s *Scheduler) Close() {
	s.closeOnce.Do(func() { close(s.closed) })
}

// Depth returns the current queued frame count for class c. Test-only
// hook; production code MUST NOT make scheduling decisions on this.
func (s *Scheduler) Depth(c wire.Class) int {
	if int(c) >= len(s.queues) || s.queues[c] == nil {
		return 0
	}
	return len(s.queues[c])
}

// StatsSnapshot returns a plain-value copy of the link-health counters,
// folding in the live per-class queue depths. Cheap; safe to call from
// the stats-emitter ticker concurrently with producers and the drainer.
func (s *Scheduler) StatsSnapshot() wire.LinkStatsSnapshot {
	var depth [numClasses]int
	for c := 0; c < numClasses; c++ {
		depth[c] = s.Depth(wire.Class(c))
	}
	return s.Stats.snapshot(depth)
}
