package loopback

import (
	"context"
	"encoding/base64"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sirmick/wash/internal/router"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// TestSpine validates the v0.0 acceptance criterion #6: handshake →
// asset-pull → window mapped → close handshake exercised against the
// real router and real SDK via in-memory pipes — no sockets, no WS.
//
// The dev sandbox SIGKILLs long-lived listening sockets, so this is
// the only way the spine can be validated in CI.
func TestSpine(t *testing.T) {
	// The "fake about" manifest. Mirrors the real wash-about
	// manifest but supplies its own bundle bytes for the asset-pull
	// half.
	const bundleBody = `customElements.define("wash-app-about", class extends HTMLElement{});`
	aboutManifest := sdk.Manifest{
		ID:              "com.wash.about",
		Name:            "About wash",
		Version:         "0.0.0",
		ProtocolVersion: sdk.ProtocolVersion,
		Element:         "wash-app-about",
		Surface:         sdk.SurfaceWindow,
		Icon:            "data:image/svg+xml,W",
		Instancing:      sdk.InstancingMulti,
		Window:          &sdk.WindowHints{DefaultWidth: 480, DefaultHeight: 320},
	}
	rtrManifest := &router.Manifest{
		ID:              "com.wash.about",
		Name:            "About wash",
		Version:         "0.0.0",
		ProtocolVersion: router.ProtocolVersion,
		Element:         "wash-app-about",
		Surface:         router.SurfaceWindow,
		Icon:            "data:image/svg+xml,W",
		Instancing:      router.InstancingMulti,
		Window:          &router.WindowHints{DefaultWidth: 480, DefaultHeight: 320},
	}

	// Build a router with an empty registry — the only app instance
	// we'll exercise comes in via HandleApp directly, not via Spawn.
	reg := router.NewRegistry()
	r := router.NewRouter(router.Config{}, reg, func(format string, args ...any) {
		t.Logf("router: "+format, args...)
	})

	appPair := wiretest.NewPipePair()
	shellPair := wiretest.NewPipePair()

	// Wire the router side of both transports.
	routerDone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = r.HandleApp(ctx, appPair.EndA(), rtrManifest, nil)
		close(routerDone)
	}()
	shellDone := make(chan struct{})
	go func() {
		_ = r.HandleShell(ctx, shellPair.EndA())
		close(shellDone)
	}()

	// Wire the real SDK on the app side. Manifest.OnMapped sets the
	// title; OnCloseRequested defaults to allow=true.
	titleSet := make(chan string, 1)
	def := &sdk.AppDef{
		Manifest: aboutManifest,
		Assets:   fstest.MapFS{"index.js": &fstest.MapFile{Data: []byte(bundleBody)}},
		OnMapped: func(c *sdk.Conn, win uint32) {
			_ = c.SetTitle("About wash")
			titleSet <- "About wash"
		},
	}
	c, err := sdk.ConnectWith(appPair.EndB(), def)
	if err != nil {
		t.Fatalf("sdk connect: %v", err)
	}
	sdkLoopDone := make(chan error, 1)
	go func() { sdkLoopDone <- c.Run(ctx) }()

	// The fake shell side reads frames as a real shell would.
	shell := shellPair.EndB()

	// 1. Receive ShellAppDeclared.
	declared, ok := readCtrl(t, shell).(wire.ShellAppDeclared)
	if !ok {
		t.Fatalf("expected ShellAppDeclared, got %T", declared)
	}
	if declared.Element != "wash-app-about" || declared.Surface != router.SurfaceWindow {
		t.Fatalf("bad declared: %+v", declared)
	}

	// 2. Receive ShellWindowCreate.
	winCreate, ok := readCtrl(t, shell).(wire.ShellWindowCreate)
	if !ok {
		t.Fatalf("expected ShellWindowCreate")
	}
	if winCreate.W != 480 || winCreate.H != 320 {
		t.Fatalf("window create geometry: %+v", winCreate)
	}

	// 3. The SDK's OnMapped fires; OnMapped also calls SetTitle.
	select {
	case <-titleSet:
	case <-time.After(2 * time.Second):
		t.Fatal("OnMapped never fired")
	}
	// 4. ShellWindowTitle arrives on the shell from the relayed
	//    EvtWindowSetTitle.
	titleMsg, ok := readCtrl(t, shell).(wire.ShellWindowTitle)
	if !ok || titleMsg.Title != "About wash" {
		t.Fatalf("expected window.title 'About wash', got %+v", titleMsg)
	}

	// 5. Shell asks for the bundle; SDK serves it; router relays.
	writeCtrl(t, shell, wire.NewShellAssetFetch(declared.InstanceID, "index.js"))
	deliver, ok := readCtrl(t, shell).(wire.ShellAssetDeliver)
	if !ok || !deliver.End {
		t.Fatalf("expected complete ShellAssetDeliver, got %+v", deliver)
	}
	gotBundle, err := base64.StdEncoding.DecodeString(deliver.Bytes)
	if err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	if string(gotBundle) != bundleBody {
		t.Fatalf("bundle bytes mismatch: %q", string(gotBundle))
	}

	// 6. Close handshake: user clicks the titlebar close. Router →
	//    EvtWindowCloseRequested → SDK auto-confirms → router →
	//    ShellWindowDestroy.
	writeCtrl(t, shell, wire.NewShellWindowCloseClicked(winCreate.WindowID))
	destroyed, ok := readCtrl(t, shell).(wire.ShellWindowDestroy)
	if !ok {
		t.Fatalf("expected ShellWindowDestroy, got %T", destroyed)
	}
	if destroyed.WindowID != winCreate.WindowID {
		t.Fatalf("destroyed wrong window: %+v", destroyed)
	}

	// Tear down.
	cancel()
	appPair.Close()
	shellPair.Close()
	select {
	case <-sdkLoopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("SDK loop didn't exit")
	}
	select {
	case <-routerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("router app handler didn't exit")
	}
	select {
	case <-shellDone:
	case <-time.After(2 * time.Second):
		t.Fatal("router shell handler didn't exit")
	}
}

// helpers ---------------------------------------------------------

func readCtrl(t *testing.T, e wire.FrameTransport) any {
	t.Helper()
	f, err := e.ReadFrame()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Channel != 0 {
		t.Fatalf("expected channel 0, got %d", f.Channel)
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
