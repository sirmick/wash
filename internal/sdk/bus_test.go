package sdk

import (
	"context"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// busTestConn spins up a Conn against a fake router and returns the
// established Bus + the router-side pipe end for injecting events.
// Test handlers must register BEFORE Run starts reading frames; the
// helper waits for the handshake but doesn't kick off Run — caller
// does that after Handle calls so kind dispatch beats the first
// frame.
func busTestConn(t *testing.T) (*Bus, wire.FrameTransport, func()) {
	t.Helper()
	pp := wiretest.NewPipePair()
	def := aboutDef(nil)
	type connResult struct {
		c   *Conn
		err error
	}
	ch := make(chan connResult, 1)
	go func() {
		c, err := ConnectWith(pp.EndA(), def)
		ch <- connResult{c, err}
	}()
	_ = readCtrl(t, pp.EndB())
	writeCtrl(t, pp.EndB(), wire.NewIdentityAck("i-1", 1))
	res := <-ch
	if res.err != nil {
		t.Fatalf("connect: %v", res.err)
	}
	bus := NewBus(res.c)
	cleanup := func() { res.c.Close() }
	return bus, pp.EndB(), cleanup
}

func TestBusHandleRequestReply(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type ListReq struct {
		Kind string `cbor:"kind"`
		ID   string `cbor:"id"`
		Path string `cbor:"path"`
	}
	type ListResp struct {
		Path    string   `cbor:"path"`
		Entries []string `cbor:"entries"`
	}
	Handle(bus, "list", func(c *Conn, id string, req ListReq) (ListResp, error) {
		if req.Path == "" {
			return ListResp{}, Errf(ErrBadRequest, "missing path")
		}
		return ListResp{Path: req.Path, Entries: []string{"a", "b"}}, nil
	})

	go func() { _ = bus.conn.Run(context.Background()) }()

	// Send: {kind:"list", id:"r1", path:"/x"}
	writeEvt(t, router, wire.NewEvtAppMsg(1, map[string]any{
		"kind": "list",
		"id":   "r1",
		"path": "/x",
	}))

	got := readEvt(t, router)
	m, ok := got.(wire.EvtAppMsg)
	if !ok {
		t.Fatalf("expected EvtAppMsg, got %T", got)
	}
	data, _ := m.Data.(map[any]any)
	if data == nil {
		t.Fatalf("data shape: %T", m.Data)
	}
	if data["kind"] != "list_ok" {
		t.Fatalf("kind=%v", data["kind"])
	}
	if data["id"] != "r1" {
		t.Fatalf("id=%v", data["id"])
	}
	if data["path"] != "/x" {
		t.Fatalf("path=%v", data["path"])
	}
}

func TestBusHandleErrorReply(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type ReadReq struct {
		Path string `cbor:"path"`
	}
	type ReadResp struct {
		Content string `cbor:"content"`
	}
	Handle(bus, "read", func(c *Conn, id string, req ReadReq) (ReadResp, error) {
		return ReadResp{}, Errf(ErrNotFound, "no such file: %s", req.Path)
	})

	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsg(1, map[string]any{
		"kind": "read",
		"id":   "r2",
		"path": "/missing",
	}))

	got := readEvt(t, router)
	m := got.(wire.EvtAppMsg)
	data, _ := m.Data.(map[any]any)
	if data["kind"] != "read_err" {
		t.Fatalf("kind=%v", data["kind"])
	}
	if data["id"] != "r2" {
		t.Fatalf("id=%v", data["id"])
	}
	if data["code"] != ErrNotFound {
		t.Fatalf("code=%v", data["code"])
	}
	if msg, _ := data["msg"].(string); msg == "" {
		t.Fatalf("missing msg")
	}
}

func TestBusFallbackToOnAppMsg(t *testing.T) {
	pp := wiretest.NewPipePair()
	def := aboutDef(nil)
	gotFallback := make(chan map[string]any, 1)
	def.OnAppMsg = func(c *Conn, win uint32, data any) {
		m := AsMap(data)
		gotFallback <- m
	}
	type connResult struct {
		c   *Conn
		err error
	}
	ch := make(chan connResult, 1)
	go func() {
		c, err := ConnectWith(pp.EndA(), def)
		ch <- connResult{c, err}
	}()
	_ = readCtrl(t, pp.EndB())
	writeCtrl(t, pp.EndB(), wire.NewIdentityAck("i-1", 1))
	res := <-ch
	if res.err != nil {
		t.Fatalf("connect: %v", res.err)
	}
	defer res.c.Close()
	bus := NewBus(res.c)
	Handle(bus, "list", func(c *Conn, id string, req struct{}) (struct{}, error) {
		return struct{}{}, nil
	})

	go func() { _ = res.c.Run(context.Background()) }()

	// "unknown" kind: fallback fires.
	writeEvt(t, pp.EndB(), wire.NewEvtAppMsg(1, map[string]any{
		"kind": "unknown",
		"x":    "y",
	}))
	select {
	case m := <-gotFallback:
		if m["kind"] != "unknown" || m["x"] != "y" {
			t.Fatalf("fallback got %v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fallback didn't fire")
	}
}

func TestBusEmit(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type Update struct {
		ReqID  string `cbor:"req_id"`
		Status string `cbor:"status"`
	}
	if err := bus.Emit("req.update", Update{ReqID: "x", Status: "running"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	got := readEvt(t, router)
	m := got.(wire.EvtAppMsg)
	data, _ := m.Data.(map[any]any)
	if data["kind"] != "req.update" {
		t.Fatalf("kind=%v", data["kind"])
	}
	if data["status"] != "running" {
		t.Fatalf("status=%v", data["status"])
	}
}

func TestBusToleratesNullFields(t *testing.T) {
	// Reproducer for the wash-journal "cannot unmarshal primitives
	// into Go struct field" panic: the FE sent {priority: null} and
	// the bus's CBOR decode rejected it. After stripNulls in
	// decodeInto, the field should fall back to its zero value.
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type SelectReq struct {
		Unit     string `cbor:"unit"`
		Priority int    `cbor:"priority"`
		Range    string `cbor:"range"`
	}
	got := make(chan SelectReq, 1)
	HandleVoid(bus, "select", func(c *Conn, _ string, req SelectReq) error {
		got <- req
		return nil
	})

	go func() { _ = bus.conn.Run(context.Background()) }()

	// priority: nil — would previously fail decode.
	writeEvt(t, router, wire.NewEvtAppMsg(1, map[string]any{
		"kind":     "select",
		"unit":     "ssh.service",
		"priority": nil,
		"range":    "hour",
	}))
	select {
	case r := <-got:
		if r.Unit != "ssh.service" || r.Range != "hour" {
			t.Fatalf("decoded wrong: %+v", r)
		}
		if r.Priority != 0 {
			t.Fatalf("expected zero-value Priority, got %d", r.Priority)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler didn't fire — null still breaks decode")
	}
}

func TestBusHandleVoid(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type SaveReq struct {
		State string `cbor:"state"`
	}
	got := make(chan string, 1)
	HandleVoid(bus, "save_state", func(c *Conn, id string, req SaveReq) error {
		got <- req.State
		return nil
	})

	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsg(1, map[string]any{
		"kind":  "save_state",
		"state": "blob",
	}))
	select {
	case v := <-got:
		if v != "blob" {
			t.Fatalf("state=%q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("save_state handler didn't fire")
	}
	// No reply expected — verify nothing comes through within a
	// short window. We do this by setting a read deadline only via
	// pipe close; instead we just confirm no immediate hang.
}

func TestBusHandlePattern(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	gotKind := make(chan string, 1)
	bus.HandlePattern("cmd.", func(c *Conn, kind string, data map[any]any) {
		gotKind <- kind
	})

	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsg(1, map[string]any{
		"kind": "cmd.save",
		"x":    "y",
	}))
	select {
	case k := <-gotKind:
		if k != "cmd.save" {
			t.Fatalf("kind=%q", k)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pattern handler didn't fire")
	}
}

func TestBusHandleFromCrossApp(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type RunReq struct {
		Argv []string `cbor:"argv"`
	}
	type RunResp struct {
		ExitCode int `cbor:"exit_code"`
	}
	HandleFrom(bus, "run", func(c *Conn, id string, req RunReq, from wire.Sender) (RunResp, error) {
		if from.AppID != "com.wash.fm" {
			return RunResp{}, Errf(ErrForbidden, "wrong sender: %s", from.AppID)
		}
		return RunResp{ExitCode: 42}, nil
	})

	go func() { _ = bus.conn.Run(context.Background()) }()

	// Simulate the router relaying a cross-app message: EvtAppMsg
	// with From set.
	msg := wire.NewEvtAppMsgFrom(1, map[string]any{
		"kind":   "run",
		"req_id": "rq-1",
		"argv":   []any{"whoami"},
	}, wire.Sender{AppID: "com.wash.fm", InstanceID: "i-5"})
	writeEvt(t, router, msg)

	got := readEvt(t, router)
	m := got.(wire.EvtAppMsgSendTo)
	if m.Recipient.InstanceID != "i-5" {
		t.Fatalf("reply addressed to %v", m.Recipient)
	}
	data, _ := m.Data.(map[any]any)
	if data["kind"] != "run_ok" {
		t.Fatalf("kind=%v", data["kind"])
	}
	if data["req_id"] != "rq-1" {
		t.Fatalf("req_id=%v", data["req_id"])
	}
	if data["exit_code"] != uint64(42) && data["exit_code"] != int64(42) {
		t.Fatalf("exit_code=%v (%T)", data["exit_code"], data["exit_code"])
	}
}

func TestBusCallRoundTrip(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type RunReq struct {
		Argv []string `cbor:"argv"`
	}
	type RunResp struct {
		ExitCode int `cbor:"exit_code"`
	}

	go func() { _ = bus.conn.Run(context.Background()) }()

	// In a goroutine, fire Call and wait for the synthetic reply.
	resultCh := make(chan RunResp, 1)
	errCh := make(chan error, 1)
	go func() {
		var resp RunResp
		err := Call(context.Background(), bus, wire.Recipient{AppID: "com.wash.priv"}, "run", RunReq{Argv: []string{"true"}}, &resp)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- resp
	}()

	// Read the outbound request, grab its req_id, send back a
	// matching reply.
	got := readEvt(t, router)
	m := got.(wire.EvtAppMsgSendTo)
	if m.Recipient.AppID != "com.wash.priv" {
		t.Fatalf("recipient=%v", m.Recipient)
	}
	data, _ := m.Data.(map[any]any)
	reqID, _ := data["req_id"].(string)
	if reqID == "" {
		t.Fatalf("missing req_id in outbound request: %v", data)
	}

	// Inject the reply as an EvtAppMsg (the bus's pending-reply
	// path matches on req_id regardless of from).
	reply := wire.NewEvtAppMsg(1, map[string]any{
		"kind":      "run_ok",
		"req_id":    reqID,
		"exit_code": 7,
	})
	writeEvt(t, router, reply)

	select {
	case r := <-resultCh:
		if r.ExitCode != 7 {
			t.Fatalf("exit_code=%d", r.ExitCode)
		}
	case err := <-errCh:
		t.Fatalf("call: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Call didn't return")
	}
}

func TestBusCallErrorEnvelope(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	go func() { _ = bus.conn.Run(context.Background()) }()

	errCh := make(chan error, 1)
	go func() {
		var resp struct{}
		err := Call(context.Background(), bus, wire.Recipient{AppID: "com.wash.priv"}, "run", struct{}{}, &resp)
		errCh <- err
	}()

	got := readEvt(t, router)
	m := got.(wire.EvtAppMsgSendTo)
	data, _ := m.Data.(map[any]any)
	reqID, _ := data["req_id"].(string)

	// Reply with an error envelope.
	writeEvt(t, router, wire.NewEvtAppMsg(1, map[string]any{
		"kind":   "run_err",
		"req_id": reqID,
		"code":   ErrForbidden,
		"msg":    "no",
	}))

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error")
		}
		var be Err
		if !errAs(err, &be) {
			t.Fatalf("expected bus.Err, got %T %v", err, err)
		}
		if be.Code != ErrForbidden {
			t.Fatalf("code=%q", be.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call didn't return")
	}
}

// errAs is a thin wrapper around errors.As for the tests above so we
// don't depend on `errors` at the top of this file (which would otherwise
// be unused after rearranging imports).
func errAs(err error, target any) bool {
	for err != nil {
		if be, ok := err.(Err); ok {
			if pp, ok := target.(*Err); ok {
				*pp = be
				return true
			}
		}
		// Unwrap nesting isn't relevant here — Err isn't wrapped
		// in these tests — so a single-step type assertion suffices.
		break
	}
	return false
}
