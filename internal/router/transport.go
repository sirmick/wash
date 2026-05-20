package router

import (
	"bytes"
	"context"
	"errors"

	"github.com/coder/websocket"
	"github.com/sirmick/wash/internal/wire"
)

// FrameTransport is an alias for wire.FrameTransport — the router
// uses it pervasively, so re-exporting keeps call sites readable.
type FrameTransport = wire.FrameTransport

// NewStreamTransport is re-exported from wire so existing call sites
// stay short.
var NewStreamTransport = wire.NewStreamTransport

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
