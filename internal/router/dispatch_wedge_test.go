package router

import (
	"net"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// TestDispatch_WedgedAppWriteDoesNotBlockShell — (REVIEW-DATAPATH F5/F6 /
// REVIEW-RECONNECT H3) a router→app write to a wedged app BE (not reading its
// socket) used to block the shell's single dispatch loop with no deadline,
// freezing every terminal and window op on that connection. The write now has
// a deadline; on timeout the instance is torn down and dispatch survives, so
// an unrelated frame right after is still processed and the healthy shell
// isn't idle-reaped.
func TestDispatch_WedgedAppWriteDoesNotBlockShell(t *testing.T) {
	old := appWriteTimeout
	appWriteTimeout = 150 * time.Millisecond
	defer func() { appWriteTimeout = old }()

	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	s, feShell, cleanup := newTestShellSession(t)
	s.router = r
	defer cleanup()
	r.registerShell(s)
	setHead(r, s)

	// A wedged app: net.Pipe is unbuffered, and nothing ever reads the far
	// end, so every write to appConn blocks until the deadline fires.
	appConn, farEnd := net.Pipe()
	defer appConn.Close()
	defer farEnd.Close()
	inst := &AppInstance{
		AppID:      "com.wash.term",
		InstanceID: "i-term",
		Transport:  wire.NewStreamTransport(appConn),
		router:     r,
	}
	const ch = 7
	b := &channelBinding{
		channelID: ch,
		kind:      wire.ChannelKindGeneric,
		app:       inst,
		shell:     s,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(1 << 20),
	}
	r.registerChannel(b)

	// Forward a raw FE→app frame. It reaches the wedged app write; without the
	// deadline dispatch would block here forever.
	done := make(chan error, 1)
	t0 := time.Now()
	go func() {
		done <- s.dispatch(wire.Frame{Flags: wire.FlagEnd, Channel: ch, Payload: []byte("stuck")})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dispatch returned an error for a wedged app write (should swallow + tear down): %v", err)
		}
		if d := time.Since(t0); d > 3*time.Second {
			t.Fatalf("dispatch took %s — the app write was not bounded", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch BLOCKED on a wedged app write — the whole desktop would freeze")
	}

	// An unrelated frame right after must still be processed: a ping gets a
	// pong back on the shell's scheduler, proving dispatch is alive.
	pingData, err := wire.EncodeCtrl(wire.NewShellPing(42))
	if err != nil {
		t.Fatalf("encode ping: %v", err)
	}
	if err := s.dispatch(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: pingData}); err != nil {
		t.Fatalf("dispatch of a ping after a wedged write: %v", err)
	}
	sawPong := false
	deadline := time.Now().Add(2 * time.Second)
	for !sawPong && time.Now().Before(deadline) {
		f := readWithin(t, feShell, 500*time.Millisecond)
		if f.Channel != ChannelControl {
			continue
		}
		msg, derr := wire.DecodeCtrl(f.Payload)
		if derr != nil {
			t.Fatalf("decode ctrl: %v", derr)
		}
		if pong, ok := msg.(wire.ShellPong); ok && pong.Seq == 42 {
			sawPong = true
		}
	}
	if !sawPong {
		t.Error("no pong after a wedged app write — dispatch loop did not survive")
	}

	// The shell stamped liveness (dispatch ran), so it's not falsely reap-due.
	if s.idleReapDue(time.Now(), readIdleTimeout) {
		t.Error("healthy shell conn falsely idle-reap-due after a wedged app write")
	}

	// The wedged instance's transport was closed by the teardown.
	if _, err := appConn.Write([]byte{0}); err == nil {
		t.Error("wedged app transport was not closed after the write timed out")
	}
}
