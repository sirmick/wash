package router

import (
	"errors"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// A browser that goes away must not take backend apps with it (GH #23).
//
// The reported failure, from a real box:
//
//	app com.wash.session loop instance=i-8: router: qos scheduler closed
//	app com.wash.session crashed: code=1 uptime=48m4s instance=i-8
//	...
//	app com.wash.session up instance=i-16
//
// The chain: a transport write fails, drainLoop closes the QoS scheduler,
// and producers blocked in Submit unblock with ErrSchedulerClosed. That
// error returns up through AppInstance.dispatch — and loop() is just
// wire.ReadLoop, which ends on ANY dispatch error. So an app that
// happened to be writing to the shell when the browser vanished is torn
// down by it, and the reconnect brings up a NEW instance holding none of
// the old state (which is also how the window model and its icons go).
//
// The e2e torture (e2e/tests/reconnect-torture.spec.ts) drops the socket
// repeatedly under load and does NOT reproduce this: over loopback the
// queues never fill, so Submit never blocks and the close is never
// observed mid-write. That is exactly why this lives here — closing the
// scheduler directly is the deterministic version of a race that needs a
// slow browser to lose.
//
// The contract asserted: forwarding to a departed shell fails the FRAME,
// never the app. The raw path already knows this (a nil shell drops to
// the ring buffer and returns nil); these are the paths that did not.

// stalledShell is a shell whose FE has stopped reading: a scheduler with
// NO drainer, so frames pile up until the queue is at capacity. That is
// the state a real browser reaches on a network stall or a sleeping
// laptop, and it is the ONLY state in which Submit blocks — and so the
// only one in which closing the scheduler is observed by a producer.
//
// Getting this wrong is why the first version of this test passed
// against the bug: with an empty queue, Submit takes the fast path and
// returns nil even on a closed scheduler.
func stalledShell(t *testing.T) *ShellSession {
	t.Helper()
	pp := wiretest.NewPipePair()
	sess := &ShellSession{
		Transport:   pp.EndA(),
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
	}
	t.Cleanup(func() {
		_ = pp.EndA().Close()
		_ = pp.EndB().Close()
	})
	// Fill the Interactive queue to capacity. TrySubmit so the filling
	// itself cannot block if the sizing changes underneath this test.
	n := ClassQueueSize[wire.ClassInteractive]
	for i := 0; i < n; i++ {
		if !sess.scheduler.TrySubmit(wire.Frame{Channel: 9, Payload: []byte("backlog")}) {
			break
		}
	}
	if sess.scheduler.TrySubmit(wire.Frame{Channel: 9, Payload: []byte("one more")}) {
		t.Fatal("queue did not reach capacity — the test would not exercise the blocking path")
	}
	return sess
}

// appWithBoundChannel wires an app and a channel binding to a shell
// session, the way handleChannelOpen would.
func appWithBoundChannel(t *testing.T, r *Router, sess *ShellSession, class wire.Class) (*AppInstance, uint32) {
	t.Helper()
	pair := wiretest.NewPipePair()
	inst := &AppInstance{
		Transport:  pair.EndA(),
		AppID:      "com.wash.session",
		InstanceID: "i-1",
		Manifest:   &Manifest{ID: "com.wash.session"},
		router:     r,
	}
	r.registerApp(inst)
	id := r.allocChannelID()
	r.registerChannel(&channelBinding{
		channelID: id,
		app:       inst,
		shell:     sess,
		kind:      wire.ChannelKindGeneric,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		// noCredit puts this on the LOSSLESS forward path — the one that
		// calls scheduler.Submit and can therefore see the close. The
		// credit-gated Bulk path is deliberately non-blocking and was
		// never the problem.
		noCredit: true,
	})
	t.Cleanup(func() {
		_ = pair.EndA().Close()
		_ = pair.EndB().Close()
	})
	return inst, id
}

// The regression: dispatching a raw frame to a shell whose scheduler has
// been closed must not return an error, because that error ends the
// app's read loop and kills the process.
func TestDispatchSurvivesAClosedShellScheduler(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), nil)
	sess := stalledShell(t)
	inst, ch := appWithBoundChannel(t, r, sess, wire.ClassInteractive)

	// The browser goes away: drainLoop's transport write fails and it
	// closes the scheduler, unblocking the producer parked on the full
	// queue. Done directly here — that is the state, and reproducing the
	// timing is what the e2e cannot do reliably.
	sess.scheduler.Close()

	err := inst.dispatch(wire.Frame{Channel: ch, Payload: []byte("output after the browser left")})
	if err != nil {
		t.Fatalf("dispatch returned %v — this ends AppInstance.loop and tears the app down; "+
			"a departed browser must fail the frame, not the app", err)
	}
	if errors.Is(err, ErrSchedulerClosed) {
		t.Fatal("ErrSchedulerClosed reached dispatch's caller")
	}
}

// The bytes are not silently lost: they land in the ring buffer, which is
// what a reattaching shell replays from. Dropping the frame is only
// acceptable BECAUSE the buffer holds it.
func TestFrameForAClosedShellIsStillBuffered(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), nil)
	sess := stalledShell(t)
	inst, ch := appWithBoundChannel(t, r, sess, wire.ClassInteractive)
	sess.scheduler.Close()

	const payload = "kept for the next shell"
	if err := inst.dispatch(wire.Frame{Channel: ch, Payload: []byte(payload)}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	b := r.lookupChannel(ch)
	if b == nil || b.buf == nil {
		t.Fatal("channel lost its ring buffer")
	}
	if got := string(b.buf.Snapshot()); !strings.Contains(got, payload) {
		t.Errorf("ring buffer = %q, want it to contain %q — the frame was dropped with nowhere to replay from", got, payload)
	}
}

// The app must still be registered afterwards. A test that only checked
// the error could pass while something else tore the instance down.
func TestAppSurvivesAClosedShellScheduler(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), nil)
	sess := stalledShell(t)
	inst, ch := appWithBoundChannel(t, r, sess, wire.ClassInteractive)
	sess.scheduler.Close()

	for i := 0; i < 20; i++ {
		if err := inst.dispatch(wire.Frame{Channel: ch, Payload: []byte("chatter")}); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}
	if got := r.instanceByApp(inst.AppID); got == nil {
		t.Fatal("the app was torn down by a browser going away")
	}
}
