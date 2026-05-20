package sdk

import (
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
