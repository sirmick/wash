package wire

import (
	"errors"
	"io"
	"sync"
)

// Handler processes a single inbound Frame on its registered channel.
// Returning a non-nil error stops Mux.Serve.
type Handler func(Frame) error

// Mux dispatches incoming frames to per-channel handlers.
//
// The wash router and SDK both demux on multiple channels (channel 0
// control + channel 1 event); Mux gives them a small reusable router
// without making the wire package opine about goroutines or queues.
// Handlers run synchronously on the calling goroutine — typically
// inside Serve.
type Mux struct {
	mu       sync.RWMutex
	handlers map[uint32]Handler
	fallback Handler
}

// NewMux returns an empty Mux.
func NewMux() *Mux {
	return &Mux{handlers: make(map[uint32]Handler)}
}

// On registers h for frames on the given channel id. A second On for
// the same channel replaces the previous handler.
func (m *Mux) On(channel uint32, h Handler) {
	m.mu.Lock()
	m.handlers[channel] = h
	m.mu.Unlock()
}

// SetFallback registers a handler invoked for channels with no
// registration. If unset, unknown frames are silently discarded; the
// router uses a fallback to relay them.
func (m *Mux) SetFallback(h Handler) {
	m.mu.Lock()
	m.fallback = h
	m.mu.Unlock()
}

// Dispatch routes a single frame to the registered handler.
func (m *Mux) Dispatch(f Frame) error {
	m.mu.RLock()
	h, ok := m.handlers[f.Channel]
	fb := m.fallback
	m.mu.RUnlock()
	if ok {
		return h(f)
	}
	if fb != nil {
		return fb(f)
	}
	return nil
}

// Serve reads frames from r until EOF or until a handler returns an
// error. It returns nil on clean EOF.
func (m *Mux) Serve(r io.Reader) error {
	for {
		f, err := DecodeFrame(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := m.Dispatch(f); err != nil {
			return err
		}
	}
}
