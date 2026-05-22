package sdk

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sirmick/wash/internal/wire"
)

// SendAppMsg sends an APP_MSG to the FE half (WIRE.md §9). data is
// passed through to CBOR verbatim — the format is app-private and
// the router never inspects it.
func (c *Conn) SendAppMsg(data any) error {
	return c.writeEvt(wire.NewEvtAppMsg(c.windowID, data))
}

// SendAppMsgTo dispatches data as an APP_MSG to a *different*
// instance — the router resolves the recipient and relays. The
// recipient is either {AppID:"com.wash.bulk"} (singleton sentinel —
// spawned on demand if not running) or {InstanceID:"i-5"} (direct).
// Used by apps that need to queue work in a system service, e.g.
// fm enqueueing a bulk-delete job into wash-bulk.
func (c *Conn) SendAppMsgTo(recipient wire.Recipient, data any) error {
	return c.writeEvt(wire.NewEvtAppMsgSendTo(recipient, data))
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

// PrepareSpawn asks the router to mint a pending-attach record for a
// child the *caller* will fork+exec itself (typical use: wash-priv
// launching a binary under sudo). The reply arrives asynchronously
// via OnPrepareSpawnResult, identified by the caller-chosen reqID.
//
// Requires the "prepare_spawn" capability. On success the caller
// receives (instance_id, attach_token, binary) and is responsible
// for exec'ing the binary with at least these env vars:
//
//	WASH_DISPLAY=<inherited>
//	WASH_PROTO=1
//	WASH_APP_ID=<the target app_id>
//	WASH_INSTANCE_ID=<minted instance_id>
//	WASH_ATTACH_TOKEN=<minted token>
//
// The dial-back from the child is matched by token; /proc/<pid>/exe
// is verified against the binary path before the attach is accepted.
func (c *Conn) PrepareSpawn(reqID uint64, appID string) error {
	return c.writeEvt(wire.NewEvtPrepareSpawn(reqID, appID))
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

// SaveState persists the app's FE state blob router-side. The router
// stores it keyed by this instance's id; on every (re)mount of a
// matching element the shell dispatches it as a `wash:state` event.
//
// state is JSON-marshalled; the schema is the app's own — the router
// never inspects it. CBOR-decoded values (which arrive at OnAppMsg
// as map[any]any) are normalized to JSON-marshalable shapes first.
func (c *Conn) SaveState(state any) error {
	data, err := json.Marshal(toJSONValue(state))
	if err != nil {
		return err
	}
	return c.writeEvt(wire.NewEvtAppStateSet(data))
}

// toJSONValue walks a CBOR-decoded value and rewrites it to be safe
// for json.Marshal. CBOR maps can have non-string keys; JSON cannot.
// Mirrors the router-side toJSON helper.
func toJSONValue(v any) any {
	switch x := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprint(k)
			}
			out[ks] = toJSONValue(vv)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = toJSONValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = toJSONValue(vv)
		}
		return out
	}
	return v
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
