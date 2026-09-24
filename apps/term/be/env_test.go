package term

import (
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/pty"
)

// A wash terminal must carry the wash environment. M5 removed the agent
// decision socket and, with it, the env transform that wrapped
// pty.WithWashEnv — silently costing every terminal its TERM pin, its
// $WASH_BIN_DIR PATH prefix (so `wash ai` stopped resolving), and the
// display-hint mapping that makes `xclock` work.
//
// Deleting a wrapper must not delete what it wrapped.
func TestTerminalsGetTheWashEnvironment(t *testing.T) {
	env := pty.WithWashEnv([]string{
		"PATH=/usr/bin:/bin",
		"TERM=dumb",
		"WASH_BIN_DIR=/opt/wash/bin",
	})
	var path, term, colorterm string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
		if v, ok := strings.CutPrefix(kv, "TERM="); ok {
			term = v
		}
		if v, ok := strings.CutPrefix(kv, "COLORTERM="); ok {
			colorterm = v
		}
	}
	if !strings.HasPrefix(path, "/opt/wash/bin:") {
		t.Errorf("PATH = %q — wash's own bin dir must come first, or a terminal inside wash N gets wash N-1's tools", path)
	}
	if term != "xterm-256color" {
		t.Errorf("TERM = %q, want xterm-256color", term)
	}
	// xterm.js renders 24-bit colour; bat, delta and neovim only use it
	// when COLORTERM says so. Absent from the router's inherited env (it
	// was not launched from a terminal), so it has to be set here.
	if colorterm != "truecolor" {
		t.Errorf("COLORTERM = %q, want truecolor", colorterm)
	}
}

// An inherited COLORTERM describes whatever launched the router, not the
// xterm in front of the user: it is replaced, and never duplicated.
func TestColortermIsPinnedNotInherited(t *testing.T) {
	env := pty.WithWashEnv([]string{"COLORTERM=24bit", "TERM=screen", "PATH=/bin"})
	var seen []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "COLORTERM=") || strings.HasPrefix(kv, "TERM=") {
			seen = append(seen, kv)
		}
	}
	want := []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	if strings.Join(seen, " ") != strings.Join(want, " ") {
		t.Errorf("terminal vars = %q, want exactly %q", seen, want)
	}
}
