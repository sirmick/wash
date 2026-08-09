package wire

import (
	"context"
	"errors"
	"io"
)

// ReadLoop reads frames from t and dispatches each to handle until
// either the transport closes (io.EOF → nil), the context cancels
// (returns ctx.Err()), or handle returns a non-nil error (returns
// that error).
//
// Reads run on a dedicated goroutine so ctx cancellation can interrupt
// a blocked read. Receiving on the done channel after ctx.Done means
// the read goroutine has observed cancellation or transport closure.
//
// Used by the router's per-app and per-shell loops, the app
// handshake, and the SDK's main event loop — every place wash needs
// "read frames until done, handling ctx and EOF."
func ReadLoop(ctx context.Context, t FrameTransport, handle func(Frame) error) error {
	type rr struct {
		f   Frame
		err error
	}
	ch := make(chan rr, 1)
	// done is closed when ReadLoop returns; the reader goroutine selects
	// its send against it so a result buffered in ch (which ReadLoop will
	// never drain after it returns) can't wedge the goroutine's next send
	// forever. Without this, a handler error / ctx cancel while one result
	// is already buffered leaks the goroutine and pins its frame — under
	// every app and shell session teardown (REVIEW-DATAPATH F9).
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			f, err := t.ReadFrame()
			select {
			case ch <- rr{f, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-ch:
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					return nil
				}
				return r.err
			}
			if err := handle(r.f); err != nil {
				return err
			}
		}
	}
}

// ReadOne reads exactly one frame from t, honoring ctx cancellation.
// Used by handshakes that need a single frame before entering a loop.
func ReadOne(ctx context.Context, t FrameTransport) (Frame, error) {
	type rr struct {
		f   Frame
		err error
	}
	ch := make(chan rr, 1)
	go func() {
		f, err := t.ReadFrame()
		ch <- rr{f, err}
	}()
	select {
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case r := <-ch:
		return r.f, r.err
	}
}
