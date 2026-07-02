package loopback

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sirmick/wash/internal/pty"
	"github.com/sirmick/wash/internal/router"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// TestPTYOutputRidesBulkClass — (REVIEW-DATAPATH F1) production terminal
// output must ride ClassBulk so the credit / behind / resync machinery (Fix
// B) actually applies to it, instead of sharing the Interactive queue with
// window ops and other apps. Drives a REAL pty.Session through a real
// router+SDK over in-memory pipes and asserts the wire class of its output
// frames is Bulk.
func TestPTYOutputRidesBulkClass(t *testing.T) {
	manifest := sdk.Manifest{
		ID:              "com.wash.test",
		Name:            "PTY Class",
		Version:         "0.9.0",
		ProtocolVersion: sdk.ProtocolVersion,
		Element:         "wash-app-test",
		Surface:         sdk.SurfaceWindow,
		Icon:            "data:image/svg+xml,Q",
		Instancing:      sdk.InstancingMulti,
		Window:          &sdk.WindowHints{DefaultWidth: 320, DefaultHeight: 200},
	}
	rtrManifest := &router.Manifest{
		ID:              manifest.ID,
		Name:            manifest.Name,
		Version:         manifest.Version,
		ProtocolVersion: router.ProtocolVersion,
		Element:         manifest.Element,
		Surface:         router.SurfaceWindow,
		Icon:            manifest.Icon,
		Instancing:      router.InstancingMulti,
		Window:          &router.WindowHints{DefaultWidth: 320, DefaultHeight: 200},
	}

	reg := router.NewRegistry()
	// No-op logger: the router's drain/idle goroutines can log AFTER this test
	// function returns (on the deferred cancel), and t.Logf after completion
	// panics. The logs aren't needed for the assertion.
	r := router.NewRouter(router.Config{}, reg, func(string, ...any) {})

	appPair := wiretest.NewPipePair()
	shellPair := wiretest.NewPipePair()

	ctx, cancel := context.WithCancel(context.Background())

	appDone := make(chan struct{})
	shellDone := make(chan struct{})
	go func() { _ = r.HandleApp(ctx, appPair.EndA(), rtrManifest, nil); close(appDone) }()
	go func() { _ = r.HandleShell(ctx, shellPair.EndA()); close(shellDone) }()
	// Join the router goroutines before returning so none outlives the test.
	defer func() {
		cancel()
		_ = appPair.EndA().Close()
		_ = shellPair.EndA().Close()
		<-appDone
		<-shellDone
	}()

	def := &sdk.AppDef{
		Manifest: manifest,
		Assets:   fstest.MapFS{"index.js": &fstest.MapFile{Data: []byte("/* test */")}},
	}
	c, err := sdk.ConnectWith(appPair.EndB(), def)
	if err != nil {
		t.Fatalf("sdk connect: %v", err)
	}
	go func() { _ = c.Run(ctx) }()

	shell := shellPair.EndB()

	// Wait for the window to exist (handshake done) so the pty's channel
	// open is accepted for a window the app owns.
	waitForWindow(t, shell)
	win := c.WindowID()
	if win == 0 {
		t.Fatal("app has no window id after handshake")
	}

	// Open a REAL pty running a trivial command that emits a marker.
	ptyCtx, ptyCancel := context.WithTimeout(ctx, 5*time.Second)
	defer ptyCancel()
	sess, err := pty.Open(ptyCtx, c, win, 80, 24,
		[]string{"/bin/sh", "-c", "printf WASHBULK; sleep 0.3"}, nil, nil)
	if err != nil {
		t.Skipf("pty unavailable in this environment: %v", err)
	}
	defer sess.Close()

	// On the shell side: find the generic channel the pty opened (its
	// channel.bind with kind==""), then read its raw output frames and assert
	// they carry ClassBulk.
	ptyChan := uint32(0)
	sawBulkOutput := false
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && !sawBulkOutput {
		f, err := shell.ReadFrame()
		if err != nil {
			t.Fatalf("shell read: %v", err)
		}
		if f.Channel == 0 {
			msg, derr := wire.DecodeCtrl(f.Payload)
			if derr != nil {
				continue
			}
			if b, ok := msg.(wire.ShellChannelBind); ok && b.Kind == wire.ChannelKindGeneric {
				ptyChan = b.ChannelID
			}
			continue
		}
		if ptyChan != 0 && f.Channel == ptyChan {
			if f.Class() != wire.ClassBulk {
				t.Fatalf("pty output frame on channel %d has class %v, want Bulk", f.Channel, f.Class())
			}
			sawBulkOutput = true
		}
	}
	if ptyChan == 0 {
		t.Fatal("never saw the pty's generic channel.bind")
	}
	if !sawBulkOutput {
		t.Fatal("no pty output frame observed on the shell — did the Bulk stream flow?")
	}
}

// waitForWindow drains shell ctrl frames until a session snapshot/patch lands,
// proving the app handshake completed.
func waitForWindow(t *testing.T, shell wire.FrameTransport) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f, err := shell.ReadFrame()
		if err != nil {
			t.Fatalf("read pre-pty: %v", err)
		}
		if f.Channel != 0 {
			continue
		}
		msg, err := wire.DecodeCtrl(f.Payload)
		if err != nil {
			continue
		}
		if _, ok := msg.(wire.ShellSessionSnapshot); ok {
			return
		}
		if _, ok := msg.(wire.ShellSessionPatch); ok {
			return
		}
	}
	t.Fatal("app window never appeared (no snapshot/patch)")
}
