package pty

import (
	"strings"
	"testing"
)

// PinTerm is wash-edit's terminal env (and the pin inside WithWashEnv):
// both TERM and COLORTERM describe xterm.js, whatever was inherited.
func TestPinTermPinsBothTerminalVars(t *testing.T) {
	for _, in := range [][]string{
		{"PATH=/bin"},
		{"TERM=dumb", "PATH=/bin"},
		{"TERM=dumb", "COLORTERM=no", "PATH=/bin", "COLORTERM=twice"},
	} {
		out := PinTerm(in)
		var terms, colors int
		for _, kv := range out {
			switch {
			case kv == "TERM=xterm-256color":
				terms++
			case kv == "COLORTERM=truecolor":
				colors++
			case strings.HasPrefix(kv, "TERM=") || strings.HasPrefix(kv, "COLORTERM="):
				t.Errorf("PinTerm(%q) kept %q", in, kv)
			}
		}
		if terms != 1 || colors != 1 {
			t.Errorf("PinTerm(%q) → %q: want exactly one TERM and one COLORTERM pin", in, out)
		}
		if lookupEnv(out, "PATH") != "/bin" {
			t.Errorf("PinTerm(%q) disturbed PATH: %q", in, out)
		}
	}
}
