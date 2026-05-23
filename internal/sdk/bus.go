// Package sdk: Bus is the typed message-bus layer on top of Conn.
//
// Apps register handlers keyed by `kind`. The bus owns the request
// envelope (kind, id) and the reply shape (<kind>_ok / <kind>_err
// with id echo). Handlers express domain logic in plain typed Go
// signatures:
//
//   sdk.Handle(bus, "list", func(c *sdk.Conn, id string, req ListReq) (ListResp, error) { … })
//
// Wire shape:
//
//   Request:        {"kind":"list",     "id":"r1", "path":"/"}
//   Success reply:  {"kind":"list_ok",  "id":"r1", "path":"/", "entries":[…]}
//   Error reply:    {"kind":"list_err", "id":"r1", "code":"not_found", "msg":"…"}
//
// Handle covers FE→BE; HandleFrom covers cross-app BE→BE; HandleVoid
// is fire-and-forget (no reply). Emit pushes unsolicited events.
// Call performs a typed cross-app round-trip with req_id matching.
//
// Decoding strategy: incoming Data lands as CBOR's map[any]any. The
// bus re-marshals just the data field to CBOR bytes and unmarshals
// into the typed Req. One extra encode+decode per message, but no
// new dependencies and the wire types stay sharp.
//
// Bus is installed by NewBus, which chains its dispatch onto the
// existing OnAppMsg / OnAppMsgFrom callbacks. Messages whose kind
// has no registered handler fall through to the prior callbacks —
// so apps can adopt the bus incrementally without breaking the
// hand-rolled paths they still need.

package sdk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"sync"

	"github.com/fxamacker/cbor/v2"
	"github.com/sirmick/wash/internal/wire"
)

// Err is the canonical error type returned from handlers. Code maps
// to the wire envelope's `code` field; Msg to `msg`. Handlers may
// return any error — non-Err errors become code="internal".
type Err struct {
	Code string
	Msg  string
}

func (e Err) Error() string {
	if e.Msg == "" {
		return e.Code
	}
	return e.Code + ": " + e.Msg
}

// Errf is a printf-style Err constructor.
func Errf(code, format string, args ...any) Err {
	return Err{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Common error codes — apps are free to use their own strings, but
// these match the conventions across the existing wash codebase.
const (
	ErrBadRequest = "bad_request"
	ErrNotFound   = "not_found"
	ErrForbidden  = "forbidden"
	ErrInternal   = "internal"
	ErrIO         = "io"
	ErrTimeout    = "timeout"
)

// Bus is the per-Conn typed message bus. Construct with NewBus.
type Bus struct {
	conn *Conn

	mu       sync.RWMutex
	handlers map[string]handlerFn // exact-match kind → handler
	patterns []patternHandler     // prefix matches in registration order

	// pending awaits keyed by req_id for Bus.Call. Each waiter
	// gets the matching reply data (already CBOR-decoded as
	// map[any]any) on its one-shot channel.
	pendMu  sync.Mutex
	pending map[string]chan map[any]any

	// prevAppMsg / prevAppMsgFrom hold whatever callbacks the app
	// already installed. The bus chains to them for any kind it
	// doesn't recognise so adoption is incremental.
	prevAppMsg     func(c *Conn, win uint32, data any)
	prevAppMsgFrom func(c *Conn, win uint32, data any, from wire.Sender)
}

// handlerFn is the bus's internal handler shape. data is the
// CBOR-decoded message map; from is non-nil for cross-app deliveries.
type handlerFn func(c *Conn, win uint32, data map[any]any, from *wire.Sender)

type patternHandler struct {
	prefix string
	fn     func(c *Conn, kind string, data map[any]any)
}

// Conn returns the underlying Conn so callers can reach the rest of
// the SDK (SetTitle, SaveState, ClipboardSet, …) without threading
// it separately.
func (b *Bus) Conn() *Conn { return b.conn }

// NewBus installs a bus on c. Must be called once per Conn — typically
// from OnReady. The bus rewrites c's OnAppMsg / OnAppMsgFrom to route
// through itself, chaining to any previously-installed callbacks for
// kinds it doesn't handle.
func NewBus(c *Conn) *Bus {
	b := &Bus{
		conn:     c,
		handlers: map[string]handlerFn{},
		pending:  map[string]chan map[any]any{},
	}
	b.prevAppMsg = c.def.OnAppMsg
	b.prevAppMsgFrom = c.def.OnAppMsgFrom
	c.def.OnAppMsg = func(conn *Conn, win uint32, data any) {
		b.dispatch(conn, win, data, nil)
	}
	c.def.OnAppMsgFrom = func(conn *Conn, win uint32, data any, from wire.Sender) {
		b.dispatch(conn, win, data, &from)
	}
	return b
}

// dispatch routes a single incoming message: pending-reply waiters
// first (so Bus.Call's reply is consumed before any registered
// handler with a matching kind), then exact-match kinds, then
// pattern handlers, then fall through to the previous callback.
func (b *Bus) dispatch(c *Conn, win uint32, data any, from *wire.Sender) {
	m := asMap(data)
	if m == nil {
		// Not a map shape — bus has nothing to dispatch; fall back.
		b.fallback(c, win, data, from)
		return
	}
	// Pending-reply waiter (Bus.Call). Match on req_id; consume.
	if reqID, _ := m["req_id"].(string); reqID != "" {
		b.pendMu.Lock()
		ch, ok := b.pending[reqID]
		if ok {
			delete(b.pending, reqID)
		}
		b.pendMu.Unlock()
		if ok {
			select {
			case ch <- m:
			default:
			}
			return
		}
	}
	kind, _ := m["kind"].(string)
	if kind != "" {
		b.mu.RLock()
		h, ok := b.handlers[kind]
		b.mu.RUnlock()
		if ok {
			h(c, win, m, from)
			return
		}
		// Pattern handlers (prefix). Used by wash-edit's cmd.*
		// passthrough. Only applies to own-FE messages.
		if from == nil {
			b.mu.RLock()
			patterns := b.patterns
			b.mu.RUnlock()
			for _, p := range patterns {
				if strings.HasPrefix(kind, p.prefix) {
					p.fn(c, kind, m)
					return
				}
			}
		}
	}
	b.fallback(c, win, data, from)
}

// fallback hands the message to whichever OnAppMsg / OnAppMsgFrom
// callback the app installed before the bus was created. Lets apps
// adopt the bus piecemeal.
func (b *Bus) fallback(c *Conn, win uint32, data any, from *wire.Sender) {
	if from != nil {
		if b.prevAppMsgFrom != nil {
			b.prevAppMsgFrom(c, win, data, *from)
			return
		}
		if b.prevAppMsg != nil {
			b.prevAppMsg(c, win, data)
		}
		return
	}
	if b.prevAppMsg != nil {
		b.prevAppMsg(c, win, data)
	}
}

// register installs h under kind. Panics if kind is already
// registered — programmer error to double-register.
func (b *Bus) register(kind string, h handlerFn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.handlers[kind]; exists {
		panic(fmt.Sprintf("sdk.Bus: kind %q already registered", kind))
	}
	b.handlers[kind] = h
}

// HandlePattern registers a prefix handler. Matched messages get the
// raw CBOR map plus the kind string. Used for wash-edit's "cmd.*"
// transparent FE→BE→FE pass-through. Only applies to own-FE messages
// (cross-app messages always have a typed kind contract).
func (b *Bus) HandlePattern(prefix string, fn func(c *Conn, kind string, data map[any]any)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.patterns = append(b.patterns, patternHandler{prefix: prefix, fn: fn})
}

// appID returns this bus's app id for log messages. Reaches through
// the Conn to the manifest. Falls back to "?" if unavailable so logs
// keep flowing under degenerate (test) wiring.
func (b *Bus) appID() string {
	if b.conn != nil && b.conn.def != nil && b.conn.def.Manifest.ID != "" {
		return b.conn.def.Manifest.ID
	}
	return "?"
}

// logDecodeErr formats the "bus decode failed" log line with enough
// context to identify which message broke. The previous one-liner
// hid the app, sender, id, and offending payload — enough on its own
// to make every decode error a puzzle.
//
//	bus: <app-id> decode <kind> id=<id> from=<src>: <err>
//	  data: {…trimmed payload…}
//
// payload is rendered with sdk.AsMap (string-keyed) + json marshal
// + a 240-char cap so a noisy field doesn't fill the log.
func (b *Bus) logDecodeErr(kind, id string, from *wire.Sender, data map[any]any, err error) {
	src := "FE"
	if from != nil {
		src = fmt.Sprintf("%s/%s", from.AppID, from.InstanceID)
	}
	log.Printf("bus: %s decode %s id=%q from=%s: %v\n  data: %s",
		b.appID(), kind, id, src, err, truncJSON(data, 240))
}

// truncJSON marshals v to a compact JSON string for diagnostic
// output, capped at max chars (with "…" suffix on overflow).
// Best-effort — on marshal failure it falls back to fmt.Sprint.
func truncJSON(v any, max int) string {
	out, err := json.Marshal(AsMap(v))
	if err != nil {
		s := fmt.Sprint(v)
		if len(s) > max {
			return s[:max] + "…"
		}
		return s
	}
	if len(out) > max {
		return string(out[:max]) + "…"
	}
	return string(out)
}

// Handle is the typed request/response registration for FE→BE
// messages. Req and Resp are arbitrary structs; the bus decodes
// the inbound payload into Req (re-marshalling to CBOR for a clean
// typed decode) and ships the returned Resp wrapped in a
// <kind>_ok envelope with id echoed. Errors map to <kind>_err.
//
// The id parameter is extracted from data["id"] before decoding so
// handlers don't need to thread it through their Req struct (though
// they may put an ID field there if they want it round-tripped).
func Handle[Req, Resp any](b *Bus, kind string, fn func(c *Conn, id string, req Req) (Resp, error)) {
	b.register(kind, func(c *Conn, _ uint32, data map[any]any, _ *wire.Sender) {
		id, _ := data["id"].(string)
		var req Req
		if err := decodeInto(data, &req); err != nil {
			b.logDecodeErr(kind, id, nil, data, err)
			b.sendErr(c, kind, id, ErrBadRequest, err.Error(), nil)
			return
		}
		resp, err := fn(c, id, req)
		if err != nil {
			b.sendErr(c, kind, id, codeOf(err), msgOf(err), nil)
			return
		}
		b.sendOk(c, kind, id, resp, nil)
	})
}

// HandleVoid registers a fire-and-forget handler — no reply on
// success. Errors are logged BE-side; the FE doesn't get an envelope
// because it didn't ask for one. Use for save_state, clipboard_set,
// and other "do this, no answer needed" messages.
func HandleVoid[Req any](b *Bus, kind string, fn func(c *Conn, id string, req Req) error) {
	b.register(kind, func(c *Conn, _ uint32, data map[any]any, _ *wire.Sender) {
		id, _ := data["id"].(string)
		var req Req
		if err := decodeInto(data, &req); err != nil {
			b.logDecodeErr(kind, id, nil, data, err)
			return
		}
		if err := fn(c, id, req); err != nil {
			log.Printf("bus: %s %s id=%q: %v", b.appID(), kind, id, err)
		}
	})
}

// HandleFrom is Handle for cross-app messages. The `from` parameter
// is router-attested — apps that care about caller identity
// (wash-priv) must use this rather than Handle.
//
// Replies route back to the caller via SendAppMsgTo on the
// originating instance. The bus stamps req_id automatically on the
// reply if the request carried one (Call() uses req_id; legacy
// callers that used id also work because the bus copies both
// fields onto the reply for compatibility).
func HandleFrom[Req, Resp any](b *Bus, kind string, fn func(c *Conn, id string, req Req, from wire.Sender) (Resp, error)) {
	b.register(kind, func(c *Conn, _ uint32, data map[any]any, from *wire.Sender) {
		if from == nil {
			// Own-FE message with the same kind as a cross-app
			// handler. Skip: HandleFrom is by definition for
			// cross-app traffic.
			return
		}
		id, reqID := requestIDs(data)
		var req Req
		if err := decodeInto(data, &req); err != nil {
			b.logDecodeErr(kind, id, from, data, err)
			b.sendErr(c, kind, id, ErrBadRequest, err.Error(), &replyTo{from: *from, reqID: reqID})
			return
		}
		resp, err := fn(c, id, req, *from)
		if err != nil {
			b.sendErr(c, kind, id, codeOf(err), msgOf(err), &replyTo{from: *from, reqID: reqID})
			return
		}
		b.sendOk(c, kind, id, resp, &replyTo{from: *from, reqID: reqID})
	})
}

// HandleFromVoid is HandleFrom for fire-and-forget cross-app messages
// (no reply expected). Used by wash-priv's stdin/cancel/disconnect
// kinds — they're stream control, not request/response.
func HandleFromVoid[Req any](b *Bus, kind string, fn func(c *Conn, id string, req Req, from wire.Sender) error) {
	b.register(kind, func(c *Conn, _ uint32, data map[any]any, from *wire.Sender) {
		if from == nil {
			return
		}
		id := requestIDOnly(data)
		var req Req
		if err := decodeInto(data, &req); err != nil {
			b.logDecodeErr(kind, id, from, data, err)
			return
		}
		if err := fn(c, id, req, *from); err != nil {
			log.Printf("bus: %s %s id=%q from=%s/%s: %v", b.appID(), kind, id, from.AppID, from.InstanceID, err)
		}
	})
}

// Emit pushes an unsolicited message to the app's FE. Use for events
// the FE subscribes to (req.update, snapshot, tab_opened, …).
// payload is anything CBOR-marshalable; the bus stamps the kind.
func (b *Bus) Emit(kind string, payload any) error {
	out, err := envelope(kind, "", payload)
	if err != nil {
		return err
	}
	return b.conn.SendAppMsg(out)
}

// EmitTo pushes an unsolicited message to a specific other app. Same
// shape as Emit but addressed via SendAppMsgTo.
func (b *Bus) EmitTo(recipient wire.Recipient, kind string, payload any) error {
	out, err := envelope(kind, "", payload)
	if err != nil {
		return err
	}
	return b.conn.SendAppMsgTo(recipient, out)
}

// Call performs a typed cross-app request, awaiting the matching
// reply. The bus mints a req_id, ships the request via SendAppMsgTo,
// installs a one-shot waiter, and decodes the matching reply into
// *resp.
//
// The reply is matched by req_id — the recipient's HandleFrom path
// echoes it automatically. Timeout via ctx.
//
// MUST NOT be called from an SDK callback: the reply arrives on the
// same read goroutine that's running the callback, so blocking it
// deadlocks. Spawn a goroutine first if you're inside a handler.
func Call[Req, Resp any](ctx context.Context, b *Bus, recipient wire.Recipient, kind string, req Req, resp *Resp) error {
	reqID, err := mintReqID()
	if err != nil {
		return err
	}
	ch := make(chan map[any]any, 1)
	b.pendMu.Lock()
	b.pending[reqID] = ch
	b.pendMu.Unlock()
	defer func() {
		b.pendMu.Lock()
		delete(b.pending, reqID)
		b.pendMu.Unlock()
	}()

	out, err := envelope(kind, "", req)
	if err != nil {
		return err
	}
	out["req_id"] = reqID
	if err := b.conn.SendAppMsgTo(recipient, out); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case m, ok := <-ch:
		if !ok {
			return errors.New("bus.Call: cancelled")
		}
		// Check for an error envelope (_err kind suffix).
		if k, _ := m["kind"].(string); strings.HasSuffix(k, "_err") {
			code, _ := m["code"].(string)
			msg, _ := m["msg"].(string)
			if code == "" {
				code = ErrInternal
			}
			return Err{Code: code, Msg: msg}
		}
		if resp != nil {
			if err := decodeInto(m, resp); err != nil {
				return fmt.Errorf("decode reply: %w", err)
			}
		}
		return nil
	}
}

// replyTo carries the addressing fields needed to ship a reply to a
// cross-app caller — the from instance and the req_id we should
// echo. Nil for own-FE replies (those go via SendAppMsg).
type replyTo struct {
	from  wire.Sender
	reqID string
}

// sendOk builds the <kind>_ok envelope and ships it.
func (b *Bus) sendOk(c *Conn, kind, id string, resp any, to *replyTo) {
	out, err := envelope(kind+"_ok", id, resp)
	if err != nil {
		log.Printf("bus: %s_ok encode: %v", kind, err)
		return
	}
	b.ship(c, out, to)
}

// sendErr builds the <kind>_err envelope and ships it.
func (b *Bus) sendErr(c *Conn, kind, id, code, msg string, to *replyTo) {
	out := map[string]any{
		"kind": kind + "_err",
		"code": code,
		"msg":  msg,
	}
	if id != "" {
		out["id"] = id
	}
	b.ship(c, out, to)
}

// ship is the addressing branch: own-FE via SendAppMsg, cross-app via
// SendAppMsgTo with the from instance as the recipient.
func (b *Bus) ship(c *Conn, payload map[string]any, to *replyTo) {
	if to != nil {
		if to.reqID != "" {
			payload["req_id"] = to.reqID
		}
		// Address by InstanceID so the reply always lands on the
		// specific caller, not a sibling singleton instance.
		_ = c.SendAppMsgTo(wire.Recipient{InstanceID: to.from.InstanceID}, payload)
		return
	}
	_ = c.SendAppMsg(payload)
}

// envelope wraps payload in {kind, id?, ...payload fields} as a
// map[string]any. payload may be a struct (re-marshaled through
// CBOR to extract its fields) or already a map.
//
// id precedence: an explicit `id` arg always wins; otherwise an "id"
// field in the payload passes through. Lets Emit("...", {"id": ...})
// preserve the id so control-socket / FE await-matchers can correlate
// the reply, while sendOk/sendErr (which thread id via the arg) keep
// working unchanged.
func envelope(kind, id string, payload any) (map[string]any, error) {
	out := map[string]any{"kind": kind}
	if id != "" {
		out["id"] = id
	}
	if payload == nil {
		return out, nil
	}
	// Fast path: payload is already a map[string]any. Copy its
	// keys onto out without going through CBOR.
	if m, ok := payload.(map[string]any); ok {
		for k, v := range m {
			if k == "kind" {
				continue
			}
			if k == "id" && id != "" {
				continue
			}
			out[k] = v
		}
		return out, nil
	}
	// Generic path: re-marshal through CBOR, walk into out.
	buf, err := cbor.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("envelope marshal: %w", err)
	}
	var fields map[string]any
	if err := cbor.Unmarshal(buf, &fields); err != nil {
		return nil, fmt.Errorf("envelope unmarshal: %w", err)
	}
	for k, v := range fields {
		if k == "kind" {
			continue
		}
		if k == "id" && id != "" {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// decodeInto re-marshals a CBOR-decoded map into typed dst by going
// through CBOR bytes. Slow path (one encode + one decode) but
// zero-dep and type-correct.
//
// Nulls are stripped before remarshal. A JSON `null` (which the FE
// commonly sends for "no value here") becomes a CBOR null primitive,
// which fxamacker/cbor refuses to decode into a plain int / string /
// bool field. Treating null as "field absent" lets the zero value
// stand — same semantics every Go programmer expects.
func decodeInto(data any, dst any) error {
	buf, err := cbor.Marshal(stripNulls(data))
	if err != nil {
		return fmt.Errorf("decode marshal: %w", err)
	}
	if err := cbor.Unmarshal(buf, dst); err != nil {
		return fmt.Errorf("decode unmarshal: %w", err)
	}
	return nil
}

// stripNulls drops nil values from maps and slices recursively.
// Used by decodeInto so a JSON null on the wire doesn't fail the
// typed CBOR decode into primitive Go fields (int/string/bool/…).
// Map entries whose VALUE is nil are removed; nested maps/slices
// recurse. Non-nil leaves pass through unchanged.
func stripNulls(v any) any {
	switch x := v.(type) {
	case map[any]any:
		out := make(map[any]any, len(x))
		for k, vv := range x {
			if vv == nil {
				continue
			}
			out[k] = stripNulls(vv)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			if vv == nil {
				continue
			}
			out[k] = stripNulls(vv)
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, vv := range x {
			// Slice-element nulls are dropped too — same semantics
			// as map values. An FE sending a sparse array gets the
			// dense version typed-decode.
			if vv == nil {
				continue
			}
			out = append(out, stripNulls(vv))
		}
		return out
	}
	return v
}

// asMap is the bus's internal AsMap variant. Same idea as sdk.AsMap
// (in cbor.go) — accept either map[any]any or map[string]any —
// but lets bus.go avoid an alias-rename headache.
func asMap(data any) map[any]any {
	switch x := data.(type) {
	case map[any]any:
		return x
	case map[string]any:
		out := make(map[any]any, len(x))
		for k, v := range x {
			out[k] = v
		}
		return out
	}
	return nil
}

// requestIDs pulls both id and req_id from a cross-app message.
// req_id is the bus.Call correlation key; id is the FE-side request
// correlation. They're separate concepts but both flow through the
// reply envelope when present.
func requestIDs(data map[any]any) (id, reqID string) {
	id, _ = data["id"].(string)
	reqID, _ = data["req_id"].(string)
	return
}

func requestIDOnly(data map[any]any) string {
	id, _ := data["id"].(string)
	if id == "" {
		// Cross-app fire-and-forget often uses req_id only.
		id, _ = data["req_id"].(string)
	}
	return id
}

// codeOf extracts the wire-level code from err. If err is a bus.Err
// the Code is used; otherwise the fallback is "internal".
func codeOf(err error) string {
	var be Err
	if errors.As(err, &be) {
		if be.Code == "" {
			return ErrInternal
		}
		return be.Code
	}
	return ErrInternal
}

// msgOf extracts the human message from err. bus.Err uses Msg
// directly; other errors return err.Error().
func msgOf(err error) string {
	var be Err
	if errors.As(err, &be) {
		return be.Msg
	}
	return err.Error()
}

// mintReqID returns a short hex id for Call's req_id field. Doesn't
// need to be cryptographic — just unique within the bus's pending
// map at any moment.
func mintReqID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "c-" + hex.EncodeToString(b[:]), nil
}

// Reflection helper: extract the value of a struct field tagged
// cbor:"id" (or json:"id"). Used internally — exposed mostly so
// callers can introspect their own types.
func extractIDField(v any) string {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := strings.Split(f.Tag.Get("cbor"), ",")[0]
		if tag == "" {
			tag = strings.Split(f.Tag.Get("json"), ",")[0]
		}
		if tag == "id" && rv.Field(i).Kind() == reflect.String {
			return rv.Field(i).String()
		}
	}
	return ""
}
