package acp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// session/set_mode's params are {sessionId, modeId} — VERIFIED against
// codex-acp 1.1.9 rather than inferred from the field name in
// `currentModeId`. Run alongside the other conformance check whenever an
// adapter is installed or upgraded:
//
//	WASH_ACP_ADAPTER='codex-acp' go test ./internal/acp/ -run SetMode -v
func TestSetModeAgainstRealAdapter(t *testing.T) {
	cmdline := os.Getenv("WASH_ACP_ADAPTER")
	if cmdline == "" {
		t.Skip("set WASH_ACP_ADAPTER to run the real-adapter set_mode check")
	}
	f := strings.Fields(cmdline)
	cmd := exec.Command(f[0], f[1:]...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	c := NewClient(stdout, stdin, &conformanceHandler{t: t})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := c.Initialize(ctx, ClientCapabilities{}, Implementation{Name: "wash"}); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	res, err := c.NewSession(ctx, cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("modes=%+v", res.Modes)
	if len(res.Modes.AvailableModes) < 2 {
		t.Skip("adapter offers no modes to switch between")
	}
	target := ""
	for _, m := range res.Modes.AvailableModes {
		if m.ID != res.Modes.CurrentModeID {
			target = m.ID
			break
		}
	}
	if err := c.SetMode(ctx, res.SessionID, target); err != nil {
		t.Fatalf("set_mode(%q): %v — the params shape is wrong", target, err)
	}
	t.Logf("set_mode to %q accepted", target)
}
