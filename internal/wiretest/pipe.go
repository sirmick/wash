// Package wiretest provides shared test helpers for wash's wire,
// router, and sdk packages: an in-memory FrameTransport pair used in
// place of sockets and WebSockets when validating the spine.
//
// This package is imported only from _test.go files; it does not
// ship in any binary.
package wiretest

import (
	"io"
	"sync"

	"github.com/sirmick/wash/internal/wire"
)

// PipePair is a bidirectional in-memory channel of wash frames. Each
// end is a wire.FrameTransport, so router and SDK code can route
// frames between them as if they were real connections.
type PipePair struct {
	aToB chan wire.Frame
	bToA chan wire.Frame
	done chan struct{}
	once sync.Once
}

// NewPipePair returns a fresh pair with bounded queues large enough
// for the v0.0 handshake/asset/close flows without blocking.
func NewPipePair() *PipePair {
	return &PipePair{
		aToB: make(chan wire.Frame, 32),
		bToA: make(chan wire.Frame, 32),
		done: make(chan struct{}),
	}
}

// Close closes both endpoints. Idempotent.
func (p *PipePair) Close() { p.once.Do(func() { close(p.done) }) }

// EndA returns the A endpoint.
func (p *PipePair) EndA() wire.FrameTransport {
	return &pipeEnd{in: p.bToA, out: p.aToB, pp: p}
}

// EndB returns the B endpoint.
func (p *PipePair) EndB() wire.FrameTransport {
	return &pipeEnd{in: p.aToB, out: p.bToA, pp: p}
}

type pipeEnd struct {
	in  <-chan wire.Frame
	out chan<- wire.Frame
	pp  *PipePair
}

func (e *pipeEnd) ReadFrame() (wire.Frame, error) {
	select {
	case f := <-e.in:
		return f, nil
	case <-e.pp.done:
		return wire.Frame{}, io.EOF
	}
}

func (e *pipeEnd) WriteFrame(f wire.Frame) error {
	select {
	case e.out <- f:
		return nil
	case <-e.pp.done:
		return io.ErrClosedPipe
	}
}

func (e *pipeEnd) Close() error {
	e.pp.Close()
	return nil
}
