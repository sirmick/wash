package router

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// pipePair is an in-memory FrameTransport pair used to drive the
// router in tests without sockets — the same plumbing used by the
// loopback spine test in commit C8.
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

// helpers for fake app/shell sides ---------------------------------

func readCtrl(t *testing.T, e *pipeEnd) any {
	t.Helper()
	f, err := e.ReadFrame()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Channel != ChannelControl {
		t.Fatalf("expected channel %d, got %d", ChannelControl, f.Channel)
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
	if err := e.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: b}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func aboutManifest() *Manifest {
	return &Manifest{
		ID:              "com.wash.about",
		Name:            "About wash",
		Version:         "0.0.0",
		ProtocolVersion: ProtocolVersion,
		Element:         "wash-app-about",
		Surface:         SurfaceWindow,
		Icon:            "data:image/svg+xml,W",
		Instancing:      InstancingMulti,
		Window:          &WindowHints{DefaultWidth: 480, DefaultHeight: 320},
	}
}

// TestHandshakeAndAssetPull drives the full v0.0 spine in-memory:
//
//   - App side sends Identity → router replies IdentityAck and tells
//     the shell ShellAppDeclared + ShellWindowCreate.
//   - Shell sends ShellAssetFetch → router relays as AssetRead on the
//     app's channel 0.
//   - App responds with AssetReadOK then AssetData(end=true) → router
//     relays both as ShellAssetDeliver back to the shell.
func TestHandshakeAndAssetPull(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, func(format string, args ...any) {
		t.Logf("router: "+format, args...)
	})

	appPair := newPipePair()
	shellPair := newPipePair()

	manifest := aboutManifest()
	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		_ = r.HandleApp(context.Background(), appPair.endA(), manifest, nil)
	}()

	shellDone := make(chan struct{})
	go func() {
		defer close(shellDone)
		_ = r.HandleShell(context.Background(), shellPair.endA())
	}()

	// App side: send Identity, expect IdentityAck.
	writeCtrl(t, appPair.endB(), wire.NewIdentity("com.wash.about", 1, "0.0.0"))
	ack, ok := readCtrl(t, appPair.endB()).(wire.IdentityAck)
	if !ok {
		t.Fatalf("expected IdentityAck")
	}
	if ack.InstanceID == "" {
		t.Fatal("instance_id missing")
	}
	if ack.WindowID == 0 {
		t.Fatal("window_id should be set for surface=window")
	}

	// Shell side: expect ShellAppDeclared then ShellWindowCreate.
	declared, ok := readCtrl(t, shellPair.endB()).(wire.ShellAppDeclared)
	if !ok {
		t.Fatalf("expected ShellAppDeclared")
	}
	if declared.InstanceID != ack.InstanceID || declared.Surface != SurfaceWindow {
		t.Fatalf("declared mismatch: %+v", declared)
	}
	winCreate, ok := readCtrl(t, shellPair.endB()).(wire.ShellWindowCreate)
	if !ok {
		t.Fatalf("expected ShellWindowCreate")
	}
	if winCreate.WindowID != ack.WindowID || winCreate.W != 480 || winCreate.H != 320 {
		t.Fatalf("window_create mismatch: %+v", winCreate)
	}

	// App side: expect EvtWindowMapped on channel 1.
	if f, err := appPair.endB().ReadFrame(); err != nil {
		t.Fatalf("read mapped: %v", err)
	} else if f.Channel != ChannelEvent {
		t.Fatalf("mapped on channel %d", f.Channel)
	} else {
		got, err := wire.DecodeEvt(f.Payload)
		if err != nil {
			t.Fatalf("decode evt: %v", err)
		}
		mapped, ok := got.(wire.EvtWindowMapped)
		if !ok || mapped.Win != ack.WindowID {
			t.Fatalf("mapped mismatch: %+v", got)
		}
	}

	// Shell side: send ShellAssetFetch.
	writeCtrl(t, shellPair.endB(), wire.NewShellAssetFetch(ack.InstanceID, "index.js"))

	// App side: should receive AssetRead.
	readMsg := readCtrl(t, appPair.endB())
	read, ok := readMsg.(wire.AssetRead)
	if !ok {
		t.Fatalf("expected AssetRead, got %T (%v)", readMsg, readMsg)
	}
	if read.Name != "index.js" {
		t.Fatalf("name mismatch: %s", read.Name)
	}

	// App side: reply with AssetReadOK + AssetData(end=true).
	writeCtrl(t, appPair.endB(), wire.NewAssetReadOK(read.ID, 5, "application/javascript"))
	body := base64.StdEncoding.EncodeToString([]byte("hello"))
	writeCtrl(t, appPair.endB(), wire.NewAssetData(read.ID, body, true))

	// Shell side: expect ShellAssetDeliver.
	delivered, ok := readCtrl(t, shellPair.endB()).(wire.ShellAssetDeliver)
	if !ok {
		t.Fatalf("expected ShellAssetDeliver")
	}
	if delivered.InstanceID != ack.InstanceID || delivered.Name != "index.js" || delivered.Bytes != body || !delivered.End {
		t.Fatalf("deliver mismatch: %+v", delivered)
	}
	if delivered.MIME != "application/javascript" {
		t.Fatalf("mime not propagated: %q", delivered.MIME)
	}

	// Tear down by closing the app side; router will exit HandleApp.
	appPair.closeBoth()
	shellPair.closeBoth()
	waitClose(t, appDone)
	waitClose(t, shellDone)
}

// TestHandshakeRejectsBadAppID confirms the router refuses an identity
// frame whose app_id doesn't match what was spawned.
func TestHandshakeRejectsBadAppID(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, nil)
	pp := newPipePair()
	manifest := aboutManifest()

	errCh := make(chan error, 1)
	go func() { errCh <- r.HandleApp(context.Background(), pp.endA(), manifest, nil) }()

	writeCtrl(t, pp.endB(), wire.NewIdentity("com.evil.imposter", 1, "0.0.0"))

	// Router will close after sending Error. Drain.
	msg := readCtrl(t, pp.endB())
	if e, ok := msg.(wire.Error); !ok || e.Code != wire.ErrCodeBadIdentity {
		t.Fatalf("expected bad_identity error, got %+v", msg)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected non-nil error from HandleApp")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HandleApp didn't return")
	}
}

// TestHandshakeRejectsBadProto checks ProtocolVersion mismatch is
// surfaced and the connection closes.
func TestHandshakeRejectsBadProto(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{}, reg, nil)
	pp := newPipePair()
	manifest := aboutManifest()

	errCh := make(chan error, 1)
	go func() { errCh <- r.HandleApp(context.Background(), pp.endA(), manifest, nil) }()

	writeCtrl(t, pp.endB(), wire.NewIdentity("com.wash.about", 99, "0.0.0"))

	msg := readCtrl(t, pp.endB())
	if e, ok := msg.(wire.Error); !ok || e.Code != wire.ErrCodeProtoMismatch {
		t.Fatalf("expected proto_mismatch error, got %+v", msg)
	}

	select {
	case err := <-errCh:
		if !errIsFromHandshake(err) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HandleApp didn't return")
	}
}

func errIsFromHandshake(err error) bool {
	return err != nil && !errors.Is(err, io.EOF)
}

func waitClose(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine didn't finish")
	}
}
