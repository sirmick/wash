package router

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/coder/websocket"
	"github.com/sirmick/wash/internal/wire"
)

// FrameTransport carries wash frames in both directions. Three
// implementations exist:
//
//   - StreamTransport over an io.ReadWriteCloser (the app-side
//     inherited-fd Unix socket; bytes are a length-prefixed series of
//     frames).
//   - WSTransport over a coder/websocket.Conn (the browser shell
//     transport; one wash frame per binary WS message per WIRE.md §1).
//   - The pipe-based transport in the loopback test (C8).
type FrameTransport interface {
	ReadFrame() (wire.Frame, error)
	WriteFrame(wire.Frame) error
	Close() error
}

// StreamTransport is the app-socket transport: a stream of
// length-prefixed wash frames.
type StreamTransport struct {
	rwc io.ReadWriteCloser
}

func NewStreamTransport(rwc io.ReadWriteCloser) *StreamTransport {
	return &StreamTransport{rwc: rwc}
}

func (s *StreamTransport) ReadFrame() (wire.Frame, error) {
	return wire.DecodeFrame(s.rwc)
}

func (s *StreamTransport) WriteFrame(f wire.Frame) error {
	return wire.EncodeFrame(s.rwc, f)
}

func (s *StreamTransport) Close() error {
	return s.rwc.Close()
}

// WSTransport is the shell transport over a WebSocket. Each binary WS
// message carries exactly one wash frame (WIRE.md §1). Text frames
// are rejected.
type WSTransport struct {
	ctx context.Context
	ws  *websocket.Conn
}

// NewWSTransport wraps ws. ctx scopes reads and writes; cancel it to
// unblock a stuck connection.
func NewWSTransport(ctx context.Context, ws *websocket.Conn) *WSTransport {
	return &WSTransport{ctx: ctx, ws: ws}
}

func (w *WSTransport) ReadFrame() (wire.Frame, error) {
	typ, data, err := w.ws.Read(w.ctx)
	if err != nil {
		return wire.Frame{}, err
	}
	if typ != websocket.MessageBinary {
		return wire.Frame{}, errors.New("wash ws: text message not allowed")
	}
	return wire.DecodeFrame(bytes.NewReader(data))
}

func (w *WSTransport) WriteFrame(f wire.Frame) error {
	var buf bytes.Buffer
	if err := wire.EncodeFrame(&buf, f); err != nil {
		return err
	}
	return w.ws.Write(w.ctx, websocket.MessageBinary, buf.Bytes())
}

func (w *WSTransport) Close() error {
	return w.ws.Close(websocket.StatusNormalClosure, "")
}
