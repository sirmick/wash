package router

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/coder/websocket"
	"github.com/sirmick/wash/internal/wire"
)

// wsWriteTimeout bounds a single FE-bound frame write (docs/PTY_ROBUST.md,
// Fix C). A client that accepts the TCP connection but never reads — a
// frozen tab, a network black hole — would otherwise block the shell's
// single drainLoop forever and hang every terminal on that connection.
// On timeout the write fails (coder/websocket also closes the conn), so
// the drainLoop closes the scheduler and tears the shell down; the FE's
// reconnect path then re-dials and reattaches — visible recovery, not a
// silent hang. Generous so a merely slow link never trips it; only a
// genuinely dead connection does (terminal frames are small; the largest
// FE-bound frames are 256 KiB bundle chunks).
const wsWriteTimeout = 30 * time.Second

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
	ctx          context.Context
	ws           *websocket.Conn
	writeTimeout time.Duration // 0 = no per-write deadline (tests)
}

// NewWSTransport wraps ws. ctx scopes reads and writes; cancel it to
// unblock a stuck connection. Each write also gets a wsWriteTimeout
// deadline so a dead-but-open client can't hang the drainLoop.
func NewWSTransport(ctx context.Context, ws *websocket.Conn) *WSTransport {
	return &WSTransport{ctx: ctx, ws: ws, writeTimeout: wsWriteTimeout}
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
	ctx := w.ctx
	if w.writeTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(w.ctx, w.writeTimeout)
		defer cancel()
	}
	return w.ws.Write(ctx, websocket.MessageBinary, buf.Bytes())
}

func (w *WSTransport) Close() error {
	return w.ws.Close(websocket.StatusNormalClosure, "")
}
