package router

import (
	"sync"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

func upsertAt(id uint32, x int32) wire.SessionPatch {
	return wire.SessionPatch{
		Op:     wire.SessionPatchWindowUpsert,
		Window: &wire.SessionWindow{WindowID: id, X: x},
	}
}

// sink collects flushed batches and can be told to refuse, standing in
// for a full Interactive queue.
type sink struct {
	mu      sync.Mutex
	batches [][]wire.SessionPatch
	refuse  bool
}

func (s *sink) flush(b []wire.SessionPatch) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse {
		return false
	}
	s.batches = append(s.batches, b)
	return true
}

func (s *sink) setRefuse(v bool) { s.mu.Lock(); s.refuse = v; s.mu.Unlock() }

func (s *sink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.batches) }

func (s *sink) last() []wire.SessionPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.batches) == 0 {
		return nil
	}
	return s.batches[len(s.batches)-1]
}

// When the writer keeps up, nothing is held back: one batch in, one
// batch straight out. Coalescing must be invisible on a healthy link.
func TestPatchesPassStraightThroughWhenTheWriterKeepsUp(t *testing.T) {
	s := &sink{}
	pc := newPatchCoalescer(s.flush)
	defer pc.stop()

	for x := int32(0); x < 5; x++ {
		pc.add([]wire.SessionPatch{upsertAt(7, x)})
	}
	if got := s.count(); got != 5 {
		t.Errorf("batches = %d, want 5 — patches were delayed on an idle writer", got)
	}
}

// The actual fix: while the writer is behind, a run of positions for one
// window collapses to the newest rather than queueing a backlog of where
// the pointer used to be.
func TestADragCollapsesToItsLatestPositionWhileBlocked(t *testing.T) {
	s := &sink{}
	pc := newPatchCoalescer(s.flush)
	defer pc.stop()

	s.setRefuse(true)
	for x := int32(0); x < 60; x++ {
		pc.add([]wire.SessionPatch{upsertAt(7, x)})
	}
	if got := s.count(); got != 0 {
		t.Fatalf("batches = %d while refusing, want 0", got)
	}

	s.setRefuse(false)
	pc.tryFlush()
	if got := s.count(); got != 1 {
		t.Fatalf("batches = %d, want 1 — the backlog was not collapsed", got)
	}
	batch := s.last()
	if len(batch) != 1 {
		t.Fatalf("batch carries %d patches, want 1", len(batch))
	}
	if got := batch[0].Window.X; got != 59 {
		t.Errorf("delivered x=%d, want 59 — a stale position won", got)
	}
}

// Distinct windows are independent and must all survive the collapse.
func TestCollapseIsPerWindow(t *testing.T) {
	s := &sink{}
	pc := newPatchCoalescer(s.flush)
	defer pc.stop()

	s.setRefuse(true)
	for x := int32(0); x < 10; x++ {
		pc.add([]wire.SessionPatch{upsertAt(1, x), upsertAt(2, x*2)})
	}
	s.setRefuse(false)
	pc.tryFlush()

	batch := s.last()
	if len(batch) != 2 {
		t.Fatalf("batch carries %d patches, want 2 (one per window)", len(batch))
	}
	byID := map[uint32]int32{}
	for _, p := range batch {
		byID[p.Window.WindowID] = p.Window.X
	}
	if byID[1] != 9 || byID[2] != 18 {
		t.Errorf("latest per window = %v, want map[1:9 2:18]", byID)
	}
}

// The hazard that makes delete share the window key space: a pending
// upsert must never be delivered after the delete that supersedes it, or
// the shell resurrects a closed window.
func TestDeleteSupersedesAPendingUpsert(t *testing.T) {
	s := &sink{}
	pc := newPatchCoalescer(s.flush)
	defer pc.stop()

	s.setRefuse(true)
	pc.add([]wire.SessionPatch{upsertAt(3, 100)})
	pc.add([]wire.SessionPatch{{Op: wire.SessionPatchWindowDelete, WindowID: 3}})
	s.setRefuse(false)
	pc.tryFlush()

	batch := s.last()
	if len(batch) != 1 {
		t.Fatalf("batch carries %d patches, want 1", len(batch))
	}
	if batch[0].Op != wire.SessionPatchWindowDelete {
		t.Errorf("op = %q, want the delete — a stale upsert would resurrect the window", batch[0].Op)
	}
}

// An op the coalescer has not been taught must never merge with another:
// unknown ops get their own key, so they queue rather than overwrite.
func TestUnknownOpsNeverMerge(t *testing.T) {
	s := &sink{}
	pc := newPatchCoalescer(s.flush)
	defer pc.stop()

	s.setRefuse(true)
	pc.add([]wire.SessionPatch{{Op: "something.new"}})
	pc.add([]wire.SessionPatch{{Op: "something.new"}})
	s.setRefuse(false)
	pc.tryFlush()

	if got := len(s.last()); got != 2 {
		t.Errorf("batch carries %d patches, want 2 — unknown ops were merged", got)
	}
}

// The tail of a drag must not be stranded when the queue was full at the
// moment the last patch arrived and nothing follows it.
func TestTheLastPatchOfADragIsRetried(t *testing.T) {
	s := &sink{}
	pc := newPatchCoalescer(s.flush)
	defer pc.stop()

	s.setRefuse(true)
	pc.add([]wire.SessionPatch{upsertAt(5, 42)})
	s.setRefuse(false)

	deadline := time.Now().Add(2 * time.Second)
	for s.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if s.count() == 0 {
		t.Fatal("the final patch was never retried")
	}
	if got := s.last()[0].Window.X; got != 42 {
		t.Errorf("retried x=%d, want 42", got)
	}
}
