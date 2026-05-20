package sdk

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// In-memory pipe pair; duplicated from router/spine_test.go until the
// loopback consolidation in C8.
type pipePair struct {
	aToB chan wire.Frame
	bToA chan wire.Frame
	done chan struct{}
	once sync.Once
}

func newPipePair() *pipePair {
	return &pipePair{
		aToB: make(chan wire.Frame, 32),
		bToA: make(chan wire.Frame, 32),
		done: make(chan struct{}),
	}
}

func (p *pipePair) closeBoth() { p.once.Do(func() { close(p.done) }) }

type pipeEnd struct {
	in  <-chan wire.Frame
	out chan<- wire.Frame
	pp  *pipePair
}

func (e *pipeEnd) ReadFrame() (wire.Frame, error) {
	select {
	case f := <-e.in:
		return f, nil
	case <-e.pp.done:
		return wire.Frame{}, io.EOF
	}
}
func (e *pipeEnd) WriteFrame(f wire.Frame) error {
	select {
	case e.out <- f:
		return nil
	case <-e.pp.done:
		return io.ErrClosedPipe
	}
}
func (e *pipeEnd) Close() error {
	e.pp.closeBoth()
	return nil
}
func (p *pipePair) endA() *pipeEnd { return &pipeEnd{in: p.bToA, out: p.aToB, pp: p} }
func (p *pipePair) endB() *pipeEnd { return &pipeEnd{in: p.aToB, out: p.bToA, pp: p} }

func readCtrl(t *testing.T, e *pipeEnd) any {
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

func writeCtrl(t *testing.T, e *pipeEnd, m any) {
	t.Helper()
	b, err := wire.EncodeCtrl(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := e.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: b}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func writeEvt(t *testing.T, e *pipeEnd, m any) {
	t.Helper()
	b, err := wire.EncodeEvt(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := e.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: b}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readEvt(t *testing.T, e *pipeEnd) any {
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
	pp := newPipePair()
	def := aboutDef(nil)

	// Run Connect concurrently with the fake router.
	type connResult struct {
		c   *Conn
		err error
	}
	ch := make(chan connResult, 1)
	go func() {
		c, err := ConnectWith(pp.endA(), def)
		ch <- connResult{c, err}
	}()

	// Router side: read identity, send identity.ack.
	got := readCtrl(t, pp.endB())
	ident, ok := got.(wire.Identity)
	if !ok {
		t.Fatalf("expected Identity, got %T", got)
	}
	if ident.AppID != "com.wash.about" || ident.Proto != 1 {
		t.Fatalf("identity mismatch: %+v", ident)
	}
	writeCtrl(t, pp.endB(), wire.NewIdentityAck("i-42", 7))

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
	pp := newPipePair()
	def := aboutDef(nil)

	ch := make(chan error, 1)
	go func() {
		_, err := ConnectWith(pp.endA(), def)
		ch <- err
	}()
	_ = readCtrl(t, pp.endB())
	writeCtrl(t, pp.endB(), wire.NewError(wire.ErrCodeBadIdentity, "no"))
	select {
	case err := <-ch:
		if err == nil || !strings.Contains(err.Error(), "router refused") {
			t.Fatalf("unexpected err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hang")
	}
}

func TestServeAsset(t *testing.T) {
	pp := newPipePair()
	def := aboutDef(map[string]string{"index.js": "console.log('hi');"})

	ch := make(chan *Conn, 1)
	go func() {
		c, err := ConnectWith(pp.endA(), def)
		if err != nil {
			t.Errorf("connect: %v", err)
			ch <- nil
			return
		}
		ch <- c
		_ = c.Run(context.Background())
	}()

	_ = readCtrl(t, pp.endB()) // identity
	writeCtrl(t, pp.endB(), wire.NewIdentityAck("i-1", 1))

	c := <-ch
	if c == nil {
		t.Fatal("connect failed")
	}

	// Router side: request the asset.
	writeCtrl(t, pp.endB(), wire.NewAssetRead(42, "index.js"))

	ok, _ := readCtrl(t, pp.endB()).(wire.AssetReadOK)
	if ok.ID != 42 || ok.Len != int64(len("console.log('hi');")) {
		t.Fatalf("asset.read.ok mismatch: %+v", ok)
	}
	if !strings.Contains(ok.MIME, "javascript") {
		t.Fatalf("mime %q has no javascript", ok.MIME)
	}

	data, _ := readCtrl(t, pp.endB()).(wire.AssetData)
	if data.ID != 42 || !data.End {
		t.Fatalf("asset.data mismatch: %+v", data)
	}
	raw, err := base64.StdEncoding.DecodeString(data.Bytes)
	if err != nil || string(raw) != "console.log('hi');" {
		t.Fatalf("bytes mismatch: %q (err=%v)", string(raw), err)
	}
}

func TestServeAssetMissing(t *testing.T) {
	pp := newPipePair()
	def := aboutDef(map[string]string{}) // empty fs

	go func() {
		c, err := ConnectWith(pp.endA(), def)
		if err != nil {
			t.Errorf("connect: %v", err)
			return
		}
		_ = c.Run(context.Background())
	}()

	_ = readCtrl(t, pp.endB())
	writeCtrl(t, pp.endB(), wire.NewIdentityAck("i-1", 1))
	writeCtrl(t, pp.endB(), wire.NewAssetRead(7, "nope.js"))

	got := readCtrl(t, pp.endB())
	errMsg, ok := got.(wire.AssetReadErr)
	if !ok {
		t.Fatalf("expected AssetReadErr, got %T (%+v)", got, got)
	}
	if errMsg.Code != wire.ErrCodeNotFound {
		t.Fatalf("code mismatch: %+v", errMsg)
	}
}

func TestCallbackOnMappedAndCloseConfirm(t *testing.T) {
	pp := newPipePair()
	def := aboutDef(nil)
	mapped := make(chan uint32, 1)
	closeAsked := make(chan uint32, 1)
	def.OnMapped = func(c *Conn, win uint32) { mapped <- win }
	def.OnCloseRequested = func(c *Conn, win uint32) bool {
		closeAsked <- win
		return true
	}

	go func() {
		c, err := ConnectWith(pp.endA(), def)
		if err != nil {
			t.Errorf("connect: %v", err)
			return
		}
		_ = c.Run(context.Background())
	}()

	_ = readCtrl(t, pp.endB())
	writeCtrl(t, pp.endB(), wire.NewIdentityAck("i-1", 9))

	writeEvt(t, pp.endB(), wire.NewEvtWindowMapped(9))
	select {
	case w := <-mapped:
		if w != 9 {
			t.Fatalf("mapped win %d", w)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnMapped didn't fire")
	}

	writeEvt(t, pp.endB(), wire.NewEvtWindowCloseRequested(9))
	select {
	case w := <-closeAsked:
		if w != 9 {
			t.Fatalf("close win %d", w)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnCloseRequested didn't fire")
	}

	got, _ := readEvt(t, pp.endB()).(wire.EvtWindowConfirmClose)
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
