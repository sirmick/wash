package sdk

import (
	"context"
	"errors"
	"fmt"

	"github.com/sirmick/wash/internal/wire"
)

// ErrWindowCreate wraps a router refusal of CreateWindow (e.g. the
// app lacks the "windows" capability, or hit the per-instance limit).
var ErrWindowCreate = errors.New("wash: window create failed")

// WindowOpts parameterizes CreateWindow. The zero value is a default
// toplevel; the router applies geometry defaults when W/H are zero.
type WindowOpts struct {
	Title  string
	W, H   uint32
	Role   string // "" (toplevel) | wire.WindowRoleToplevel | wire.WindowRolePopup
	Parent uint32 // owning window for a popup/transient
	MinW   uint32
	MinH   uint32
	MaxW   uint32
	MaxH   uint32
	// Element optionally overrides the custom-element tag the shell
	// mounts for this window. Empty inherits the instance's manifest
	// element (the common case). Use it to name a per-window decoder or
	// view (e.g. "wash-app-display" for a video surface).
	Element string
}

type windowCreateResult struct {
	win uint32
	err error
}

// CreateWindow asks the router for an additional window beyond this
// instance's primary one and blocks until the router replies with the
// allocated id (or refuses). Requires the "windows" capability
// (sdk.CapWindows). Open a per-window video channel with
// OpenChannelKind(win, ChannelKindVideo) once you have the id.
//
// MUST NOT be called from an SDK callback (OnFocus, OnAppMsg, …): those
// run on the read goroutine that delivers the reply, so calling there
// deadlocks. Hand off to a fresh goroutine if you're inside a callback.
func (c *Conn) CreateWindow(ctx context.Context, opts WindowOpts) (uint32, error) {
	reqID := c.nextReqID.Add(1)
	waitCh := make(chan windowCreateResult, 1)
	c.winCreateMu.Lock()
	c.pendingWindowCreate[reqID] = waitCh
	c.winCreateMu.Unlock()

	m := wire.EvtWindowCreate{
		T: wire.TEvtWindowCreate, ReqID: reqID,
		Role: opts.Role, ParentWin: opts.Parent, Title: opts.Title,
		W: opts.W, H: opts.H,
		MinW: opts.MinW, MinH: opts.MinH, MaxW: opts.MaxW, MaxH: opts.MaxH,
		Element: opts.Element,
	}
	if err := c.writeEvt(m); err != nil {
		c.winCreateMu.Lock()
		delete(c.pendingWindowCreate, reqID)
		c.winCreateMu.Unlock()
		return 0, err
	}

	select {
	case <-ctx.Done():
		c.winCreateMu.Lock()
		delete(c.pendingWindowCreate, reqID)
		c.winCreateMu.Unlock()
		return 0, ctx.Err()
	case r := <-waitCh:
		return r.win, r.err
	}
}

// DestroyWindow tears down a window previously returned by
// CreateWindow. Fire-and-forget; the router emits the session
// window.delete and closes any channels rooted at the window. Dropping
// the primary handshake window this way is a no-op (it dies with the
// instance).
func (c *Conn) DestroyWindow(win uint32) error {
	return c.writeEvt(wire.NewEvtWindowDestroy(win))
}

// resolveWindowCreate delivers a window.created / window.create.err
// outcome to the waiting CreateWindow caller. Returns false if none is
// waiting (the call was abandoned via ctx).
func (c *Conn) resolveWindowCreate(reqID uint64, win uint32, err error) bool {
	c.winCreateMu.Lock()
	waitCh, ok := c.pendingWindowCreate[reqID]
	if ok {
		delete(c.pendingWindowCreate, reqID)
	}
	c.winCreateMu.Unlock()
	if !ok {
		return false
	}
	waitCh <- windowCreateResult{win: win, err: err}
	return true
}

// windowCreateErrFromMsg builds the canonical CreateWindow error.
func windowCreateErrFromMsg(m wire.EvtWindowCreateErr) error {
	return fmt.Errorf("%w: %s: %s", ErrWindowCreate, m.Code, m.Msg)
}
