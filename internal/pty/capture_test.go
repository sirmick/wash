package pty

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCaptureKeepsTheTailAndSaysSo(t *testing.T) {
	c := &capture{max: 10}
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got, trunc := c.snapshot(); got != "hello" || trunc {
		t.Fatalf("under the limit: got %q trunc=%v, want %q false", got, trunc, "hello")
	}
	// Exactly at the limit is still whole — an off-by-one here would
	// report truncation on output that fits.
	if _, err := c.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	if got, trunc := c.snapshot(); got != "hello12345" || trunc {
		t.Fatalf("at the limit: got %q trunc=%v, want %q false", got, trunc, "hello12345")
	}
	// Over it: the TAIL survives, because the end of a build log is the
	// part worth keeping.
	if _, err := c.Write([]byte("XY")); err != nil {
		t.Fatal(err)
	}
	got, trunc := c.snapshot()
	if got != "llo12345XY" || !trunc {
		t.Fatalf("over the limit: got %q trunc=%v, want %q true", got, trunc, "llo12345XY")
	}
}

func TestCaptureHandlesAWriteBiggerThanItself(t *testing.T) {
	c := &capture{max: 4}
	if _, err := c.Write([]byte("abcdefgh")); err != nil {
		t.Fatal(err)
	}
	// Write reports the full length it accepted (io.Writer's contract —
	// io.MultiWriter treats a short write as an error and would tear down
	// the tee, taking the terminal with it).
	if got, trunc := c.snapshot(); got != "efgh" || !trunc {
		t.Fatalf("got %q trunc=%v, want %q true", got, trunc, "efgh")
	}
	n, err := c.Write([]byte("xyz"))
	if n != 3 || err != nil {
		t.Fatalf("Write returned (%d, %v), want (3, nil) — a short write breaks io.MultiWriter", n, err)
	}
}

func TestRecordExitDistinguishesCodesFromSignals(t *testing.T) {
	// A code.
	cmd := exec.Command("sh", "-c", "exit 3")
	_ = cmd.Run()
	s := &Session{}
	s.recordExit(cmd.ProcessState)
	code, sig, exited := s.ExitStatus()
	if !exited || code != 3 || sig != "" {
		t.Errorf("exit 3 → code=%d sig=%q exited=%v, want 3 \"\" true", code, sig, exited)
	}

	// A signal. "exit 137" is what a shell reports for SIGKILL, but the
	// caller answering ACP must say `signal`, not invent an exit code.
	killed := exec.Command("sh", "-c", "kill -9 $$")
	_ = killed.Run()
	s2 := &Session{}
	s2.recordExit(killed.ProcessState)
	code2, sig2, exited2 := s2.ExitStatus()
	if !exited2 || sig2 == "" {
		t.Errorf("killed → code=%d sig=%q exited=%v, want a signal name", code2, sig2, exited2)
	}
	if !strings.Contains(strings.ToLower(sig2), "kill") {
		t.Errorf("signal name = %q, want something naming SIGKILL", sig2)
	}

	// Still running: nothing claimed.
	s3 := &Session{}
	if _, _, exited3 := s3.ExitStatus(); exited3 {
		t.Error("a session with no recorded exit reports exited=true")
	}
}

func TestOutputIsEmptyWithoutCapture(t *testing.T) {
	// wash-term opens sessions without WithCapture — the browser is its
	// buffer — and must not pay for a second copy of every byte.
	s := &Session{}
	if text, trunc := s.Output(); text != "" || trunc {
		t.Errorf("Output() = %q,%v on a capture-less session, want \"\",false", text, trunc)
	}
	WithCapture(0)(s)
	if s.cap != nil {
		t.Error("WithCapture(0) allocated a buffer; a zero limit means off")
	}
}

// An exited child's output may still be draining when a caller woken by
// Done asks for it, or closes the session. Waiting for the drain is what
// keeps a fast command's output — the reaper usually beats the copy.
func TestAwaitDrainWaitsForAnExitedChildsOutput(t *testing.T) {
	s := &Session{done: make(chan struct{}), drained: make(chan struct{})}
	close(s.done)
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(s.drained)
	}()
	start := time.Now()
	s.awaitDrain()
	if waited := time.Since(start); waited < 20*time.Millisecond {
		t.Fatalf("returned after %s, before the output drained", waited)
	}
}

// A child still running has no finished output to wait for, and Output is
// also a live poll: it must not block.
func TestAwaitDrainDoesNotWaitForARunningChild(t *testing.T) {
	s := &Session{done: make(chan struct{}), drained: make(chan struct{})}
	start := time.Now()
	s.awaitDrain()
	if waited := time.Since(start); waited > 10*time.Millisecond {
		t.Fatalf("waited %s on a running child", waited)
	}
}

// A grandchild holding the pty open means EOF never comes; the wait is
// bounded so Close and Output cannot hang on it.
func TestAwaitDrainIsBounded(t *testing.T) {
	old := drainGrace
	drainGrace = 20 * time.Millisecond
	t.Cleanup(func() { drainGrace = old })
	s := &Session{done: make(chan struct{}), drained: make(chan struct{})}
	close(s.done)
	start := time.Now()
	s.awaitDrain()
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waited %s past a %s grace", waited, drainGrace)
	}
}
