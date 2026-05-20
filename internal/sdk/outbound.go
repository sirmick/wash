package sdk

import (
	"context"

	"github.com/sirmick/wash/internal/wire"
)

// SendAppMsg sends an APP_MSG to the FE half (WIRE.md §9). data is
// passed through to CBOR verbatim — the format is app-private and
// the router never inspects it.
func (c *Conn) SendAppMsg(data any) error {
	return c.writeEvt(wire.NewEvtAppMsg(c.windowID, data))
}

// SetTitle requests a titlebar text change for this app's window.
// No-op for surface=desktop apps (the router will ignore a frame
// referencing window 0).
func (c *Conn) SetTitle(title string) error {
	return c.writeEvt(wire.NewEvtWindowSetTitle(c.windowID, title))
}

// ConfirmClose answers a window.close_requested. allow=true tears the
// window down; allow=false vetoes the close (e.g. an unsaved-changes
// dialog said "cancel").
func (c *Conn) ConfirmClose(win uint32, allow bool) error {
	return c.writeEvt(wire.NewEvtWindowConfirmClose(win, allow))
}

// SpawnRequest asks the router to launch another app by id. Requires
// the app's manifest to declare the "spawn" capability — otherwise
// the router replies with code=forbidden and OnSpawnResult is called
// with a non-nil error.
//
// The result arrives asynchronously via AppDef.OnSpawnResult; this
// call only schedules the request.
func (c *Conn) SpawnRequest(appID string) error {
	return c.writeEvt(wire.NewEvtSpawnRequest(appID))
}

// Notify asks the chrome to show a toast. level is one of "info",
// "warn", "error" (empty defaults to "info"). v0.1 has no capability
// gate; the router relays for any app.
func (c *Conn) Notify(title, body, level string) error {
	if level == "" {
		level = wire.NotifyLevelInfo
	}
	return c.writeEvt(wire.NewEvtNotify(title, body, level))
}

// ClipboardSet stores (mime, data) in the router-held clipboard and
// implicitly broadcasts an OnClipboardChanged to every other app.
// Re-setting with the same content is allowed.
func (c *Conn) ClipboardSet(mime string, data []byte) error {
	return c.writeEvt(wire.NewEvtClipboardSet(mime, data))
}

// ClipboardGet fetches the current clipboard contents. Blocks until
// the router replies or ctx cancels. Like OpenChannel, MUST NOT be
// called from an SDK dispatch callback — the response comes back on
// the same read goroutine and would deadlock.
func (c *Conn) ClipboardGet(ctx context.Context) (string, []byte, error) {
	reqID := c.nextReqID.Add(1)
	wait := make(chan clipboardResult, 1)
	c.clipMu.Lock()
	c.pendingClipboardGet[reqID] = wait
	c.clipMu.Unlock()

	if err := c.writeEvt(wire.NewEvtClipboardGet(reqID)); err != nil {
		c.clipMu.Lock()
		delete(c.pendingClipboardGet, reqID)
		c.clipMu.Unlock()
		return "", nil, err
	}
	select {
	case <-ctx.Done():
		c.clipMu.Lock()
		delete(c.pendingClipboardGet, reqID)
		c.clipMu.Unlock()
		return "", nil, ctx.Err()
	case r := <-wait:
		return r.mime, r.data, r.err
	}
}
