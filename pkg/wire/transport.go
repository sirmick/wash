package wire

import (
	"io"
	"sync"
	"time"
)

// FrameTransport carries wash frames in both directions. The router
// and SDK both target this interface so transport-specific code (the
// app-side inherited-fd Unix socket, the shell-side WebSocket, an
// in-memory pipe used by tests) lives in exactly one place.
type FrameTransport interface {
	ReadFrame() (Frame, error)
	WriteFrame(Frame) error
	Close() error
}

// StreamTransport wraps an io.ReadWriteCloser whose stream is a
// length-prefixed series of wash frames — i.e. the inherited-fd Unix
// socket on the app side, and any net.Conn-like medium.
//
// Concurrent writers from different goroutines are safe: writeMu
// serialises EncodeFrame's two-Write sequence (header then payload)
// so frames can't interleave on the wire. The SDK's OpenChannel
// path spawns one goroutine per call and several may overlap when
// an app accepts back-to-back user actions (e.g. rapid Ctrl-Shift-T
// in wash-term) — without this lock, headers and payloads from
// different frames mix and the router parses garbage.
type StreamTransport struct {
	rwc     io.ReadWriteCloser
	writeMu sync.Mutex
}

// NewStreamTransport returns a FrameTransport over rwc.
func NewStreamTransport(rwc io.ReadWriteCloser) *StreamTransport {
	return &StreamTransport{rwc: rwc}
}

func (s *StreamTransport) ReadFrame() (Frame, error) {
	return DecodeFrame(s.rwc)
}

// ReadFrameRaw reads one frame and returns its complete wire bytes
// (header + payload) without decoding the payload — see DecodeFrameRaw.
// The relay's verbatim splice uses it to avoid a decode/re-encode per
// frame.
func (s *StreamTransport) ReadFrameRaw() ([]byte, error) {
	return DecodeFrameRaw(s.rwc)
}

func (s *StreamTransport) WriteFrame(f Frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return EncodeFrame(s.rwc, f)
}

// writeDeadliner is any stream that supports write deadlines — a net.Conn
// (the app-side inherited-fd unix socket, net.Pipe in tests) does; an
// in-memory io.Pipe does not.
type writeDeadliner interface {
	SetWriteDeadline(time.Time) error
}

// WriteFrameDeadline is WriteFrame with a bound on how long the underlying
// stream write may block. If the stream supports deadlines, a write that
// can't complete by `deadline` fails with an i/o timeout instead of blocking
// the caller forever; a stream that doesn't just writes normally. The router
// uses this so a wedged app BE (not reading its socket, SO_SNDBUF full) can't
// freeze the shell's single dispatch loop (REVIEW-DATAPATH F5/F6 /
// REVIEW-RECONNECT H3). The deadline is cleared after the write so it never
// leaks onto a later one.
func (s *StreamTransport) WriteFrameDeadline(f Frame, deadline time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if d, ok := s.rwc.(writeDeadliner); ok {
		_ = d.SetWriteDeadline(deadline)
		defer func() { _ = d.SetWriteDeadline(time.Time{}) }()
	}
	return EncodeFrame(s.rwc, f)
}

func (s *StreamTransport) Close() error {
	return s.rwc.Close()
}
