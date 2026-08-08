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

// session/set_config_option's params are {sessionId, configId, value} —
// VERIFIED against a real adapter rather than inferred, like set_mode
// before it. This is the mechanism behind the model picker, the
// reasoning-effort control and plan mode, so being wrong about it would
// be wrong about all three.
func TestSetConfigOptionAgainstRealAdapter(t *testing.T) {
	cmdline := os.Getenv("WASH_ACP_ADAPTER")
	if cmdline == "" {
		t.Skip("set WASH_ACP_ADAPTER to run the real-adapter config check")
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
	if len(res.ConfigOptions) == 0 {
		t.Skip("adapter exposes no config options")
	}
	for _, o := range res.ConfigOptions {
		t.Logf("config %s (%s) = %q, %d options", o.ID, o.Name, o.CurrentValue, len(o.Options))
	}

	// Pick something with an alternative and set it.
	for _, o := range res.ConfigOptions {
		for _, v := range o.Options {
			if v.Value == o.CurrentValue || v.Value == "" {
				continue
			}
			out, err := c.SetConfigOption(ctx, res.SessionID, o.ID, v.Value)
			if err != nil {
				t.Fatalf("set_config_option(%s=%s): %v — the params shape is wrong", o.ID, v.Value, err)
			}
			t.Logf("set %s=%s accepted; agent returned %d options", o.ID, v.Value, len(out.ConfigOptions))
			return
		}
	}
	t.Skip("no config option had an alternative value")
}
