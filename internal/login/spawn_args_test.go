package login

import (
	"strings"
	"testing"
	"time"
)

func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// The router has always documented that --idle-timeout=0 disables idle
// reaping. Forwarding the flag only when non-zero meant the one value
// that says "never reap" was the one value wash-login could not express:
// it forwarded nothing, the router fell through to its own zero, saw
// --listen-unix and applied a default anyway. The workaround was passing
// an absurd duration that contradicted the flag's own help text.
func TestIdleTimeoutForwardingDistinguishesUnsetFromZero(t *testing.T) {
	cases := []struct {
		name     string
		set      bool
		value    time.Duration
		wantFlag bool
		wantVal  string
	}{
		{name: "unset forwards nothing", set: false, value: 0, wantFlag: false},
		{name: "explicit zero forwards zero", set: true, value: 0, wantFlag: true, wantVal: "0s"},
		{name: "explicit duration forwards it", set: true, value: 90 * time.Minute, wantFlag: true, wantVal: "1h30m0s"},
		// The old shape special-cased non-zero, so this case passed while
		// the one above silently did the opposite of what was asked.
		{name: "unset non-zero still forwards nothing", set: false, value: time.Hour, wantFlag: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Spawner{IdleTimeout: tc.value, IdleTimeoutSet: tc.set}
			args := s.routerArgs("/run/x.sock", "mick", "/run/x.log")
			got, ok := argValue(args, "--idle-timeout")
			if ok != tc.wantFlag {
				t.Fatalf("--idle-timeout present = %v, want %v (args: %s)", ok, tc.wantFlag, strings.Join(args, " "))
			}
			if ok && got != tc.wantVal {
				t.Errorf("--idle-timeout = %q, want %q", got, tc.wantVal)
			}
		})
	}
}

// The never-attached threshold follows the same rule, so an operator can
// turn off leak-reaping too if they mean to.
func TestIdleUnattachedForwarding(t *testing.T) {
	s := &Spawner{IdleTimeoutUnattached: 0, IdleTimeoutUnattachedSet: true}
	if got, ok := argValue(s.routerArgs("/s", "n", "/l"), "--idle-timeout-unattached"); !ok || got != "0s" {
		t.Errorf("explicit zero = (%q, %v), want (\"0s\", true)", got, ok)
	}
	s = &Spawner{IdleTimeoutUnattached: time.Minute}
	if _, ok := argValue(s.routerArgs("/s", "n", "/l"), "--idle-timeout-unattached"); ok {
		t.Error("unset forwarded a value")
	}
}
