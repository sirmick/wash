package wire

import "io"

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
type StreamTransport struct {
	rwc io.ReadWriteCloser
}

// NewStreamTransport returns a FrameTransport over rwc.
func NewStreamTransport(rwc io.ReadWriteCloser) *StreamTransport {
	return &StreamTransport{rwc: rwc}
}

func (s *StreamTransport) ReadFrame() (Frame, error) {
	return DecodeFrame(s.rwc)
}

func (s *StreamTransport) WriteFrame(f Frame) error {
	return EncodeFrame(s.rwc, f)
}

func (s *StreamTransport) Close() error {
	return s.rwc.Close()
}
