package loopback

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sirmick/wash/internal/router"
	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// readFrameWithDeadline is a diagnostic wrapper around ReadFrame that
// returns an error if no frame arrives within d. The describe callback
// is invoked only on timeout and used to build the error message —
// callers thread the test's current expectations through it so a hang
// names what was missing (bundle bytes vs title patch, etc.) instead
// of just timing out with no context.
func readFrameWithDeadline(t wire.FrameTransport, d time.Duration, describe func() string) (wire.Frame, error) {
	type rr struct {
		f   wire.Frame
		err error
	}
	ch := make(chan rr, 1)
	go func() {
		f, err := t.ReadFrame()
		ch <- rr{f, err}
	}()
	select {
	case r := <-ch:
		return r.f, r.err
	case <-time.After(d):
		return wire.Frame{}, fmt.Errorf("no frame for %v; state: %s", d, describe())
	}
}

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
		Version:         "0.9.0",
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
		Version:         "0.9.0",
		ProtocolVersion: router.ProtocolVersion,
		Element:         "wash-app-about",
		Surface:         router.SurfaceWindow,
		Icon:            "data:image/svg+xml,W",
		Instancing:      router.InstancingMulti,
		Window:          &router.WindowHints{DefaultWidth: 480, DefaultHeight: 320},
	}

	// Build a router with a hand-seeded registry: bundle delivery
	// reads from the registry entry, so HandleApp-only tests must
	// pre-register.
	reg := router.NewRegistry()
	reg.RegisterEntry(&router.Entry{
		Path:     "test:com.wash.about",
		Manifest: rtrManifest,
		Bundle:   []byte(bundleBody),
	})
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
			// Capture the instance id immediately so the bundle
			// bind/raw/unbind frames that follow are recognized by
			// frameReader (they ship back-to-back with the declare,
			// since bundles come from the registry).
			fr.targetInstance = declared.InstanceID
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

	// 3+4. Title patch and bundle delivery race each other on the
	// wire — the SDK ships the bundle on a kind=bundle raw channel
	// right after handshake, and the router replays it to the shell
	// concurrently with whatever ctrl frames OnMapped's SetTitle
	// produces. fr handles raw bundle frames transparently; we just
	// loop until both signals (title patch + bundle done) arrive.
	for !fr.bundleComplete() || !fr.titlePatchSeen(winID, mappedTitle) {
		fr.readOne()
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
// just check bundleComplete() instead of running its own mixed reader.
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
	// bundleSize is the byte count the bind announced (post-encoding).
	// The bundle is complete when bundleBytes reaches it — NOT when the
	// unbind arrives. Bundle payload is Bulk class while the unbind is a
	// ctrl frame, so under strict-priority scheduling the unbind can
	// legitimately overtake the data it terminates; completing on the
	// unbind made this test assert an ordering the router explicitly does
	// not promise (see replayBundleToShell, docs/TEST_FLAKES.md B2).
	bundleSize int
	// bundleUnbound records the unbind for diagnostics only.
	bundleUnbound bool
	// pendingRaw holds raw frames seen before the bind that names their
	// channel, for the same reason: data may precede its own bind.
	pendingRaw map[uint32][]byte

	titlePatches []wire.ShellSessionPatch
	// snapshotWindows captures any window upsert that lands in a
	// ShellSessionSnapshot. If SetTitle runs BEFORE HandleShell's
	// snapshot, the new title is baked into the snapshot and
	// winSession.setTitle returns nil patches for the redundant
	// SetTitle — there is no follow-on patch to observe.
	snapshotWindows []wire.SessionWindow
}

// nextCtrl reads until the next CONTROL frame and returns it. Raw frames
// encountered on the way are still accounted (see readOne).
//
// Callers waiting on a condition that raw frames can satisfy (bundle
// bytes) must use readOne instead: nextCtrl blocks until a ctrl frame
// shows up, and the bundle's last data frame can legitimately be the
// last thing on the wire — waiting for a ctrl frame after it hangs.
func (fr *frameReader) nextCtrl() any {
	fr.t.Helper()
	for {
		if m := fr.readOne(); m != nil {
			return m
		}
	}
}

// readOne reads exactly one frame and accounts it: raw frames append to
// the bundle (or to pendingRaw until the bind names their channel),
// control frames update the title/snapshot/bundle bookkeeping. Returns
// the decoded ctrl message, or nil for a raw or unmodelled frame — so a
// caller can re-test its wait condition after EVERY frame.
func (fr *frameReader) readOne() any {
	fr.t.Helper()
	{
		f, err := readFrameWithDeadline(fr.shell, 10*time.Second, func() string {
			return fmt.Sprintf("bundleBytes=%d/%d unbound=%v titlePatches=%d snapshotWindows=%d",
				len(fr.bundleBytes), fr.bundleSize, fr.bundleUnbound,
				len(fr.titlePatches), len(fr.snapshotWindows))
		})
		if err != nil {
			fr.t.Fatalf("read: %v", err)
		}
		if f.Channel != 0 {
			if fr.bundleChannelID != 0 && f.Channel == fr.bundleChannelID {
				fr.bundleBytes = append(fr.bundleBytes, f.Payload...)
			} else {
				// Not bound yet: hold rather than drop.
				if fr.pendingRaw == nil {
					fr.pendingRaw = map[uint32][]byte{}
				}
				fr.pendingRaw[f.Channel] = append(fr.pendingRaw[f.Channel], f.Payload...)
			}
			return nil
		}
		m, err := wire.DecodeCtrl(f.Payload)
		if errors.Is(err, wire.ErrUnknownCtrl) {
			// Forward compatible: the router ships ctrl frames this test
			// does not model (link.stats, …). Skip, don't fail.
			return nil
		}
		if err != nil {
			fr.t.Fatalf("decode: %v", err)
		}
		switch v := m.(type) {
		case wire.ShellChannelBind:
			if v.Kind == wire.ChannelKindBundle &&
				fr.targetInstance != "" && v.InstanceID == fr.targetInstance {
				fr.bundleChannelID = v.ChannelID
				fr.bundleSize = int(v.Size)
				if held := fr.pendingRaw[v.ChannelID]; len(held) > 0 {
					fr.bundleBytes = append(fr.bundleBytes, held...)
					delete(fr.pendingRaw, v.ChannelID)
				}
			}
		case wire.ShellChannelUnbind:
			if fr.bundleChannelID != 0 && v.ChannelID == fr.bundleChannelID {
				fr.bundleUnbound = true
			}
		case wire.ShellSessionPatch:
			fr.titlePatches = append(fr.titlePatches, v)
		case wire.ShellSessionSnapshot:
			fr.snapshotWindows = append(fr.snapshotWindows, v.Windows...)
		}
		return m
	}
}

// titlePatchSeen reports whether any session.patch OR session.snapshot
// buffered so far carries the mapped title on winID. The snapshot path
// covers the race where SetTitle is applied to winSession BEFORE
// HandleShell's setup runs — in that case the snapshot the shell
// receives already carries the new title and setTitle returns nil
// patches for the (now-redundant) SetTitle call, so we'd wait
// forever for a patch that never arrives.
func (fr *frameReader) titlePatchSeen(winID uint32, want string) bool {
	for _, sp := range fr.titlePatches {
		for _, p := range sp.Patches {
			if p.Op == wire.SessionPatchWindowUpsert && p.Window != nil &&
				p.Window.WindowID == winID && p.Window.Title == want {
				return true
			}
		}
	}
	for _, w := range fr.snapshotWindows {
		if w.WindowID == winID && w.Title == want {
			return true
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

// bundleComplete reports whether every announced bundle byte has arrived.
// The bind's Size is authoritative; before a bind is seen there is nothing
// to be complete about.
func (fr *frameReader) bundleComplete() bool {
	if fr.bundleChannelID == 0 || fr.bundleSize == 0 {
		return false
	}
	return len(fr.bundleBytes) >= fr.bundleSize
}
