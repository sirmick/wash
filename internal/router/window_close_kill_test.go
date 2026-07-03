package router

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// TestHelperIgnoreSIGTERM is not a real test — it's a helper subprocess that
// installs SIG_IGN for SIGTERM, prints READY, then blocks, standing in for an
// app that confirms a window close but hangs in shutdown. It runs only when
// re-exec'd with WASH_TEST_IGNORE_SIGTERM=1; SIGKILL is the only way out.
func TestHelperIgnoreSIGTERM(t *testing.T) {
	if os.Getenv("WASH_TEST_IGNORE_SIGTERM") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	fmt.Println("READY") // os.Stdout is unbuffered — the parent gates on this
	time.Sleep(time.Hour) // a timer keeps the runtime off the deadlock detector
}

// TestWindowCloseEscalatesToKill is the regression for REVIEW-RECONNECT M7:
// a confirmed window close SIGTERMs the app, and if the app hangs in
// shutdown (ignores SIGTERM) the router must escalate to SIGKILL so the
// instance is torn down and removed from r.apps rather than pinned forever
// with its window already destroyed.
func TestWindowCloseEscalatesToKill(t *testing.T) {
	old := windowCloseKillGrace
	windowCloseKillGrace = 300 * time.Millisecond
	defer func() { windowCloseKillGrace = old }()

	reg := NewRegistry()
	r := NewRouter(Config{NoSession: true}, reg, func(string, ...any) {})

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperIgnoreSIGTERM")
	cmd.Env = append(os.Environ(), "WASH_TEST_IGNORE_SIGTERM=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	// Gate on the helper having installed SIG_IGN (it prints READY after) —
	// otherwise a SIGTERM racing the child's startup kills it under the
	// default disposition and never exercises the escalation.
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "READY" {
				close(ready)
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("helper never signalled READY")
	}

	inst := &AppInstance{
		AppID:      "com.test.hang",
		InstanceID: "inst-hang-1",
		Manifest:   &Manifest{ID: "com.test.hang"},
		Cmd:        cmd,
		router:     r,
	}
	r.registerApp(inst)
	// Mimic the app loop goroutine: when the process finally dies, tearDown
	// unregisters the instance (what appByInstance/waitInstanceGone watch).
	go func() { _ = cmd.Wait(); r.tearDown(inst) }()

	start := time.Now()
	r.terminateWindowedApp(inst) // SIGTERM is ignored → must escalate to SIGKILL

	if !r.waitInstanceGone("inst-hang-1", 2*time.Second) {
		_ = cmd.Process.Kill() // don't leak the child if the assertion fails
		t.Fatal("instance still registered — SIGTERM was ignored and no SIGKILL escalation happened")
	}
	// It should have taken at least the grace window — proving SIGTERM alone
	// didn't stop it and the escalation did the work.
	if elapsed := time.Since(start); elapsed < windowCloseKillGrace {
		t.Fatalf("teardown in %v, less than the grace %v — helper exited on SIGTERM, not the escalation", elapsed, windowCloseKillGrace)
	}
}
