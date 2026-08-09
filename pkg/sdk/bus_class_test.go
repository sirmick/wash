package sdk

import (
	"context"
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// readEvtFrame is readEvt's class-aware sibling: returns the raw
// frame alongside the decoded payload so tests can inspect class
// bits set by the SDK.
func readEvtFrame(t *testing.T, e wire.FrameTransport) (wire.Frame, any) {
	t.Helper()
	f, err := e.ReadFrame()
	if err != nil {
		t.Fatalf("read evt: %v", err)
	}
	if f.Channel != 1 {
		t.Fatalf("expected channel 1, got %d", f.Channel)
	}
	m, err := wire.DecodeEvt(f.Payload)
	if err != nil {
		t.Fatalf("decode evt: %v", err)
	}
	return f, m
}

func TestClassifyKind(t *testing.T) {
	cases := []struct {
		kind string
		want wire.Class
	}{
		// Bulk suffixes per QOS.md §10 SDK-internal classifier.
		{"pty.output", wire.ClassBulk},
		{"fs.list_reply", wire.ClassBulk},
		{"fs.read_reply", wire.ClassBulk},
		{"fs.watch_event", wire.ClassBulk},
		{"priv.stream", wire.ClassBulk},
		{"weirdapp.something.output", wire.ClassBulk},
		// Interactive (default).
		{"key.down", wire.ClassInteractive},
		{"window.focus", wire.ClassInteractive},
		{"fs.list", wire.ClassInteractive},
		{"app_msg", wire.ClassInteractive},
		{"", wire.ClassInteractive},
		// `_ok` reply suffix is NOT auto-classed Bulk on its own —
		// the reply kind alone doesn't say "large." HandleBulk is
		// the explicit override.
		{"fs.list_ok", wire.ClassInteractive},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			if got := classifyKind(tc.kind); got != tc.want {
				t.Fatalf("classifyKind(%q)=%v want %v", tc.kind, got, tc.want)
			}
		})
	}

	// Every declared bulk suffix must classify as Bulk: lock the
	// declared list (source of truth) against the function body.
	for _, suffix := range bulkKindSuffixes {
		kind := "app" + suffix
		if got := classifyKind(kind); got != wire.ClassBulk {
			t.Errorf("classifyKind(%q)=%v want Bulk (suffix %q)", kind, got, suffix)
		}
	}
}

func TestEmitDefaultsInteractive(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	go func() { _ = bus.conn.Run(context.Background()) }()

	if err := bus.Emit("custom.ping", map[string]any{"x": 1}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	f, _ := readEvtFrame(t, router)
	if got := f.Class(); got != wire.ClassInteractive {
		t.Fatalf("class=%v, want Interactive (flags=%08b)", got, f.Flags)
	}
}

func TestEmitBulkSetsBulkClass(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	go func() { _ = bus.conn.Run(context.Background()) }()

	if err := bus.EmitBulk("custom.ping", map[string]any{"x": 1}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	f, _ := readEvtFrame(t, router)
	if got := f.Class(); got != wire.ClassBulk {
		t.Fatalf("class=%v, want Bulk (flags=%08b)", got, f.Flags)
	}
}

func TestEmitAutoClassifiesBulkSuffix(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	go func() { _ = bus.conn.Run(context.Background()) }()

	// Emit (not EmitBulk) with a bulk-suffix verb — the classifier
	// safety net should still tag it Bulk.
	if err := bus.Emit("pty.output", map[string]any{"data": "hi"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	f, _ := readEvtFrame(t, router)
	if got := f.Class(); got != wire.ClassBulk {
		t.Fatalf("class=%v, want Bulk (auto from suffix); flags=%08b", got, f.Flags)
	}
}

func TestHandleReplyIsInteractive(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type req struct {
		ID string `json:"id"`
	}
	type resp struct {
		OK bool `json:"ok"`
	}
	Handle(bus, "ping", func(c *Conn, id string, r req) (resp, error) {
		return resp{OK: true}, nil
	})
	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsg(1, map[string]any{
		"kind": "ping",
		"id":   "r1",
	}))
	f, _ := readEvtFrame(t, router)
	if got := f.Class(); got != wire.ClassInteractive {
		t.Fatalf("reply class=%v, want Interactive", got)
	}
}

func TestHandleBulkReplyIsBulk(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type req struct {
		ID string `json:"id"`
	}
	type resp struct {
		OK bool `json:"ok"`
	}
	HandleBulk(bus, "ping", func(c *Conn, id string, r req) (resp, error) {
		return resp{OK: true}, nil
	})
	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsg(1, map[string]any{
		"kind": "ping",
		"id":   "r1",
	}))
	f, _ := readEvtFrame(t, router)
	if got := f.Class(); got != wire.ClassBulk {
		t.Fatalf("reply class=%v, want Bulk", got)
	}
}

func TestEmitBackgroundSetsBackgroundClass(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	go func() { _ = bus.conn.Run(context.Background()) }()

	if err := bus.EmitBackground("transfer.chunk", map[string]any{"seq": 1}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	f, _ := readEvtFrame(t, router)
	if got := f.Class(); got != wire.ClassBackground {
		t.Fatalf("class=%v, want Background (flags=%08b)", got, f.Flags)
	}
}

func TestHandleBackgroundReplyIsBackground(t *testing.T) {
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type req struct {
		ID string `json:"id"`
	}
	type resp struct {
		OK bool `json:"ok"`
	}
	HandleBackground(bus, "ping", func(c *Conn, id string, r req) (resp, error) {
		return resp{OK: true}, nil
	})
	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsg(1, map[string]any{
		"kind": "ping",
		"id":   "r1",
	}))
	f, _ := readEvtFrame(t, router)
	if got := f.Class(); got != wire.ClassBackground {
		t.Fatalf("reply class=%v, want Background", got)
	}
}

func TestHandleBulkErrorReplyIsBulk(t *testing.T) {
	// Error path on a HandleBulk handler also goes out as Bulk —
	// the handler's bulk-ness applies to both success and error
	// envelopes; the user-visible behavior should be consistent.
	bus, router, cleanup := busTestConn(t)
	defer cleanup()

	type req struct {
		ID string `json:"id"`
	}
	type resp struct{}
	HandleBulk(bus, "ping", func(c *Conn, id string, r req) (resp, error) {
		return resp{}, Errf(ErrBadRequest, "no")
	})
	go func() { _ = bus.conn.Run(context.Background()) }()

	writeEvt(t, router, wire.NewEvtAppMsg(1, map[string]any{
		"kind": "ping",
		"id":   "r1",
	}))
	f, _ := readEvtFrame(t, router)
	if got := f.Class(); got != wire.ClassBulk {
		t.Fatalf("error reply class=%v, want Bulk", got)
	}
}
