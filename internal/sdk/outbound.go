package sdk

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/sirmick/wash/internal/wire"
)

// PrivAppID is wash-priv's reserved app id. Sender for the privilege
// helpers below; exposed so app code can recognise wash-priv replies
// without hard-coding the string.
const PrivAppID = "com.wash.priv"

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

// PrivSpawn asks wash-priv to launch the named wash app as root.
// One-line privilege escalation for app authors:
//
//	c.PrivSpawn("com.wash.term", "user wants a root shell")
//
// Fire-and-forget over the cross-app bus. wash-priv handles the
// queue UI, user approval, password prompt, sudo invocation, and
// PrepareSpawn. The router stamps your app's id + instance as the
// router-attested sender; wash-priv shows that in the approval row.
//
// Errors only on transport failure. The actual approve/reject /
// success result is async — wash-priv replies with {kind:"spawned"}
// or {kind:"rejected"} on this app's event channel; if you care,
// install OnAppMsgFrom and match on from.AppID == sdk.PrivAppID.
func (c *Conn) PrivSpawn(appID, reason string) error {
	return c.SendAppMsgTo(wire.Recipient{AppID: PrivAppID}, map[string]any{
		"kind":   "spawn",
		"req_id": newPrivReqID(),
		"app_id": appID,
		"reason": reason,
	})
}

// PrivRun asks wash-priv to run argv as root inside a fresh wash-term
// window. The user sees the command run in a real terminal; ideal
// for one-off `apt update` / `make install` style invocations
// triggered from a normal app's UI.
//
//	c.PrivRun([]string{"apt", "update"}, "from fm: refreshing")
//
// Same async / fire-and-forget shape as PrivSpawn.
func (c *Conn) PrivRun(argv []string, reason string) error {
	return c.SendAppMsgTo(wire.Recipient{AppID: PrivAppID}, map[string]any{
		"kind":   "run",
		"req_id": newPrivReqID(),
		"argv":   argv,
		"reason": reason,
	})
}

// PrivResult is what PrivRunInlineSync returns. Stdout / Stderr are
// the raw bytes the command produced (no encoding decoration); Exit
// is the wrapped command's exit code. Err is non-nil only for
// transport / handshake failures — a sudo "wrong password" or a
// rejected approval surfaces as Exit non-zero and a populated
// Stderr, not Err.
type PrivResult struct {
	Stdout []byte
	Stderr []byte
	Exit   int
	Err    error
}

// PrivRunInlineSync runs argv as root via wash-priv and BLOCKS until
// the command exits, returning everything in one struct. The
// one-liner for app code that needs the actual output:
//
//	r, _ := c.PrivRunInlineSync(ctx, []string{"whoami"}, "audit")
//	fmt.Println(string(r.Stdout)) // -> "root\n"
//
// Approval / password / audit still flow through wash-priv's FE.
//
// MUST NOT be called from an SDK callback (OnAppMsg, OnAppMsgFrom,
// OnFocus, …): the wash-priv reply streams arrive on the same read
// goroutine that's running the callback, so blocking it deadlocks.
// Spawn a goroutine if you're inside a callback.
func (c *Conn) PrivRunInlineSync(ctx context.Context, argv []string, reason string) (PrivResult, error) {
	if len(argv) == 0 {
		return PrivResult{}, fmt.Errorf("argv is empty")
	}
	reqID := newPrivReqID()
	call := &privCall{
		stdout: &byteBuf{},
		stderr: &byteBuf{},
		done:   make(chan PrivResult, 1),
	}
	c.privMu.Lock()
	if c.privPending == nil {
		c.privPending = make(map[string]*privCall)
	}
	c.privPending[reqID] = call
	c.privMu.Unlock()
	defer func() {
		c.privMu.Lock()
		delete(c.privPending, reqID)
		c.privMu.Unlock()
	}()

	if err := c.SendAppMsgTo(wire.Recipient{AppID: PrivAppID}, map[string]any{
		"kind":   "run_inline",
		"req_id": reqID,
		"argv":   argv,
		"reason": reason,
	}); err != nil {
		return PrivResult{Err: err}, err
	}
	select {
	case r := <-call.done:
		return r, r.Err
	case <-ctx.Done():
		return PrivResult{Err: ctx.Err()}, ctx.Err()
	}
}

// privCall is the in-flight state for one PrivRunInlineSync. The
// SDK's dispatchEvt path intercepts incoming app_msgs from wash-
// priv whose req_id matches a pending call here, accumulates bytes,
// and resolves done on priv.result.
type privCall struct {
	stdout *byteBuf
	stderr *byteBuf
	done   chan PrivResult
}

// byteBuf is a tiny mutex-guarded byte buffer. The stream callback
// pushes from the dispatch goroutine; the final result reads on the
// blocked caller's goroutine — both need synchronised access.
type byteBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (b *byteBuf) append(p []byte) {
	b.mu.Lock()
	b.buf = append(b.buf, p...)
	b.mu.Unlock()
}

func (b *byteBuf) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out
}

// tryConsumePrivReply looks at a CBOR-decoded app_msg payload from
// wash-priv. If its req_id matches a PrivRunInlineSync waiter,
// accumulates streamed bytes and resolves the call on priv.result.
// Returns true when the message was consumed — caller should NOT
// forward to OnAppMsgFrom.
func (c *Conn) tryConsumePrivReply(data any) bool {
	m, ok := data.(map[any]any)
	if !ok {
		return false
	}
	reqID, _ := m["req_id"].(string)
	if reqID == "" {
		return false
	}
	c.privMu.Lock()
	call, ok := c.privPending[reqID]
	c.privMu.Unlock()
	if !ok {
		return false
	}
	kind, _ := m["kind"].(string)
	switch kind {
	case "priv.stream":
		stream, _ := m["stream"].(string)
		b64, _ := m["bytes"].(string)
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) == 0 {
			return true
		}
		if stream == "stderr" {
			call.stderr.append(raw)
		} else {
			call.stdout.append(raw)
		}
		return true
	case "priv.result":
		exit := 0
		switch x := m["exit_code"].(type) {
		case int64:
			exit = int(x)
		case uint64:
			exit = int(x)
		case float64:
			exit = int(x)
		}
		errMsg, _ := m["error"].(string)
		r := PrivResult{
			Stdout: call.stdout.bytes(),
			Stderr: call.stderr.bytes(),
			Exit:   exit,
		}
		if errMsg != "" {
			r.Err = fmt.Errorf("wash-priv: %s", errMsg)
		}
		select {
		case call.done <- r:
		default:
		}
		return true
	case "rejected":
		reason, _ := m["reason"].(string)
		select {
		case call.done <- PrivResult{Exit: 1, Err: fmt.Errorf("wash-priv rejected: %s", reason)}:
		default:
		}
		return true
	}
	return false
}

// newPrivReqID returns a short hex id for the wash-priv req_id field.
// Doesn't need to be cryptographic; just unique inside the queue at
// any moment. The "p-" prefix makes BE logs easy to grep apart from
// wash-sudo's "ws-".
func newPrivReqID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "p-" + hex.EncodeToString(b[:])
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
