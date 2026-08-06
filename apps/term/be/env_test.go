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
	var path, term string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
		if v, ok := strings.CutPrefix(kv, "TERM="); ok {
			term = v
		}
	}
	if !strings.HasPrefix(path, "/opt/wash/bin:") {
		t.Errorf("PATH = %q — wash's own bin dir must come first, or a terminal inside wash N gets wash N-1's tools", path)
	}
	if term != "xterm-256color" {
		t.Errorf("TERM = %q, want xterm-256color", term)
	}
}
