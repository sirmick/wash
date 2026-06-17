package wire

import (
	"io"
	"sync"
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

func (s *StreamTransport) Close() error {
	return s.rwc.Close()
}
