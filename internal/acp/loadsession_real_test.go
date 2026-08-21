package acp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Does session/load actually answer with a settings block?
//
// This repo writes "Observed on codex-acp 1.1.9" rather than assuming a
// shape, and HistoryPanel refuses to ship a Fork button precisely because
// session/fork's shape was never verified against a real adapter. Same
// standard here: LoadSessionResponse mirrors NewSessionResponse because
// that is the obvious symmetry, which is a hypothesis, not a fact.
//
// Run whenever an adapter is installed or upgraded:
//
//	WASH_ACP_ADAPTER='claude-agent-acp' go test ./internal/acp/ -run LoadSessionAgainstReal -v
//
// A zero response is NOT a failure — it is the answer "this adapter
// returns nothing here", which is worth recording. The failure this test
// exists to catch is a response that carries settings we are dropping.
func TestLoadSessionAgainstRealAdapter(t *testing.T) {
	cmdline := os.Getenv("WASH_ACP_ADAPTER")
	if cmdline == "" {
		t.Skip("set WASH_ACP_ADAPTER to run the real-adapter session/load check")
	}
	f := strings.Fields(cmdline)
	cmd := exec.Command(f[0], f[1:]...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	c := NewClient(stdout, stdin, &conformanceHandler{t: t})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	init, err := c.Initialize(ctx, ClientCapabilities{}, Implementation{Name: "wash"})
	if err != nil {
		t.Fatal(err)
	}
	if !init.AgentCapabilities.LoadSession {
		t.Skipf("%s does not advertise loadSession", init.AgentInfo.Name)
	}

	cwd, _ := os.Getwd()
	created, err := c.NewSession(ctx, cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("session/new  → modes=%d current=%q configs=%d models=%d",
		len(created.Modes.AvailableModes), created.Modes.CurrentModeID,
		len(created.ConfigOptions), len(created.Models.AvailableModels))

	loaded, err := c.LoadSession(ctx, created.SessionID, cwd, nil)
	if err != nil {
		t.Fatalf("session/load: %v", err)
	}
	t.Logf("session/load → modes=%d current=%q configs=%d models=%d",
		len(loaded.Modes.AvailableModes), loaded.Modes.CurrentModeID,
		len(loaded.ConfigOptions), len(loaded.Models.AvailableModels))

	// The point of the change: what new gives, load should give back.
	// Reported rather than asserted — an adapter that answers bare is a
	// fact about that adapter, and the fallback (re-requesting configs
	// after load) is a different piece of work than this one.
	switch {
	case len(loaded.Modes.AvailableModes) > 0 || len(loaded.ConfigOptions) > 0:
		t.Logf("VERIFIED: %s returns a settings block on session/load", init.AgentInfo.Name)
	case len(created.Modes.AvailableModes) > 0 || len(created.ConfigOptions) > 0:
		t.Logf("NOTE: %s offers settings on session/new but returns none on session/load — "+
			"a resumed session on this adapter needs the re-request fallback, not this decode",
			init.AgentInfo.Name)
	default:
		t.Logf("NOTE: %s offers no settings on either path; nothing to carry", init.AgentInfo.Name)
	}
}
