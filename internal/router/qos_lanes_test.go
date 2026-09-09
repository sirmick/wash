package router

import (
	"context"
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// laneFrame is a frame of n bytes in class c.
func laneFrame(c wire.Class, n int) wire.Frame {
	return wire.Frame{Flags: wire.FlagEnd, Channel: 9, Payload: make([]byte, n)}.
		WithClass(c)
}

// The gate this scheduler never had: while a lower lane is saturated, a
// frame a human is waiting on must be the NEXT thing out — not queued
// behind the backlog. This is the property the whole lane taxonomy
// exists to provide, and nothing asserted it.
func TestInteractiveOvertakesASaturatedBulkQueue(t *testing.T) {
	s := NewScheduler()
	defer s.Close()

	// Fill Bulk to capacity — a file transfer mid-flight.
	for i := 0; i < ClassQueueSize[wire.ClassBulk]; i++ {
		if !s.TrySubmit(laneFrame(wire.ClassBulk, 1024)) {
			t.Fatalf("could not fill bulk at %d", i)
		}
	}
	// A drag patch arrives behind all of it.
	if err := s.Submit(context.Background(), laneFrame(wire.ClassInteractive, 64)); err != nil {
		t.Fatal(err)
	}

	got, err := s.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Class() != wire.ClassInteractive {
		t.Errorf("first frame out is %v, want Interactive — the drag waits behind the backlog", got.Class())
	}
}

// Telemetry must never be able to delay what a human is waiting on. It
// used to ride Control, the highest lane, once a second.
func TestTelemetryCannotPreemptInteractive(t *testing.T) {
	s := NewScheduler()
	defer s.Close()

	s.SubmitTelemetry(laneFrame(telemetryClass, 512))
	if err := s.Submit(context.Background(), laneFrame(wire.ClassInteractive, 64)); err != nil {
		t.Fatal(err)
	}

	got, err := s.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Class() != wire.ClassInteractive {
		t.Errorf("telemetry (%v) went out ahead of the interactive frame", got.Class())
	}
}

// Lanes are only meaningful if the sizeable traffic is actually in the
// lower ones. A frame big enough to fill the send buffer must never be
// emitted on the interactive lane: the writer commits a whole frame
// before the scheduler gets another decision, so one big interactive
// frame is head-of-line blocking the priority order cannot undo.
func TestChunkCapBoundsHeadOfLineBlocking(t *testing.T) {
	if maxChunkBytes > 64*1024 {
		t.Errorf("maxChunkBytes = %d: a frame this size is a send buffer's worth of blocking", maxChunkBytes)
	}
	var got []int
	writeChunked(make([]byte, 5*maxChunkBytes+7), func(p []byte) error {
		got = append(got, len(p))
		return nil
	})
	if len(got) != 6 {
		t.Fatalf("chunks = %d, want 6", len(got))
	}
	for i, n := range got {
		if n > maxChunkBytes {
			t.Errorf("chunk %d is %d bytes, over the %d cap", i, n, maxChunkBytes)
		}
	}
	if got[len(got)-1] != 7 {
		t.Errorf("last chunk = %d, want the 7-byte remainder", got[len(got)-1])
	}
}

// An empty payload still needs its one (empty) frame: bundle transactions
// send Bind → data → Unbind, and a zero-length asset must not silently
// skip the data phase.
func TestWriteChunkedSendsAnEmptyPayloadOnce(t *testing.T) {
	n := 0
	writeChunked(nil, func(p []byte) error { n++; return nil })
	if n != 1 {
		t.Errorf("empty payload produced %d frames, want 1", n)
	}
}
