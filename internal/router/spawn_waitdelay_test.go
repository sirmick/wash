package router

import (
	"testing"
	"time"
)

// TestSpawnWaitDelayUnblocksTeardown is the regression for REVIEW-RECONNECT
// L3: a spawned app tees stdout/stderr through an OS pipe, so cmd.Wait
// blocks until every inheritor of that pipe closes it. A grandchild that
// outlives the app (here a backgrounded sleep) holds the pipe open and would
// pin Wait — and any tearDown gated on it — until the grandchild exits.
// spawnWaitDelay must cap that wait so Wait returns shortly after the app's
// own process exits.
func TestSpawnWaitDelayUnblocksTeardown(t *testing.T) {
	old := spawnWaitDelay
	spawnWaitDelay = 300 * time.Millisecond
	defer func() { spawnWaitDelay = old }()

	// /bin/sh backgrounds `sleep 3` (which inherits the tee'd stdout pipe)
	// then exits immediately. Without WaitDelay, Wait would block ~3s on the
	// lingering grandchild; with it, ~spawnWaitDelay after sh exits.
	res, err := Spawn("/bin/sh", "com.test.gc", "dummy-display", nil, []string{"-c", "sleep 3 &"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if res.Cmd.WaitDelay != spawnWaitDelay {
		t.Fatalf("Spawn did not set WaitDelay: got %v, want %v", res.Cmd.WaitDelay, spawnWaitDelay)
	}

	done := make(chan struct{})
	start := time.Now()
	go func() { _ = res.Cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cmd.Wait blocked past 2s — pinned by the grandchild holding the stdout pipe")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cmd.Wait took %v — WaitDelay did not cap the grandchild pipe wait", elapsed)
	}
}
