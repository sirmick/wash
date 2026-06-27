package router

import (
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// TestHandlePingRepliesPong: a ShellPing is echoed as a ShellPong with the
// same Seq, on the Control class (so it never queues behind bulk), and the
// first ping arms the read-idle watchdog.
func TestHandlePingRepliesPong(t *testing.T) {
	sess, fe, cleanup := newTestShellSession(t)
	defer cleanup()
	sess.router = NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	if sess.sawPing.Load() {
		t.Fatal("watchdog armed before any ping")
	}
	if err := sess.handlePing(wire.NewShellPing(99)); err != nil {
		t.Fatalf("handlePing: %v", err)
	}
	if !sess.sawPing.Load() {
		t.Error("first ping must arm the watchdog (sawPing)")
	}

	f, err := fe.ReadFrame()
	if err != nil {
		t.Fatalf("read pong frame: %v", err)
	}
	if f.Channel != ChannelControl {
		t.Errorf("pong channel = %d, want control %d", f.Channel, ChannelControl)
	}
	if f.Class() != wire.ClassControl {
		t.Errorf("pong class = %v, want Control", f.Class())
	}
	msg, err := wire.DecodeCtrl(f.Payload)
	if err != nil {
		t.Fatalf("decode pong: %v", err)
	}
	pong, ok := msg.(wire.ShellPong)
	if !ok {
		t.Fatalf("got %T, want ShellPong", msg)
	}
	if pong.Seq != 99 {
		t.Errorf("pong seq = %d, want 99 (echoed)", pong.Seq)
	}
}

// TestDispatchPingUpdatesLiveness: routing a ping frame through dispatch both
// arms the watchdog and refreshes the read-liveness clock.
func TestDispatchPingUpdatesLiveness(t *testing.T) {
	sess, fe, cleanup := newTestShellSession(t)
	defer cleanup()
	sess.router = NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	if got := sess.lastReadAtNanos.Load(); got != 0 {
		t.Fatalf("lastReadAt = %d before dispatch, want 0", got)
	}
	data, err := wire.EncodeCtrl(wire.NewShellPing(1))
	if err != nil {
		t.Fatalf("encode ping: %v", err)
	}
	f := wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data}.WithClass(wire.ClassControl)
	if err := sess.dispatch(f); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !sess.sawPing.Load() {
		t.Error("dispatch of a ping must arm the watchdog")
	}
	if sess.lastReadAtNanos.Load() == 0 {
		t.Error("dispatch must refresh lastReadAt")
	}
	// Drain the pong so the drainer/pipe doesn't block on cleanup.
	if _, err := fe.ReadFrame(); err != nil {
		t.Fatalf("drain pong: %v", err)
	}
}

// TestIdleReapDue covers the watchdog decision: dormant until the FE has
// pinged at least once (backward-compat with a heartbeat-less FE), then due
// only once the last inbound frame is older than the timeout.
func TestIdleReapDue(t *testing.T) {
	sess, _, cleanup := newTestShellSession(t)
	defer cleanup()
	now := time.Unix(1_000_000, 0)

	// Not armed: never reap, even with an ancient lastReadAt.
	sess.lastReadAtNanos.Store(now.Add(-10 * time.Minute).UnixNano())
	if sess.idleReapDue(now, readIdleTimeout) {
		t.Error("reaped a connection that never pinged (legacy FE must be safe)")
	}

	// Armed + recent traffic: alive.
	sess.sawPing.Store(true)
	sess.lastReadAtNanos.Store(now.UnixNano())
	if sess.idleReapDue(now, readIdleTimeout) {
		t.Error("reaped a live connection (recent inbound frame)")
	}

	// Armed + silent past the timeout: zombie.
	sess.lastReadAtNanos.Store(now.Add(-(readIdleTimeout + time.Second)).UnixNano())
	if !sess.idleReapDue(now, readIdleTimeout) {
		t.Error("failed to reap a silent armed connection")
	}
}
