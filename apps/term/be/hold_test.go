package term

import (
	"testing"
	"time"
)

// The exit-hold policy (docs/Review-findings.md "P1 → term"): a tab whose
// command failed, or finished before anyone could have read it, stays on
// screen with its exit status; everything else closes as it always did.
func TestShouldHold(t *testing.T) {
	cases := []struct {
		name    string
		reason  string
		code    int
		signal  string
		execd   bool
		elapsed time.Duration
		want    bool
	}{
		{"shell exits 0 after a while: instant close", "pty eof", 0, "", false, time.Minute, false},
		{"shell exits 0 immediately: still instant — `exit` in a fresh shell", "pty eof", 0, "", false, 100 * time.Millisecond, false},
		{"shell exits non-zero: held", "pty eof", 1, "", false, time.Minute, true},
		{"exec'd command exits 3: held", "pty eof", 3, "", true, time.Minute, true},
		{"exec'd command exits 0 fast: held for reading", "pty eof", 0, "", true, 500 * time.Millisecond, true},
		{"exec'd command exits 0 after the window: instant close", "pty eof", 0, "", true, 5 * time.Second, false},
		{"killed by a signal: held", "pty eof", 0, "killed", false, time.Minute, true},
		{"user closed the tab: never held, whatever the status", "user requested", 3, "", true, 0, false},
		{"window closed: never held", "window closed", 0, "killed", true, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldHold(c.reason, c.code, c.signal, c.execd, c.elapsed); got != c.want {
				t.Errorf("shouldHold(%q, %d, %q, execd=%v, %v) = %v, want %v",
					c.reason, c.code, c.signal, c.execd, c.elapsed, got, c.want)
			}
		})
	}
}
