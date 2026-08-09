package wire

import (
	"context"
	"errors"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"
)

type rrTest struct {
	f   Frame
	err error
}

// scriptedTransport hands out queued frames and, once drained, signals on
// blocked and parks until Close. This lets a test reproduce the exact F9
// teardown state: a frame buffered in ReadLoop's 1-slot channel while the
// reader is parked in ReadFrame, so that when the transport closes the
// reader wakes and tries a send into a full, never-drained channel.
type scriptedTransport struct {
	frames    chan rrTest
	blocked   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newScriptedTransport(queue ...rrTest) *scriptedTransport {
	s := &scriptedTransport{
		frames:  make(chan rrTest, len(queue)),
		blocked: make(chan struct{}, 1),
		closed:  make(chan struct{}),
	}
	for _, r := range queue {
		s.frames <- r
	}
	return s
}

func (s *scriptedTransport) ReadFrame() (Frame, error) {
	select {
	case r := <-s.frames:
		return r.f, r.err
	default:
	}
	// Queue drained: announce we are about to park, then block until a
	// frame arrives or the transport is closed.
	select {
	case s.blocked <- struct{}{}:
	default:
	}
	select {
	case r := <-s.frames:
		return r.f, r.err
	case <-s.closed:
		return Frame{}, io.ErrClosedPipe
	}
}

func (s *scriptedTransport) WriteFrame(Frame) error { return nil }
func (s *scriptedTransport) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

// TestReadLoopReaderExitsOnHandlerError reproduces REVIEW-DATAPATH F9. Two
// frames are staged; the reader buffers the first into ReadLoop's 1-slot
// channel, then buffers the second (once the main loop pulls the first),
// then parks in ReadFrame on a third read. The handler errors on the first
// frame, so ReadLoop returns leaving the second frame undrained. When we
// then close the transport, the parked ReadFrame wakes and the reader tries
// to send into the full channel — which without the done-channel guard
// blocks forever, leaking the goroutine and pinning the frame.
func TestReadLoopReaderExitsOnHandlerError(t *testing.T) {
	tr := newScriptedTransport(
		rrTest{f: Frame{Flags: FlagEnd, Channel: 1, Payload: []byte("a")}},
		rrTest{f: Frame{Flags: FlagEnd, Channel: 2, Payload: []byte("b")}},
	)

	boom := errors.New("boom")
	releaseHandle := make(chan struct{})

	runtime.GC()
	base := runtime.NumGoroutine()

	loopDone := make(chan error, 1)
	go func() {
		loopDone <- ReadLoop(context.Background(), tr, func(Frame) error {
			<-releaseHandle // hold the loop in handle(frame1) until the reader has parked
			return boom
		})
	}()

	// Wait until the reader has consumed both queued frames (frame2 now
	// buffered in ReadLoop's channel) and parked on the third read.
	select {
	case <-tr.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("reader never parked on a third read")
	}

	close(releaseHandle) // let handle(frame1) return boom → ReadLoop returns
	if err := <-loopDone; !errors.Is(err, boom) {
		t.Fatalf("ReadLoop = %v, want boom", err)
	}

	tr.Close() // wake the parked reader so it attempts the send into the full channel

	deadline := time.Now().Add(2 * time.Second)
	for {
		if runtime.NumGoroutine() <= base {
			return // reader exited — no leak
		}
		if time.Now().After(deadline) {
			t.Fatalf("reader goroutine did not exit: NumGoroutine %d > baseline %d",
				runtime.NumGoroutine(), base)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
