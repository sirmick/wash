package loopback

import (
	"context"
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
	// OnMapped sets a title distinct from the manifest Name so the
	// router-side title patch isn't optimized away as a no-op.
	const mappedTitle = "About wash — ready"
	titleSet := make(chan string, 1)
	def := &sdk.AppDef{
		Manifest: aboutManifest,
		Assets:   fstest.MapFS{"index.js": &fstest.MapFile{Data: []byte(bundleBody)}},
		OnMapped: func(c *sdk.Conn, win uint32) {
			_ = c.SetTitle(mappedTitle)
			titleSet <- mappedTitle
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

	// 0. Catalog goes out first on shell connect.
	if _, ok := readCtrl(t, shell).(wire.ShellCatalog); !ok {
		t.Fatalf("expected ShellCatalog first")
	}

	// 1. Drain frames until we have both app.declared and a window
	// upsert. They arrive in either order depending on whether the
	// shell registered before or after the app finished handshake —
	// declares can come from the HandleShell setup loop or from the
	// HandleApp broadcast.
	var declared *wire.ShellAppDeclared
	var window *wire.SessionWindow
	for declared == nil || window == nil {
		switch m := readCtrl(t, shell).(type) {
		case wire.ShellAppDeclared:
			d := m
			declared = &d
		case wire.ShellSessionSnapshot:
			for i := range m.Windows {
				w := m.Windows[i]
				window = &w
			}
		case wire.ShellSessionPatch:
			for _, p := range m.Patches {
				if p.Op == wire.SessionPatchWindowUpsert && p.Window != nil {
					w := *p.Window
					window = &w
				}
			}
		}
	}
	if declared.Element != "wash-app-about" || declared.Surface != router.SurfaceWindow {
		t.Fatalf("bad declared: %+v", declared)
	}
	if window.W != 480 || window.H != 320 {
		t.Fatalf("window geometry: %+v", window)
	}
	winID := window.WindowID

	// 2. The SDK's OnMapped fires; OnMapped also calls SetTitle.
	select {
	case <-titleSet:
	case <-time.After(2 * time.Second):
		t.Fatal("OnMapped never fired")
	}
	// 3. A session.patch arrives with the new title. May be preceded
	//    by other in-flight frames; drain until we see it.
	for {
		switch m := readCtrl(t, shell).(type) {
		case wire.ShellSessionPatch:
			for _, p := range m.Patches {
				if p.Op == wire.SessionPatchWindowUpsert && p.Window != nil && p.Window.WindowID == winID && p.Window.Title == mappedTitle {
					goto titlePatchFound
				}
			}
		}
	}
titlePatchFound:

	// 4. Bundle delivery: the SDK ships the embedded bundle on a
	// kind=bundle raw channel right after handshake. Router caches
	// the bytes and replays them to every attached shell via
	// ShellChannelBind{kind:bundle} + raw frames + ShellChannelUnbind.
	var bundleChannelID uint32
	bundleBytes := []byte{}
	bundleDone := false
	for !bundleDone {
		f, err := shell.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if f.Channel == 0 {
			m, derr := wire.DecodeCtrl(f.Payload)
			if derr != nil {
				t.Fatalf("decode ctrl: %v", derr)
			}
			switch v := m.(type) {
			case wire.ShellChannelBind:
				if v.Kind == wire.ChannelKindBundle && v.InstanceID == declared.InstanceID {
					bundleChannelID = v.ChannelID
				}
			case wire.ShellChannelUnbind:
				if v.ChannelID == bundleChannelID {
					bundleDone = true
				}
			}
			continue
		}
		if f.Channel == bundleChannelID {
			bundleBytes = append(bundleBytes, f.Payload...)
		}
	}
	if string(bundleBytes) != bundleBody {
		t.Fatalf("bundle bytes mismatch: %q", string(bundleBytes))
	}

	// 5. Close handshake: shell sends close_clicked → router runs the
	//    close handshake with the app (which auto-confirms) → router
	//    broadcasts session.patch with window.delete.
	writeCtrl(t, shell, wire.NewShellWindowCloseClicked(winID))
	for {
		switch m := readCtrl(t, shell).(type) {
		case wire.ShellSessionPatch:
			for _, p := range m.Patches {
				if p.Op == wire.SessionPatchWindowDelete && p.WindowID == winID {
					goto deletePatchFound
				}
			}
		}
	}
deletePatchFound:

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
