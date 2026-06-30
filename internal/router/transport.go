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

// readIdleTimeout reaps a shell connection the router has heard nothing
// from for this long. The FE heartbeats (wire.TShellPing) ~every 15s, so
// three missed pings (no keystrokes, no pings, no anything) means the
// socket is a zombie the OS never FIN'd — typically a laptop suspended
// with the lid shut, or a tab the kernel froze. Reaping it frees the
// session's resources and, more importantly, surfaces the disconnect in
// the logs instead of leaving a dead ShellSession pinned forever.
//
// The watchdog only ARMS once it has seen at least one ping, so an older
// FE build that predates the heartbeat (and so never pings) is never
// falsely reaped during an idle stretch — it simply keeps the legacy
// "detected only when written to" behaviour.
const readIdleTimeout = 45 * time.Second

// readIdleCheckInterval is how often the watchdog samples liveness.
const readIdleCheckInterval = 5 * time.Second

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
	// A graceful close writes a close frame and waits up to 5s for the
	// peer to echo one (coder/websocket's fixed handshake timeout). That
	// politeness is only worth paying for when the peer is still there:
	// once our context is cancelled we're tearing the session down (router
	// shutdown / idle reap), so skip the handshake and close immediately
	// rather than block the listener's shutdown join on a peer that's going
	// away — or, in tests, a peer that never speaks WebSocket at all.
	if w.ctx.Err() != nil {
		return w.ws.CloseNow()
	}
	return w.ws.Close(websocket.StatusNormalClosure, "")
}
