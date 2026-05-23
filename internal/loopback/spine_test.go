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

	// fr is a frame reader shared by every phase of the test. It
	// tolerates raw bundle frames whenever they show up, accumulating
	// the bytes into fr.bundleBytes so phase 4's assertion still
	// works no matter which phase happens to consume the raw frames
	// off the wire. Without this, any phase that calls readCtrl-
	// flavoured logic races against bundle delivery and fatals on
	// "expected channel 0, got N".
	fr := &frameReader{t: t, shell: shell}

	// 0. Catalog goes out first on shell connect.
	if _, ok := fr.nextCtrl().(wire.ShellCatalog); !ok {
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
		switch m := fr.nextCtrl().(type) {
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
	fr.targetInstance = declared.InstanceID
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

	// 3+4. Title patch and bundle delivery race each other on the
	// wire — the SDK ships the bundle on a kind=bundle raw channel
	// right after handshake, and the router replays it to the shell
	// concurrently with whatever ctrl frames OnMapped's SetTitle
	// produces. fr handles raw bundle frames transparently; we just
	// loop until both signals (title patch + bundle done) arrive.
	for !fr.bundleDone || !fr.titlePatchSeen(winID, mappedTitle) {
		fr.nextCtrl()
	}
	if string(fr.bundleBytes) != bundleBody {
		t.Fatalf("bundle bytes mismatch: %q", string(fr.bundleBytes))
	}

	// 5. Close handshake: shell sends close_clicked → router runs the
	//    close handshake with the app (which auto-confirms) → router
	//    broadcasts session.patch with window.delete.
	writeCtrl(t, shell, wire.NewShellWindowCloseClicked(winID))
	for {
		switch m := fr.nextCtrl().(type) {
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

// frameReader is a phase-spanning shell-side frame consumer. It
// blocks on the next ctrl frame on channel 0 but transparently
// accumulates raw bundle bytes (channel != 0) into bundleBytes, so a
// raw frame arriving "early" (during phase 1's catalog/declared/
// window drain, say) doesn't fatal the test on "expected channel 0".
// Bundle bind/unbind ctrl frames are also tracked so phase 4 can
// just check bundleDone instead of running its own mixed reader.
//
// targetInstance is set once phase 1 learns the app's instance id;
// before then any ShellChannelBind{kind:bundle} is ignored (there
// shouldn't be any — the SDK uploads the bundle after handshake).
type frameReader struct {
	t              *testing.T
	shell          wire.FrameTransport
	targetInstance string

	bundleChannelID uint32
	bundleBytes     []byte
	bundleDone      bool

	titlePatches []wire.ShellSessionPatch
}

func (fr *frameReader) nextCtrl() any {
	fr.t.Helper()
	for {
		f, err := fr.shell.ReadFrame()
		if err != nil {
			fr.t.Fatalf("read: %v", err)
		}
		if f.Channel != 0 {
			if fr.bundleChannelID != 0 && f.Channel == fr.bundleChannelID {
				fr.bundleBytes = append(fr.bundleBytes, f.Payload...)
			}
			continue
		}
		m, err := wire.DecodeCtrl(f.Payload)
		if err != nil {
			fr.t.Fatalf("decode: %v", err)
		}
		switch v := m.(type) {
		case wire.ShellChannelBind:
			if v.Kind == wire.ChannelKindBundle &&
				fr.targetInstance != "" && v.InstanceID == fr.targetInstance {
				fr.bundleChannelID = v.ChannelID
			}
		case wire.ShellChannelUnbind:
			if fr.bundleChannelID != 0 && v.ChannelID == fr.bundleChannelID {
				fr.bundleDone = true
			}
		case wire.ShellSessionPatch:
			fr.titlePatches = append(fr.titlePatches, v)
		}
		return m
	}
}

// titlePatchSeen reports whether any session.patch buffered so far
// carries the mapped title on winID.
func (fr *frameReader) titlePatchSeen(winID uint32, want string) bool {
	for _, sp := range fr.titlePatches {
		for _, p := range sp.Patches {
			if p.Op == wire.SessionPatchWindowUpsert && p.Window != nil &&
				p.Window.WindowID == winID && p.Window.Title == want {
				return true
			}
		}
	}
	return false
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
