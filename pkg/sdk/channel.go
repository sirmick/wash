package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/sirmick/wash/pkg/wire"
)

// RawChannel is a bidirectional byte stream on a dynamic wash channel.
// Bytes Write to the router; Read blocks until the router delivers
// more frames. Close half-closes the local side and notifies the
// router; ChannelClosed from the peer fully tears the channel down.
type RawChannel struct {
	id   uint32
	conn *Conn

	// writeClass is the QoS class stamped on outbound frames. Zero value
	// is ClassInteractive (the default for a generic channel). A "file"
	// channel sets ClassBulk so a bulk download yields to interactive
	// traffic (docs/QOS.md). Set once at open, before any Write.
	writeClass wire.Class

	queue  chan []byte
	closed chan struct{}

	mu       sync.Mutex
	buf      []byte // leftover bytes from a previous frame that did not fit p
	closeErr error  // set under mu on teardown
}

func newRawChannel(id uint32, conn *Conn) *RawChannel {
	return &RawChannel{
		id:     id,
		conn:   conn,
		queue:  make(chan []byte, 64),
		closed: make(chan struct{}),
	}
}

// ID returns the router-allocated channel id.
func (rc *RawChannel) ID() uint32 { return rc.id }

// Read blocks until at least one byte is available. Returns io.EOF
// after the peer (or local Close) tears the channel down. The error
// carries the router's reason if any.
func (rc *RawChannel) Read(p []byte) (int, error) {
	rc.mu.Lock()
	if len(rc.buf) > 0 {
		n := copy(p, rc.buf)
		rc.buf = rc.buf[n:]
		rc.mu.Unlock()
		return n, nil
	}
	rc.mu.Unlock()

	select {
	case b, ok := <-rc.queue:
		if !ok {
			return 0, io.EOF
		}
		n := copy(p, b)
		if n < len(b) {
			rc.mu.Lock()
			// Save the rest for the next Read.
			rest := make([]byte, len(b)-n)
			copy(rest, b[n:])
			rc.buf = append(rc.buf, rest...)
			rc.mu.Unlock()
		}
		return n, nil
	case <-rc.closed:
		rc.mu.Lock()
		err := rc.closeErr
		rc.mu.Unlock()
		if err != nil {
			return 0, err
		}
		return 0, io.EOF
	}
}

// Write sends p as one frame on this channel. Returns len(p) on
// success; short writes are not produced unless the underlying
// transport errors mid-write.
func (rc *RawChannel) Write(p []byte) (int, error) {
	select {
	case <-rc.closed:
		return 0, io.ErrClosedPipe
	default:
	}
	err := rc.conn.transport.WriteFrame(wire.Frame{
		Flags:   wire.FlagEnd,
		Channel: rc.id,
		Payload: p,
	}.WithClass(rc.writeClass))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close sends channel.close and marks the local side closed. The
// router's ChannelClosed completes teardown asynchronously.
func (rc *RawChannel) Close() error {
	select {
	case <-rc.closed:
		return nil
	default:
	}
	close(rc.closed)
	rc.conn.removeChannel(rc.id)
	return rc.conn.writeCtrl(wire.NewChannelClose(rc.id))
}

// deliver pushes bytes from the SDK's read goroutine into the queue.
// Non-blocking against close; data dropped if the channel is dead.
func (rc *RawChannel) deliver(b []byte) {
	select {
	case rc.queue <- b:
	case <-rc.closed:
	}
}

// teardown is called when ChannelClosed arrives or the conn dies.
// Idempotent.
func (rc *RawChannel) teardown(err error) {
	rc.mu.Lock()
	if rc.closeErr == nil {
		rc.closeErr = err
	}
	rc.mu.Unlock()
	select {
	case <-rc.closed:
	default:
		close(rc.closed)
	}
}

// ErrChannelOpen wraps router-side rejections so callers can match
// on errors.Is(...).
var ErrChannelOpen = errors.New("wash: channel open failed")

// OpenChannel asks the router to allocate a raw channel bound to this
// app's window. Returns an io.ReadWriteCloser; close it to release
// the channel.
//
// MUST NOT be called from an SDK callback (OnAppMsg, OnFocus, …):
// those run on the read goroutine that delivers the ChannelOpened
// reply. Calling from a callback deadlocks. Hand off to a fresh
// goroutine if you're inside a callback.
func (c *Conn) OpenChannel(ctx context.Context, windowID uint32) (*RawChannel, error) {
	return c.openChannelKind(ctx, windowID, wire.ChannelKindGeneric)
}

// OpenChannelKind is OpenChannel with an explicit channel kind hint
// (e.g. wire.ChannelKindVideo for a per-window pixel stream — the
// shell mounts its display decoder on a video channel instead of
// treating it as opaque bytes). Same callback/deadlock caveat as
// OpenChannel.
func (c *Conn) OpenChannelKind(ctx context.Context, windowID uint32, kind string) (*RawChannel, error) {
	return c.openChannelKind(ctx, windowID, kind)
}

// OpenChannelBulk opens a GENERIC raw channel whose output writes at Bulk
// class. Used by the PTY/terminal stream (docs/QOS.md: "raw channels default
// to Bulk at OPEN time") so a chatty terminal (a `yes`, a big `cat`) yields
// to interactive traffic and rides the credit / behind / resync machinery
// (docs/PTY_ROBUST.md Fix B) instead of sharing the 256-slot Interactive
// queue with window ops, app_msgs, and every other app's lifecycle traffic.
//
// Unlike OpenChannelFile the router KEEPS the credit ledger (generic kind),
// so the stream is credit-gated + resyncable — correct for a terminal, whose
// realigned scrollback snapshot recovers a suppressed stream. A binary blob
// with no reassembly recovery must NOT use this (the resync's realignReplay
// would corrupt it) — use OpenChannelFile for lossless bulk instead. Same
// callback/deadlock caveat as OpenChannel.
func (c *Conn) OpenChannelBulk(ctx context.Context, windowID uint32) (*RawChannel, error) {
	ch, err := c.openChannelKind(ctx, windowID, wire.ChannelKindGeneric)
	if err != nil {
		return nil, err
	}
	// Set before returning — no Write can race, the caller hasn't started
	// streaming yet.
	ch.writeClass = wire.ClassBulk
	return ch, nil
}

// OpenChannelFile opens a channel for a bulk app→FE file transfer (e.g.
// fm download). The router omits the credit ledger and the channel writes
// at Bulk class, so the transfer rides the lossless Bulk path: it yields
// to interactive traffic but is never frame-dropped (docs/QOS.md). Same
// callback/deadlock caveat as OpenChannel.
func (c *Conn) OpenChannelFile(ctx context.Context, windowID uint32) (*RawChannel, error) {
	return c.openChannelKind(ctx, windowID, wire.ChannelKindFile)
}

// openChannelKind is OpenChannel with an explicit channel kind hint
// for the router. Internal — bundle channels are the only non-
// generic kind today.
func (c *Conn) openChannelKind(ctx context.Context, windowID uint32, kind string) (*RawChannel, error) {
	reqID := c.nextReqID.Add(1)
	waitCh := c.pendingOpens.register(reqID)

	if err := c.writeCtrl(wire.NewChannelOpenKind(reqID, windowID, kind)); err != nil {
		c.pendingOpens.cancel(reqID)
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.pendingOpens.cancel(reqID)
		return nil, ctx.Err()
	case r := <-waitCh:
		// A file channel writes at Bulk so a download yields to interactive
		// traffic (the router opened it creditless → lossless Bulk path).
		if r.ch != nil && kind == wire.ChannelKindFile {
			r.ch.writeClass = wire.ClassBulk
		}
		return r.ch, r.err
	}
}

// channel lookup / registration used by dispatch.

func (c *Conn) registerChannel(rc *RawChannel) {
	c.chanMu.Lock()
	c.channels[rc.id] = rc
	c.chanMu.Unlock()
}

func (c *Conn) removeChannel(id uint32) {
	c.chanMu.Lock()
	delete(c.channels, id)
	c.chanMu.Unlock()
}

func (c *Conn) lookupChannel(id uint32) *RawChannel {
	c.chanMu.Lock()
	defer c.chanMu.Unlock()
	return c.channels[id]
}

// resolveOpen delivers the open's result to the waiting OpenChannel
// caller. Returns false if no caller is waiting (open was abandoned).
func (c *Conn) resolveOpen(reqID uint64, result openResult) bool {
	return c.pendingOpens.resolve(reqID, result)
}

// channelOpenErrFromMsg builds the canonical OpenChannel error.
func channelOpenErrFromMsg(m wire.ChannelOpenErr) error {
	return fmt.Errorf("%w: %s: %s", ErrChannelOpen, m.Code, m.Msg)
}
