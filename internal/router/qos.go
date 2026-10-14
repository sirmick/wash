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

	"github.com/sirmick/wash/pkg/wire"
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
	// Background now carries multi-frame asset streams (wallpapers/fonts/
	// icons; docs/QOS.md tc reclass), so it needs more than the original
	// telemetry-only headroom or a single wallpaper would block the
	// producer on a full queue.
	wire.ClassBackground: 64,
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

	// onStall fires when Submit is about to BLOCK on a full queue —
	// the leading indicator for GH #23. A producer parked here is a
	// producer that will see ErrSchedulerClosed if the browser then
	// dies, so a log at this moment is the difference between "the
	// desktop died for no reason" and "the browser stopped reading 40
	// seconds earlier". Set by the ShellSession; nil in tests.
	onStall func(c wire.Class, depth, capacity int)

	// head holds, per class, a frame already received off that class's
	// queue but not yet handed out because a higher class turned out to
	// be non-empty (see pick). It is logically the front of its queue,
	// so FIFO within the class holds. Touched only by Next — one
	// consumer — so it needs no lock; Depth does not count it.
	head    [4]wire.Frame
	hasHead [4]bool

	// onEmpty is a test seam: it fires each time pick finds class c
	// empty, so a test can land frames at an exact point in the scan.
	// Nil in production.
	onEmpty func(c wire.Class)
}

// SetStallHook installs the saturation callback. Called once at setup,
// before any producer exists.
func (s *Scheduler) SetStallHook(fn func(c wire.Class, depth, capacity int)) {
	s.onStall = fn
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
	if s.onStall != nil {
		s.onStall(c, len(q), cap(q))
	}
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
	// Closed check on the fast path: after Close no consumer drains the
	// queues, so an enqueue here would strand the frame. Report it as
	// undelivered (same shape as a full queue) instead (REVIEW-DATAPATH F11).
	select {
	case <-s.closed:
		return false
	default:
	}
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
	// Closed check: after Close no consumer drains, so skip rather than
	// strand a telemetry frame in an undrained queue (REVIEW-DATAPATH F11).
	select {
	case <-s.closed:
		return
	default:
	}
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
// Ordering guarantee: a frame is never handed out ahead of a
// higher-priority frame whose Submit returned before its own began.
// That is what lets a Bind ride the Interactive ctrl lane while its
// data rides Bulk: the shell only starts a bundle/asset/panel
// accumulator on the Bind, so data that overtakes it is dropped
// (GH #8). The reverse — an Unbind overtaking its data — is expected
// and handled by completing on the Bind's Size.
//
// Neither a priority scan nor a multi-queue select is atomic: frames
// can land in a lane the scan already passed, and a select over
// several ready lanes picks at random. So pick re-checks every higher
// lane AFTER taking a frame; one found non-empty wins, and the taken
// frame is kept as its class's head rather than requeued (requeueing
// at the back would break FIFO — an earlier version did exactly that).
func (s *Scheduler) Next(ctx context.Context) (wire.Frame, error) {
	for {
		if f, ok := s.pick(); ok {
			return f, nil
		}
		// Every lane empty: block on any queue (or close/cancel). The
		// frame that wakes us becomes its class's head and goes back
		// through pick, which validates it against the higher lanes.
		var c wire.Class
		var f wire.Frame
		select {
		case f = <-s.queues[wire.ClassControl]:
			c = wire.ClassControl
		case f = <-s.queues[wire.ClassInteractive]:
			c = wire.ClassInteractive
		case f = <-s.queues[wire.ClassBulk]:
			c = wire.ClassBulk
		case f = <-s.queues[wire.ClassBackground]:
			c = wire.ClassBackground
		case <-s.closed:
			return wire.Frame{}, ErrSchedulerClosed
		case <-ctx.Done():
			return wire.Frame{}, ctx.Err()
		}
		s.head[c], s.hasHead[c] = f, true
	}
}

// pick returns the head of the highest-priority non-empty class, or
// false if every class is empty. A frame is returned only once every
// higher class has been seen empty after it was taken: the receive
// that took it happens after its Submit, which happens after any
// earlier Submit by the same producer, so that re-check observes it.
func (s *Scheduler) pick() (wire.Frame, bool) {
	for i := 0; i < len(drainOrder); i++ {
		c := drainOrder[i]
		if !s.fill(c) {
			if s.onEmpty != nil {
				s.onEmpty(c)
			}
			continue
		}
		if s.higherFilled(i) {
			i = -1 // rescan from the top; the taken frame stays as head
			continue
		}
		f := s.head[c]
		s.head[c], s.hasHead[c] = wire.Frame{}, false
		return f, true
	}
	return wire.Frame{}, false
}

// higherFilled reports whether any class ahead of drainOrder[i] now has
// a head frame, filling them as it goes.
func (s *Scheduler) higherFilled(i int) bool {
	for _, h := range drainOrder[:i] {
		if s.fill(h) {
			return true
		}
	}
	return false
}

// fill makes sure class c has a head frame, receiving one off its
// queue without blocking if needed. Reports whether c has one.
func (s *Scheduler) fill(c wire.Class) bool {
	if s.hasHead[c] {
		return true
	}
	select {
	case f := <-s.queues[c]:
		s.head[c], s.hasHead[c] = f, true
		return true
	default:
		return false
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
// Depths is the current per-class queue occupancy. Read at the moment a
// shell dies, it names who was blocked behind the departing browser: a
// saturated Interactive queue is the precondition for Submit blocking at
// all, and therefore for GH #23's "app killed by a browser" chain.
func (s *Scheduler) Depths() map[wire.Class]int {
	out := make(map[wire.Class]int, len(s.queues))
	for c, q := range s.queues {
		if q != nil {
			out[wire.Class(c)] = len(q)
		}
	}
	return out
}

// Cap is the configured capacity for a class, so a depth can be reported
// as a fraction rather than a bare number nobody can calibrate.
func (s *Scheduler) Cap(c wire.Class) int {
	if int(c) >= len(s.queues) || s.queues[c] == nil {
		return 0
	}
	return cap(s.queues[c])
}

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

// maxChunkBytes caps a single frame emitted by the router's chunked
// writers. The scheduler is preemptive BETWEEN frames and not inside
// one: the drain loop commits a whole frame to the socket before it
// looks at the queues again. So the largest frame any lane emits is the
// worst-case delay it can impose on every higher lane, and 256 KB — the
// old bundle chunk — was exactly one clamped send buffer of it, which
// is how a bundle download stuttered a drag no matter what the
// scheduler wanted. 32 KB is small enough that the writer yields often
// and large enough that per-frame overhead stays noise.
const maxChunkBytes = 32 * 1024

// writeChunked splits payload into maxChunkBytes frames and calls write
// for each, stopping at the first error. A zero-length payload still
// produces exactly one (empty) call: transactional flows send Bind →
// data → Unbind, and swallowing the data phase for an empty asset would
// make "nothing to send" indistinguishable from "channel closed early".
func writeChunked(payload []byte, write func([]byte) error) error {
	if len(payload) == 0 {
		return write(nil)
	}
	for off := 0; off < len(payload); off += maxChunkBytes {
		end := off + maxChunkBytes
		if end > len(payload) {
			end = len(payload)
		}
		if err := write(payload[off:end]); err != nil {
			return err
		}
	}
	return nil
}

// errReplayRefused stops a chunked replay at the first frame the
// scheduler refuses. The caller reports the whole replay as undelivered
// and leaves the channel marked behind, so the watchdog retries the
// snapshot from the ring rather than the FE keeping a partial one.
var errReplayRefused = errors.New("router: replay chunk refused")

// telemetryClass is where link-health pushes ride. Background, not
// Control: telemetry is the definition of best-effort, and describing
// the link must never be able to delay using it. It rode Control — the
// highest lane — once a second, which put a growing stats blob ahead of
// every drag frame.
const telemetryClass = wire.ClassBackground
