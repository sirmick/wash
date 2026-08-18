package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

// ── the log has to tell the story (GH #22/#23 forensics) ────────────────
//
// The reason #23 took a report to find is that its ROOT event was silent.
// The transport write failed, the scheduler closed, and the function
// returned — no line. The first written evidence was an app exiting with
// "qos scheduler closed", which reads like an app fault and is not one.
//
// These pin the lines that make the sequence readable after the fact.
// A log claim nobody asserts is a claim that quietly rots.

// captureLog collects router log lines for assertions.
func captureLog() (Logger, func() string) {
	var mu sync.Mutex
	var sb strings.Builder
	return func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			sb.WriteString(fmt.Sprintf(format, args...))
			sb.WriteByte('\n')
		}, func() string {
			mu.Lock()
			defer mu.Unlock()
			return sb.String()
		}
}

// The departure itself must be logged — with the queue depths that name
// who was stuck behind it.
func TestTransportWriteFailureIsLogged(t *testing.T) {
	logf, dump := captureLog()
	r := NewRouter(Config{}, NewRegistry(), logf)
	pp := wiretest.NewPipePair()
	sess := &ShellSession{
		Transport:   pp.EndA(),
		router:      r,
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
		connID:      7,
	}
	// Close the FE end so the very first write fails: that is a browser
	// that has gone away between frames.
	_ = pp.EndA().Close()
	_ = pp.EndB().Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sess.drainLoop(ctx)
	// Keep feeding until the drainer gives up. One frame is not reliably
	// enough: a pipe write can succeed after the far end is closed, and
	// the drainer then loops back to wait for more — which is how the
	// first version of this test hung instead of failing.
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case <-sess.drainerDone:
			done = true
		case <-deadline:
			t.Fatal("drainLoop never noticed the dead transport")
		default:
			_ = sess.scheduler.Submit(context.Background(), wire.Frame{Channel: 9, Payload: []byte("x")})
		}
	}

	got := dump()
	if !strings.Contains(got, "shell: transport write failed") {
		t.Fatalf("the root event was not logged; log was:\n%s", got)
	}
	for _, want := range []string{"conn=7", "queued(", "apps keep running"} {
		if !strings.Contains(got, want) {
			t.Errorf("log is missing %q — it reads:\n%s", want, got)
		}
	}
}

// The drop and the recovery are two halves of one story: without the
// second, the log shows a session going dark and never says it came back.
func TestShellGoneAndRecoveryAreBothLogged(t *testing.T) {
	logf, dump := captureLog()
	r := NewRouter(Config{}, NewRegistry(), logf)
	sess := stalledShell(t)
	inst, ch := appWithBoundChannel(t, r, sess, wire.ClassInteractive)
	sess.scheduler.Close()

	for i := 0; i < 3; i++ {
		if err := inst.dispatch(wire.Frame{Channel: ch, Payload: []byte("lost")}); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	}
	if got := dump(); !strings.Contains(got, "shell gone mid-write") {
		t.Fatalf("the drop was not logged:\n%s", got)
	}
	// Exactly one line for the stretch, however many frames were dropped.
	if n := strings.Count(dump(), "shell gone mid-write"); n != 1 {
		t.Errorf("logged the drop %d times, want 1 — a chatty app would flood the log", n)
	}

	// A shell reattaches: bind the channel to a live session and let a
	// frame through.
	live, _, cleanup := newTestShellSession(t)
	defer cleanup()
	b := r.lookupChannel(ch)
	b.shellMu.Lock()
	b.shell = live
	b.shellMu.Unlock()
	if err := inst.dispatch(wire.Frame{Channel: ch, Payload: []byte("through")}); err != nil {
		t.Fatalf("dispatch after reattach: %v", err)
	}

	got := dump()
	if !strings.Contains(got, "shell back") {
		t.Fatalf("recovery was not logged; the log shows a session going dark and never coming back:\n%s", got)
	}
	// The count is the size of what the reattaching shell had to replay.
	if !strings.Contains(got, "dropping 3 frame(s)") {
		t.Errorf("recovery line does not say how much was dropped:\n%s", got)
	}
}

// Saturation is the LEADING indicator: a producer parked here is the one
// that dies if the browser then goes. It used to be a counter and
// nothing more.
func TestQueueSaturationIsLogged(t *testing.T) {
	logf, dump := captureLog()
	r := NewRouter(Config{}, NewRegistry(), logf)
	pp := wiretest.NewPipePair()
	sess := &ShellSession{
		Transport:   pp.EndA(),
		router:      r,
		scheduler:   NewScheduler(),
		drainerDone: make(chan struct{}),
		connID:      3,
	}
	t.Cleanup(func() { _ = pp.EndA().Close(); _ = pp.EndB().Close() })
	sess.installStallLog()

	n := ClassQueueSize[wire.ClassInteractive]
	for i := 0; i < n; i++ {
		if !sess.scheduler.TrySubmit(wire.Frame{Channel: 9, Payload: []byte("fill")}) {
			break
		}
	}
	// One more Submit would block; close first so it returns instead of
	// pinning the test, and the stall hook still fires on the way in.
	sess.scheduler.Close()
	_ = sess.scheduler.Submit(context.Background(), wire.Frame{Channel: 9, Payload: []byte("stalls")})

	got := dump()
	if !strings.Contains(got, "shell: queue saturated") {
		t.Fatalf("a blocking Submit logged nothing; the log was:\n%s", got)
	}
	for _, want := range []string{"conn=3", "depth=", "stalled_for="} {
		if !strings.Contains(got, want) {
			t.Errorf("saturation line is missing %q:\n%s", want, got)
		}
	}
}

// Throttled: a queue stays saturated for as long as the browser is
// behind, and one line per blocked frame would bury the evidence.
func TestQueueSaturationLogIsThrottled(t *testing.T) {
	logf, dump := captureLog()
	r := NewRouter(Config{}, NewRegistry(), logf)
	pp := wiretest.NewPipePair()
	sess := &ShellSession{
		Transport: pp.EndA(), router: r, scheduler: NewScheduler(),
		drainerDone: make(chan struct{}), connID: 1,
	}
	t.Cleanup(func() { _ = pp.EndA().Close(); _ = pp.EndB().Close() })
	sess.installStallLog()

	n := ClassQueueSize[wire.ClassInteractive]
	for i := 0; i < n; i++ {
		sess.scheduler.TrySubmit(wire.Frame{Channel: 9, Payload: []byte("fill")})
	}
	sess.scheduler.Close()
	for i := 0; i < 50; i++ {
		_ = sess.scheduler.Submit(context.Background(), wire.Frame{Channel: 9, Payload: []byte("stall")})
	}

	if n := strings.Count(dump(), "queue saturated"); n != 1 {
		t.Errorf("logged saturation %d times for 50 stalled submits, want 1", n)
	}
}
