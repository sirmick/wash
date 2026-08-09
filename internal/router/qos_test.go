package router

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

func frameOf(class wire.Class, ch uint32, tag byte) wire.Frame {
	return wire.Frame{Flags: wire.FlagEnd, Channel: ch, Payload: []byte{tag}}.WithClass(class)
}

// TestSchedulerFIFOWithinClass — frames of the same class come out
// in the order they went in.
func TestSchedulerFIFOWithinClass(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	ctx := context.Background()

	for i := byte(0); i < 5; i++ {
		if err := s.Submit(ctx, frameOf(wire.ClassInteractive, uint32(i), i)); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	for i := byte(0); i < 5; i++ {
		f, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("next %d: %v", i, err)
		}
		if f.Payload[0] != i {
			t.Fatalf("out of order: got tag %d at pos %d", f.Payload[0], i)
		}
	}
}

// TestSchedulerStrictPriorityFastPath — when frames of multiple
// classes are queued before any drain, the fast-path picks the
// highest priority class first.
func TestSchedulerStrictPriorityFastPath(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	ctx := context.Background()

	// Stage frames in reverse priority order to make sure ordering
	// is by class, not arrival time.
	frames := []wire.Frame{
		frameOf(wire.ClassBackground, 1, 'g'),
		frameOf(wire.ClassBulk, 2, 'b'),
		frameOf(wire.ClassInteractive, 3, 'i'),
		frameOf(wire.ClassControl, 4, 'c'),
	}
	for _, f := range frames {
		if err := s.Submit(ctx, f); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}

	want := []byte{'c', 'i', 'b', 'g'}
	for i, w := range want {
		f, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("next %d: %v", i, err)
		}
		if f.Payload[0] != w {
			t.Fatalf("pos %d: got %q want %q", i, f.Payload[0], w)
		}
	}
}

// TestSchedulerFIFOWithinClassSlowPath — same FIFO guarantee as the
// fast-path version, but exercised when Next is blocked in the slow
// path and a higher-priority frame arrives during the wait. A
// previous re-check optimization put the lower-priority frame back
// at the END of its queue, reordering items submitted later. This
// test catches that regression: it drains in slow-path mode while
// submitting in a strict order across both classes.
func TestSchedulerFIFOWithinClassSlowPath(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	ctx := context.Background()

	// Driver schedule: each cycle submits one Interactive frame,
	// then a Control frame "during" the consumer's slow-path wait.
	// Order seen by the consumer SHOULD be: I0, C0, I1, C1, I2, C2.
	// (Or any interleaving where each class's own sequence is FIFO.)
	const cycles = 20
	go func() {
		for i := byte(0); i < cycles; i++ {
			_ = s.Submit(ctx, frameOf(wire.ClassInteractive, 1, i))
			// Tiny delay so the consumer (Next) blocks before the
			// Control arrives — exercises the slow path.
			time.Sleep(1 * time.Millisecond)
			_ = s.Submit(ctx, frameOf(wire.ClassControl, 1, i))
			time.Sleep(1 * time.Millisecond)
		}
	}()

	var (
		interactives []byte
		controls     []byte
	)
	for len(interactives) < cycles || len(controls) < cycles {
		f, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		switch f.Class() {
		case wire.ClassInteractive:
			interactives = append(interactives, f.Payload[0])
		case wire.ClassControl:
			controls = append(controls, f.Payload[0])
		}
	}

	// Each class must drain in strict submit order.
	for i, b := range interactives {
		if b != byte(i) {
			t.Fatalf("interactive out of order at %d: got %d (full: %v)", i, b, interactives)
		}
	}
	for i, b := range controls {
		if b != byte(i) {
			t.Fatalf("control out of order at %d: got %d (full: %v)", i, b, controls)
		}
	}
}

// TestSchedulerControlAndBulkBothDrain — when both classes are
// queued concurrently, neither starves and no deadlock occurs. The
// stronger priority ordering invariant (Control always returns
// before Bulk when both are visible) is covered by the deterministic
// fast-path test above; this test catches regressions where the
// slow-path's re-check logic leaks frames.
func TestSchedulerControlAndBulkBothDrain(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	ctx := context.Background()

	const trials = 50
	for i := 0; i < trials; i++ {
		if err := s.Submit(ctx, frameOf(wire.ClassBulk, 1, 'b')); err != nil {
			t.Fatalf("submit bulk: %v", err)
		}
		go func() { _ = s.Submit(ctx, frameOf(wire.ClassControl, 1, 'c')) }()
		f1, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("next1: %v", err)
		}
		f2, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("next2: %v", err)
		}
		got := string([]byte{f1.Payload[0], f2.Payload[0]})
		if got != "cb" && got != "bc" {
			t.Fatalf("trial %d: unexpected sequence %q (frames leaked or duplicated)", i, got)
		}
	}
}

// TestSchedulerQueueFullBlocks — Submit on a full queue blocks until
// Next drains a slot.
func TestSchedulerQueueFullBlocks(t *testing.T) {
	// Shrink Bulk to 2 for this test so we don't have to enqueue
	// 64 frames to fill it.
	old := ClassQueueSize[wire.ClassBulk]
	ClassQueueSize[wire.ClassBulk] = 2
	defer func() { ClassQueueSize[wire.ClassBulk] = old }()

	s := NewScheduler()
	defer s.Close()
	ctx := context.Background()

	for i := byte(0); i < 2; i++ {
		if err := s.Submit(ctx, frameOf(wire.ClassBulk, 1, i)); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}

	// Third Submit should block — verify by racing it against a
	// short timer.
	done := make(chan error, 1)
	go func() {
		done <- s.Submit(ctx, frameOf(wire.ClassBulk, 1, 99))
	}()
	select {
	case err := <-done:
		t.Fatalf("Submit did not block on full queue; returned %v", err)
	case <-time.After(50 * time.Millisecond):
		// good — still blocked
	}

	// Drain one slot; Submit should now unblock.
	if _, err := s.Next(ctx); err != nil {
		t.Fatalf("next: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unblock submit returned: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Submit did not unblock after drain")
	}
}

// TestSchedulerTrySubmitFullReturnsFalse — TrySubmit must never block.
func TestSchedulerTrySubmitFullReturnsFalse(t *testing.T) {
	old := ClassQueueSize[wire.ClassBulk]
	ClassQueueSize[wire.ClassBulk] = 1
	defer func() { ClassQueueSize[wire.ClassBulk] = old }()

	s := NewScheduler()
	defer s.Close()

	if !s.TrySubmit(frameOf(wire.ClassBulk, 1, 'a')) {
		t.Fatalf("first TrySubmit should succeed")
	}
	if s.TrySubmit(frameOf(wire.ClassBulk, 1, 'b')) {
		t.Fatalf("TrySubmit on full queue should return false")
	}
}

// TestSchedulerSubmitAfterClose — after Close no consumer drains the
// queues, so the non-blocking submit paths must NOT enqueue a frame that
// would then be stranded (REVIEW-DATAPATH F11). TrySubmit reports false and
// SubmitTelemetry is a no-op; the queues stay empty.
func TestSchedulerSubmitAfterClose(t *testing.T) {
	s := NewScheduler()
	s.Close()

	if s.TrySubmit(frameOf(wire.ClassBulk, 1, 'a')) {
		t.Fatalf("TrySubmit after Close should return false")
	}
	s.SubmitTelemetry(frameOf(wire.ClassBackground, 1, 'b'))

	for _, c := range []wire.Class{wire.ClassControl, wire.ClassInteractive, wire.ClassBulk, wire.ClassBackground} {
		if d := s.Depth(c); d != 0 {
			t.Fatalf("class %v queue depth %d after post-Close submits, want 0 (frame stranded)", c, d)
		}
	}
}

// TestSchedulerCloseUnblocksSubmit — Close releases producers stuck
// on a full queue with ErrSchedulerClosed.
func TestSchedulerCloseUnblocksSubmit(t *testing.T) {
	old := ClassQueueSize[wire.ClassBulk]
	ClassQueueSize[wire.ClassBulk] = 1
	defer func() { ClassQueueSize[wire.ClassBulk] = old }()

	s := NewScheduler()
	ctx := context.Background()

	if err := s.Submit(ctx, frameOf(wire.ClassBulk, 1, 'a')); err != nil {
		t.Fatalf("first submit: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Submit(ctx, frameOf(wire.ClassBulk, 1, 'b')) }()
	// Let the goroutine block on the full queue.
	time.Sleep(20 * time.Millisecond)
	s.Close()

	select {
	case err := <-done:
		if !errors.Is(err, ErrSchedulerClosed) {
			t.Fatalf("got err %v, want ErrSchedulerClosed", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Close did not unblock Submit")
	}
}

// TestSchedulerCloseUnblocksNext — Close while Next is blocking
// returns ErrSchedulerClosed.
func TestSchedulerCloseUnblocksNext(t *testing.T) {
	s := NewScheduler()
	ctx := context.Background()
	done := make(chan error, 1)
	go func() {
		_, err := s.Next(ctx)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	s.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrSchedulerClosed) {
			t.Fatalf("got %v, want ErrSchedulerClosed", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Close did not unblock Next")
	}
}

// TestSchedulerContextCancelUnblocksSubmit — context cancellation
// path for Submit (used at shell-session teardown).
func TestSchedulerContextCancelUnblocksSubmit(t *testing.T) {
	old := ClassQueueSize[wire.ClassBulk]
	ClassQueueSize[wire.ClassBulk] = 1
	defer func() { ClassQueueSize[wire.ClassBulk] = old }()

	s := NewScheduler()
	defer s.Close()

	if err := s.Submit(context.Background(), frameOf(wire.ClassBulk, 1, 'a')); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Submit(ctx, frameOf(wire.ClassBulk, 1, 'b')) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("cancel did not unblock Submit")
	}
}

// TestSchedulerDepth — Depth reports the queued frame count.
func TestSchedulerDepth(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	ctx := context.Background()
	if d := s.Depth(wire.ClassInteractive); d != 0 {
		t.Fatalf("initial depth=%d", d)
	}
	_ = s.Submit(ctx, frameOf(wire.ClassInteractive, 1, 'a'))
	_ = s.Submit(ctx, frameOf(wire.ClassInteractive, 1, 'b'))
	if d := s.Depth(wire.ClassInteractive); d != 2 {
		t.Fatalf("depth after submits=%d, want 2", d)
	}
	_, _ = s.Next(ctx)
	if d := s.Depth(wire.ClassInteractive); d != 1 {
		t.Fatalf("depth after one drain=%d, want 1", d)
	}
}

// TestSchedulerBulkUnderInteractiveLoad — simulates Phase 6a's
// exit-criterion shape at scheduler granularity: while a producer
// hammers Bulk frames, Interactive frames overtake them.
//
// Submits 1000 Bulk frames and 10 Interactive frames concurrently;
// asserts that across the first ~10 Next() returns, the Interactive
// frames are all delivered. (At Bulk queue size 64 there should never
// be 1000 bulk frames waiting before an interactive can be heard.)
func TestSchedulerBulkUnderInteractiveLoad(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(2)

	// Bulk producer — limited so it doesn't run forever.
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = s.Submit(ctx, frameOf(wire.ClassBulk, 1, 'b'))
		}
	}()

	// Interactive producer — sneaks in 10 frames after a small
	// delay to let Bulk fill its queue.
	interactiveSubmitted := make(chan struct{})
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		for i := 0; i < 10; i++ {
			_ = s.Submit(ctx, frameOf(wire.ClassInteractive, 1, 'i'))
		}
		close(interactiveSubmitted)
	}()

	<-interactiveSubmitted

	// Drain frames, counting how many we see before all 10
	// Interactive frames have been delivered. The strict-priority
	// promise: once Interactive is queued, it drains before any
	// further Bulk. We allow a small tolerance for the few Bulk
	// frames that may have already been picked up before
	// Interactive arrived.
	interactiveCount := 0
	totalFramesUntilInteractiveDone := 0
	for interactiveCount < 10 {
		f, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		totalFramesUntilInteractiveDone++
		if f.Class() == wire.ClassInteractive {
			interactiveCount++
		}
		if totalFramesUntilInteractiveDone > 200 {
			t.Fatalf("interactive frames not draining ahead: only %d/10 after 200 drains", interactiveCount)
		}
	}
	// Sanity: at most ~tens of bulk frames should have leaked
	// through before all 10 Interactive were delivered.
	if totalFramesUntilInteractiveDone > 80 {
		t.Errorf("too many bulk frames before all interactive: %d drains for 10 interactive", totalFramesUntilInteractiveDone)
	}

	// Drain the rest so wg.Wait doesn't block forever.
	go func() {
		for {
			if _, err := s.Next(ctx); err != nil {
				return
			}
		}
	}()
	wg.Wait()
}
