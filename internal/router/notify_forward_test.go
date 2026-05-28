package router

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// notifyManifest is a minimal manifest for a background-surface
// singleton service. Mirrors what the real wash-notify ships, scoped
// down to what the router cares about for routing tests.
func notifyManifest() *Manifest {
	return &Manifest{
		ID:              NotifyAppID,
		Name:            "Notifications",
		Version:         "0.0.0",
		ProtocolVersion: ProtocolVersion,
		Surface:         SurfaceBackground,
		Instancing:      InstancingSingleton,
	}
}

// connectApp drives the wire handshake from the app side: writes
// Identity, awaits IdentityAck, returns the live instance_id.
// Failure aborts the test. Mirrors the inline handshake bits already
// used in spine_test.go.
func connectApp(t *testing.T, pp *wiretest.PipePair, appID string) string {
	t.Helper()
	writeCtrl(t, pp.EndB(), wire.NewIdentity(appID, ProtocolVersion, "0.0.0"))
	ack, ok := readCtrl(t, pp.EndB()).(wire.IdentityAck)
	if !ok {
		t.Fatalf("%s: expected IdentityAck", appID)
	}
	return ack.InstanceID
}

// writeEvtFrame is the router-test-package local equivalent of the
// sdk package's writeEvt helper: encodes any wire event and ships it
// on channel 1. Inlined here instead of exporting from sdk because
// the router test package can't easily reach into sdk's _test files.
func writeEvtFrame(t *testing.T, e wire.FrameTransport, m any) {
	t.Helper()
	b, err := wire.EncodeEvt(m)
	if err != nil {
		t.Fatalf("encode evt: %v", err)
	}
	if err := e.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelEvent, Payload: b}); err != nil {
		t.Fatalf("write evt: %v", err)
	}
}

// TestRelayNotifyForwardsToService is the load-bearing M2 check: an
// EvtNotify from any app fans out to BOTH the shell broadcast (existing
// v0.1 behaviour, kept for transient toasts) AND the com.wash.notify
// singleton (new — drives the history surface that the sidebar widget
// will consume).
func TestRelayNotifyForwardsToService(t *testing.T) {
	reg := NewRegistry()
	// Register an entry so resolveRecipient by AppID can resolve. The
	// entry's binary path is unused — HandleApp owns the live
	// transport directly; resolveRecipient only needs the manifest.
	reg.RegisterEntry(&Entry{Path: "/unused/wash-notify", Manifest: notifyManifest()})
	reg.RegisterEntry(&Entry{Path: "/unused/wash-about", Manifest: aboutManifest()})
	r := NewRouter(Config{}, reg, func(format string, args ...any) {
		t.Logf("router: "+format, args...)
	})

	// Bring up the notify service first so it lands in router.singletons
	// before the producer sends — that way resolveRecipient hits the
	// fast path and we don't accidentally test the spawn-on-demand
	// branch (which would need a real binary on disk).
	notifyPair := wiretest.NewPipePair()
	notifyDone := make(chan struct{})
	go func() {
		defer close(notifyDone)
		_ = r.HandleApp(context.Background(), notifyPair.EndA(), notifyManifest(), nil)
	}()
	_ = connectApp(t, notifyPair, NotifyAppID)

	// Now the producer.
	producerPair := wiretest.NewPipePair()
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		_ = r.HandleApp(context.Background(), producerPair.EndA(), aboutManifest(), nil)
	}()
	producerInst := connectApp(t, producerPair, "com.wash.about")
	// Drain the EvtWindowMapped the router ships post-bringUp so it
	// doesn't pollute later reads on this pair.
	_, _ = producerPair.EndB().ReadFrame()

	// Producer emits EvtNotify.
	writeEvtFrame(t, producerPair.EndB(), wire.NewEvtNotify("title-x", "body-y", wire.NotifyLevelWarn))

	// Notify side receives a cross-app app_msg with kind:"notify" and
	// From identifying the producer. relayNotify forwards in a
	// goroutine so we may need to wait briefly.
	gotKind, gotFrom := readNotifyForward(t, notifyPair, 2*time.Second)
	if gotKind != "notify" {
		t.Fatalf("forwarded kind=%q, want notify", gotKind)
	}
	if gotFrom == nil || gotFrom.AppID != "com.wash.about" || gotFrom.InstanceID != producerInst {
		t.Fatalf("from=%+v, want AppID=com.wash.about InstanceID=%q", gotFrom, producerInst)
	}

	producerPair.Close()
	notifyPair.Close()
	waitClose(t, producerDone)
	waitClose(t, notifyDone)
}

// TestRelayNotifyFromNotifyDoesNotLoop confirms a Notify() emitted by
// the notify service itself does NOT loop back through the service
// (which would deadlock on the same handler thread / inflate history
// with self-events).
func TestRelayNotifyFromNotifyDoesNotLoop(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterEntry(&Entry{Path: "/unused/wash-notify", Manifest: notifyManifest()})
	r := NewRouter(Config{}, reg, nil)

	notifyPair := wiretest.NewPipePair()
	notifyDone := make(chan struct{})
	go func() {
		defer close(notifyDone)
		_ = r.HandleApp(context.Background(), notifyPair.EndA(), notifyManifest(), nil)
	}()
	_ = connectApp(t, notifyPair, NotifyAppID)

	// Notify service emits its own Notify (pretend it's a debug event).
	writeEvtFrame(t, notifyPair.EndB(), wire.NewEvtNotify("self", "self-body", wire.NotifyLevelInfo))

	// Expect: NO inbound app_msg arrives on the notify side. Drain
	// with a deadline; if any frame appears, test fails.
	done := make(chan wire.Frame, 1)
	go func() {
		f, err := notifyPair.EndB().ReadFrame()
		if err == nil {
			done <- f
		}
	}()
	select {
	case f := <-done:
		// Some frame arrived. Decode to give a useful failure message.
		if m, err := wire.DecodeEvt(f.Payload); err == nil {
			if msg, ok := m.(wire.EvtAppMsg); ok {
				t.Fatalf("notify service got self-forwarded notify: data=%s", string(msg.Data))
			}
			t.Fatalf("unexpected non-app_msg frame: %T", m)
		}
		t.Fatalf("unexpected frame: %+v", f)
	case <-time.After(200 * time.Millisecond):
		// Expected: nothing arrived. Loop guard worked.
	}

	notifyPair.Close()
	waitClose(t, notifyDone)
}

// readNotifyForward waits for an inbound app_msg with kind="notify"
// on the given pipe. Returns the kind and the From sender. Skips
// non-app_msg frames the router may emit (mapped event, etc.) — for
// the notify service those are technically not produced (no window)
// but the helper is defensive in case the framework grows new
// router-driven events later.
func readNotifyForward(t *testing.T, pp *wiretest.PipePair, timeout time.Duration) (kind string, from *wire.Sender) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, err := pp.EndB().ReadFrame()
		if err != nil {
			t.Fatalf("read forward: %v", err)
		}
		if f.Channel != ChannelEvent {
			continue
		}
		m, err := wire.DecodeEvt(f.Payload)
		if err != nil {
			t.Fatalf("decode evt: %v", err)
		}
		msg, ok := m.(wire.EvtAppMsg)
		if !ok {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			t.Fatalf("unmarshal data: %v", err)
		}
		k, _ := env["kind"].(string)
		return k, msg.From
	}
	t.Fatalf("no notify forward arrived within %v", timeout)
	return "", nil
}
