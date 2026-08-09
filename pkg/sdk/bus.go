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
// Decoding strategy: incoming Data lands as map[string]any (the SDK
// json-decodes once at dispatch). The bus re-marshals just the data
// field to JSON bytes and unmarshals into the typed Req. One extra
// encode+decode per message, but the wire types stay sharp.
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
	"strings"
	"sync"

	"github.com/sirmick/wash/pkg/wire"
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
	// gets the matching reply data on its one-shot channel.
	pending *pendingCalls[string, map[string]any]

	// prevAppMsg / prevAppMsgFrom hold whatever callbacks the app
	// already installed. The bus chains to them for any kind it
	// doesn't recognise so adoption is incremental.
	prevAppMsg     func(c *Conn, win uint32, data any)
	prevAppMsgFrom func(c *Conn, win uint32, data any, from wire.Sender)
}

// handlerFn is the bus's internal handler shape. data is the
// JSON-decoded message map; from is non-nil for cross-app deliveries.
type handlerFn func(c *Conn, win uint32, data map[string]any, from *wire.Sender)

type patternHandler struct {
	prefix string
	fn     func(c *Conn, kind string, data map[string]any)
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
		pending:  newPendingCalls[string, map[string]any](),
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
	m, _ := data.(map[string]any)
	if m == nil {
		// Not a map shape — bus has nothing to dispatch; fall back.
		b.fallback(c, win, data, from)
		return
	}
	// Pending-reply waiter (Bus.Call). Match on req_id; consume.
	if reqID, _ := m["req_id"].(string); reqID != "" {
		if b.pending.resolve(reqID, m) {
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
// raw decoded map plus the kind string. Used for wash-edit's "cmd.*"
// transparent FE→BE→FE pass-through. Only applies to own-FE messages
// (cross-app messages always have a typed kind contract).
func (b *Bus) HandlePattern(prefix string, fn func(c *Conn, kind string, data map[string]any)) {
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
// context to identify which message broke.
//
//	bus: <app-id> decode <kind> id=<id> from=<src>: <err>
//	  data: {…trimmed payload…}
func (b *Bus) logDecodeErr(kind, id string, from *wire.Sender, data map[string]any, err error) {
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
	out, err := json.Marshal(v)
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
// the inbound payload into Req (re-marshalling through JSON for a
// clean typed decode) and ships the returned Resp wrapped in a
// <kind>_ok envelope with id echoed. Errors map to <kind>_err.
//
// Reply class is Interactive — the common case. For handlers whose
// reply is large by nature (list/read/stream), use HandleBulk.
func Handle[Req, Resp any](b *Bus, kind string, fn func(c *Conn, id string, req Req) (Resp, error)) {
	handleClass(b, kind, fn, wire.ClassInteractive)
}

// HandleBulk is Handle for replies that should always go out on the
// Bulk class.
func HandleBulk[Req, Resp any](b *Bus, kind string, fn func(c *Conn, id string, req Req) (Resp, error)) {
	handleClass(b, kind, fn, wire.ClassBulk)
}

// HandleBackground is Handle for replies on the Background class.
func HandleBackground[Req, Resp any](b *Bus, kind string, fn func(c *Conn, id string, req Req) (Resp, error)) {
	handleClass(b, kind, fn, wire.ClassBackground)
}

func handleClass[Req, Resp any](b *Bus, kind string, fn func(c *Conn, id string, req Req) (Resp, error), class wire.Class) {
	b.register(kind, func(c *Conn, _ uint32, data map[string]any, _ *wire.Sender) {
		id, _ := data["id"].(string)
		var req Req
		if err := decodeInto(data, &req); err != nil {
			b.logDecodeErr(kind, id, nil, data, err)
			b.sendErr(c, kind, id, ErrBadRequest, err.Error(), &replyTo{class: class})
			return
		}
		resp, err := fn(c, id, req)
		if err != nil {
			b.sendErr(c, kind, id, codeOf(err), msgOf(err), &replyTo{class: class})
			return
		}
		b.sendOk(c, kind, id, resp, &replyTo{class: class})
	})
}

// HandleVoid registers a fire-and-forget handler — no reply on
// success.
func HandleVoid[Req any](b *Bus, kind string, fn func(c *Conn, id string, req Req) error) {
	b.register(kind, func(c *Conn, _ uint32, data map[string]any, _ *wire.Sender) {
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
func HandleFrom[Req, Resp any](b *Bus, kind string, fn func(c *Conn, id string, req Req, from wire.Sender) (Resp, error)) {
	handleFromClass(b, kind, fn, wire.ClassInteractive)
}

// HandleFromBulk is HandleFrom for replies on Bulk class.
func HandleFromBulk[Req, Resp any](b *Bus, kind string, fn func(c *Conn, id string, req Req, from wire.Sender) (Resp, error)) {
	handleFromClass(b, kind, fn, wire.ClassBulk)
}

// HandleFromBackground is HandleFrom for replies on Background class.
func HandleFromBackground[Req, Resp any](b *Bus, kind string, fn func(c *Conn, id string, req Req, from wire.Sender) (Resp, error)) {
	handleFromClass(b, kind, fn, wire.ClassBackground)
}

func handleFromClass[Req, Resp any](b *Bus, kind string, fn func(c *Conn, id string, req Req, from wire.Sender) (Resp, error), class wire.Class) {
	b.register(kind, func(c *Conn, _ uint32, data map[string]any, from *wire.Sender) {
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
			b.sendErr(c, kind, id, ErrBadRequest, err.Error(), &replyTo{from: *from, reqID: reqID, class: class})
			return
		}
		resp, err := fn(c, id, req, *from)
		if err != nil {
			b.sendErr(c, kind, id, codeOf(err), msgOf(err), &replyTo{from: *from, reqID: reqID, class: class})
			return
		}
		b.sendOk(c, kind, id, resp, &replyTo{from: *from, reqID: reqID, class: class})
	})
}

// HandleFromVoid is HandleFrom for fire-and-forget cross-app messages.
func HandleFromVoid[Req any](b *Bus, kind string, fn func(c *Conn, id string, req Req, from wire.Sender) error) {
	b.register(kind, func(c *Conn, _ uint32, data map[string]any, from *wire.Sender) {
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

// Emit pushes an unsolicited message to the app's FE.
func (b *Bus) Emit(kind string, payload any) error {
	out, err := envelope(kind, "", payload)
	if err != nil {
		return err
	}
	if classifyKind(kind) == wire.ClassBulk {
		return b.conn.SendAppMsgBulk(out)
	}
	return b.conn.SendAppMsg(out)
}

// EmitBulk is Emit with the Bulk class bit set unconditionally.
func (b *Bus) EmitBulk(kind string, payload any) error {
	out, err := envelope(kind, "", payload)
	if err != nil {
		return err
	}
	return b.conn.SendAppMsgBulk(out)
}

// EmitBackground is Emit with the Background class bit set.
func (b *Bus) EmitBackground(kind string, payload any) error {
	out, err := envelope(kind, "", payload)
	if err != nil {
		return err
	}
	return b.conn.SendAppMsgBackground(out)
}

// EmitTo pushes an unsolicited message to a specific other app.
func (b *Bus) EmitTo(recipient wire.Recipient, kind string, payload any) error {
	out, err := envelope(kind, "", payload)
	if err != nil {
		return err
	}
	if classifyKind(kind) == wire.ClassBulk {
		return b.conn.SendAppMsgToBulk(recipient, out)
	}
	return b.conn.SendAppMsgTo(recipient, out)
}

// EmitToBulk is EmitTo with the Bulk class bit set unconditionally.
func (b *Bus) EmitToBulk(recipient wire.Recipient, kind string, payload any) error {
	out, err := envelope(kind, "", payload)
	if err != nil {
		return err
	}
	return b.conn.SendAppMsgToBulk(recipient, out)
}

// EmitToBackground is EmitTo with the Background class bit set.
func (b *Bus) EmitToBackground(recipient wire.Recipient, kind string, payload any) error {
	out, err := envelope(kind, "", payload)
	if err != nil {
		return err
	}
	return b.conn.SendAppMsgToBackground(recipient, out)
}

// bulkKindSuffixes are the well-known streaming verb suffixes that
// carry large or high-frequency payloads, so classifyKind tags them
// Bulk even when an app called Emit instead of EmitBulk. This list is
// the source of truth — keep it small and conservative.
var bulkKindSuffixes = []string{
	".output",
	".list_reply",
	".read_reply",
	".watch_event",
	".stream",
}

// classifyKind is the SDK-internal safety net that picks Bulk for the
// well-known streaming verb suffixes in bulkKindSuffixes when an app
// called Emit instead of EmitBulk.
func classifyKind(kind string) wire.Class {
	for _, suffix := range bulkKindSuffixes {
		if strings.HasSuffix(kind, suffix) {
			return wire.ClassBulk
		}
	}
	return wire.ClassInteractive
}

// Call performs a typed cross-app request, awaiting the matching
// reply. The bus mints a req_id, ships the request via SendAppMsgTo,
// installs a one-shot waiter, and decodes the matching reply into
// *resp.
//
// MUST NOT be called from an SDK callback: the reply arrives on the
// same read goroutine that's running the callback, so blocking it
// deadlocks. Spawn a goroutine first if you're inside a handler.
func Call[Req, Resp any](ctx context.Context, b *Bus, recipient wire.Recipient, kind string, req Req, resp *Resp) error {
	reqID, err := mintReqID()
	if err != nil {
		return err
	}
	ch := b.pending.register(reqID)
	defer b.pending.cancel(reqID)

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

// replyTo carries the addressing + class fields needed to ship a
// reply.
type replyTo struct {
	from  wire.Sender
	reqID string
	class wire.Class
}

// sendOk builds the <kind>_ok envelope and ships it.
func (b *Bus) sendOk(c *Conn, kind, id string, resp any, to *replyTo) {
	out, err := envelope(kind+"_ok", id, resp)
	if err != nil {
		log.Printf("bus: %s %s_ok encode id=%q: %v", b.appID(), kind, id, err)
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

// ship is the addressing+class branch.
func (b *Bus) ship(c *Conn, payload map[string]any, to *replyTo) {
	class := wire.ClassInteractive
	if to != nil {
		class = to.class
	}
	// A reply that can't be shipped strands the requester (it hangs or
	// times out with no trace anywhere) — the failure must be logged.
	if to != nil && to.from.InstanceID != "" {
		if to.reqID != "" {
			payload["req_id"] = to.reqID
		}
		// Address by InstanceID so the reply always lands on the
		// specific caller, not a sibling singleton instance.
		recipient := wire.Recipient{InstanceID: to.from.InstanceID}
		var err error
		switch class {
		case wire.ClassBulk:
			err = c.SendAppMsgToBulk(recipient, payload)
		case wire.ClassBackground:
			err = c.SendAppMsgToBackground(recipient, payload)
		default:
			err = c.SendAppMsgTo(recipient, payload)
		}
		if err != nil {
			log.Printf("bus: %s ship %v to=%s req_id=%q: %v",
				b.appID(), payload["kind"], to.from.InstanceID, to.reqID, err)
		}
		return
	}
	var err error
	switch class {
	case wire.ClassBulk:
		err = c.SendAppMsgBulk(payload)
	case wire.ClassBackground:
		err = c.SendAppMsgBackground(payload)
	default:
		err = c.SendAppMsg(payload)
	}
	if err != nil {
		log.Printf("bus: %s ship %v to FE: %v", b.appID(), payload["kind"], err)
	}
}

// envelope wraps payload in {kind, id?, ...payload fields} as a
// map[string]any. payload may be a struct (re-marshaled through JSON
// to extract its fields) or already a map.
//
// id precedence: an explicit `id` arg always wins; otherwise an "id"
// field in the payload passes through.
func envelope(kind, id string, payload any) (map[string]any, error) {
	out := map[string]any{"kind": kind}
	if id != "" {
		out["id"] = id
	}
	if payload == nil {
		return out, nil
	}
	// Fast path: payload is already a map[string]any. Copy its
	// keys onto out without going through JSON.
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
	// Generic path: re-marshal through JSON, walk into out.
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("envelope marshal: %w", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(buf, &fields); err != nil {
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

// decodeInto re-marshals a JSON-shaped map into typed dst by going
// through JSON bytes. Slow path (one encode + one decode) but
// zero-dep and type-correct. encoding/json handles JSON null by
// leaving the field at its zero value — the natural "field absent"
// semantic — so no null-stripping pass is needed.
func decodeInto(data any, dst any) error {
	buf, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("decode marshal: %w", err)
	}
	if err := json.Unmarshal(buf, dst); err != nil {
		return fmt.Errorf("decode unmarshal: %w", err)
	}
	return nil
}

// requestIDs pulls both id and req_id from a cross-app message.
// req_id is the bus.Call correlation key; id is the FE-side request
// correlation. They're separate concepts but both flow through the
// reply envelope when present.
func requestIDs(data map[string]any) (id, reqID string) {
	id, _ = data["id"].(string)
	reqID, _ = data["req_id"].(string)
	return
}

func requestIDOnly(data map[string]any) string {
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
