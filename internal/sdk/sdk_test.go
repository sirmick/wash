package sdk

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// decodeAppMsgData unmarshals the JSON-encoded data field of an
// EvtAppMsg / EvtAppMsgSendTo into a map[string]any for tests that
// need to introspect kind/id/payload fields directly. Callers pass
// the raw bytes (m.Data) so the helper doesn't need to know which
// envelope holds them.
func decodeAppMsgData(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode app_msg data: %v", err)
	}
	return out
}

func readCtrl(t *testing.T, e wire.FrameTransport) any {
	t.Helper()
	f, err := e.ReadFrame()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m, err := wire.DecodeCtrl(f.Payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func writeCtrl(t *testing.T, e wire.FrameTransport, m any) {
	t.Helper()
	b, err := wire.EncodeCtrl(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := e.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: b}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func writeEvt(t *testing.T, e wire.FrameTransport, m any) {
	t.Helper()
	b, err := wire.EncodeEvt(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := e.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: b}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readEvt(t *testing.T, e wire.FrameTransport) any {
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
	return m
}

func aboutDef(assets map[string]string) *AppDef {
	def := &AppDef{
		Manifest: Manifest{
			ID:              "com.wash.about",
			Name:            "About wash",
			Version:         "0.0.0",
			ProtocolVersion: 1,
			Element:         "wash-app-about",
			Surface:         SurfaceWindow,
			Icon:            "data:image/svg+xml,W",
			Instancing:      InstancingMulti,
		},
	}
	if assets != nil {
		fsys := fstest.MapFS{}
		for k, v := range assets {
			fsys[k] = &fstest.MapFile{Data: []byte(v)}
		}
		def.Assets = fsys
	}
	return def
}

func TestHandshakeSendsIdentityAndAcceptsAck(t *testing.T) {
	pp := wiretest.NewPipePair()
	def := aboutDef(nil)

	// Run Connect concurrently with the fake router.
	type connResult struct {
		c   *Conn
		err error
	}
	ch := make(chan connResult, 1)
	go func() {
		c, err := ConnectWith(pp.EndA(), def)
		ch <- connResult{c, err}
	}()

	// Router side: read identity, send identity.ack.
	got := readCtrl(t, pp.EndB())
	ident, ok := got.(wire.Identity)
	if !ok {
		t.Fatalf("expected Identity, got %T", got)
	}
	if ident.AppID != "com.wash.about" || ident.Proto != 1 {
		t.Fatalf("identity mismatch: %+v", ident)
	}
	writeCtrl(t, pp.EndB(), wire.NewIdentityAck("i-42", 7))

	res := <-ch
	if res.err != nil {
		t.Fatalf("connect: %v", res.err)
	}
	if res.c.InstanceID() != "i-42" || res.c.WindowID() != 7 {
		t.Fatalf("conn state: %+v", res.c)
	}
	res.c.Close()
}

func TestHandshakeFailsOnRouterError(t *testing.T) {
	pp := wiretest.NewPipePair()
	def := aboutDef(nil)

	ch := make(chan error, 1)
	go func() {
		_, err := ConnectWith(pp.EndA(), def)
		ch <- err
	}()
	_ = readCtrl(t, pp.EndB())
	writeCtrl(t, pp.EndB(), wire.NewError(wire.ErrCodeBadIdentity, "no"))
	select {
	case err := <-ch:
		if err == nil || !strings.Contains(err.Error(), "router refused") {
			t.Fatalf("unexpected err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hang")
	}
}

func TestCallbackOnMappedAndCloseConfirm(t *testing.T) {
	pp := wiretest.NewPipePair()
	def := aboutDef(nil)
	mapped := make(chan uint32, 1)
	closeAsked := make(chan uint32, 1)
	def.OnMapped = func(c *Conn, win uint32) { mapped <- win }
	def.OnCloseRequested = func(c *Conn, win uint32) bool {
		closeAsked <- win
		return true
	}

	go func() {
		c, err := ConnectWith(pp.EndA(), def)
		if err != nil {
			t.Errorf("connect: %v", err)
			return
		}
		_ = c.Run(context.Background())
	}()

	_ = readCtrl(t, pp.EndB())
	writeCtrl(t, pp.EndB(), wire.NewIdentityAck("i-1", 9))

	writeEvt(t, pp.EndB(), wire.NewEvtWindowMapped(9))
	select {
	case w := <-mapped:
		if w != 9 {
			t.Fatalf("mapped win %d", w)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnMapped didn't fire")
	}

	writeEvt(t, pp.EndB(), wire.NewEvtWindowCloseRequested(9))
	select {
	case w := <-closeAsked:
		if w != 9 {
			t.Fatalf("close win %d", w)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnCloseRequested didn't fire")
	}

	got, _ := readEvt(t, pp.EndB()).(wire.EvtWindowConfirmClose)
	if got.Win != 9 || !got.Allow {
		t.Fatalf("confirm_close mismatch: %+v", got)
	}
}

func TestPrintManifestEnvelope(t *testing.T) {
	// Just exercise the marshal step.
	def := aboutDef(nil)
	b, err := marshalManifestForTest(def)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"com.wash.about"`) {
		t.Fatalf("manifest bytes: %s", b)
	}
}

// marshalManifestForTest exposes the manifest-bytes path without
// exiting the process; the real --wash-manifest path calls os.Exit so
// it can't be tested directly.
func marshalManifestForTest(def *AppDef) ([]byte, error) {
	return wire.EncodeCtrl(def.Manifest)
}
