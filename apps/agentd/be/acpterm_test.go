package agentd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/acp"
)

// The pieces that decide correctness without needing a live conn: the cwd
// wrapper (a quoting bug here runs the wrong command), the env merge, and
// the confinement. The full create→output→wait path needs a router and is
// covered end to end by the agent e2e.

func TestTerminalCwdWrapperQuotes(t *testing.T) {
	got := terminalCwdWrapper([]string{"echo", "hello world"}, "/tmp/a dir")
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("wrapper shape = %q, want sh -c …", got)
	}
	// Both the directory and the arguments carry spaces; neither may
	// split. `exec` matters too: without it the wrapper leaves a shell
	// between us and the child, so signals and exit codes come from the
	// wrong process.
	if !strings.Contains(got[2], `cd '/tmp/a dir' && exec 'echo' 'hello world'`) {
		t.Errorf("wrapper = %q, want a quoted cd + exec", got[2])
	}

	// A single quote in a path is the case naive quoting gets wrong.
	got2 := terminalCwdWrapper([]string{"ls"}, "/tmp/it's here")
	if !strings.Contains(got2[2], `'/tmp/it'\''s here'`) {
		t.Errorf("quoting of an apostrophe: %q", got2[2])
	}

	// No cwd → untouched, so the common case pays nothing.
	plain := terminalCwdWrapper([]string{"ls", "-l"}, "")
	if len(plain) != 2 || plain[0] != "ls" {
		t.Errorf("empty cwd should pass argv through, got %q", plain)
	}
}

func TestWithEnvLetsTheAgentOverrideButPinsPWD(t *testing.T) {
	inherited := []string{"PATH=/usr/bin", "EDITOR=vi", "PWD=/somewhere/else"}
	out := withEnv(inherited, []acp.EnvVar{{Name: "EDITOR", Value: "nano"}}, "/work")

	seen := map[string]string{}
	for _, kv := range out {
		if i := strings.IndexByte(kv, '='); i > 0 {
			seen[kv[:i]] = kv[i+1:]
		}
	}
	if seen["EDITOR"] != "nano" {
		t.Errorf("EDITOR = %q, want the agent's value", seen["EDITOR"])
	}
	if seen["PATH"] != "/usr/bin" {
		t.Errorf("PATH = %q, want the inherited value", seen["PATH"])
	}
	// PWD follows the resolved cwd, never the inherited one.
	if seen["PWD"] != "/work" {
		t.Errorf("PWD = %q, want /work", seen["PWD"])
	}
	// And exactly one of each — a duplicate EDITOR would leave the
	// child's behaviour up to which one its libc reads first.
	count := 0
	for _, kv := range out {
		if strings.HasPrefix(kv, "EDITOR=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("EDITOR appears %d times, want 1", count)
	}
}

func TestCreateTerminalRefusesACwdOutsideTheSession(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	h := &hosted{key: "acp:1", cwd: root}

	// No conn, so a permitted path would fail later at pty.Open — but the
	// refusal must happen BEFORE that, on the path check.
	_, err := h.CreateTerminal(t.Context(), acp.CreateTerminalRequest{
		Command: "echo", Args: []string{"hi"}, Cwd: outside,
	})
	if err == nil {
		t.Fatal("a cwd outside the session root was accepted")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("error = %v, want the confinement error", err)
	}

	// An empty command is refused too, before anything is spawned.
	if _, err := h.CreateTerminal(t.Context(), acp.CreateTerminalRequest{}); err == nil {
		t.Error("an empty command was accepted")
	}
	_ = os.WriteFile(filepath.Join(root, "x"), nil, 0o644)
}

func TestLookupTerminalTreatsReleasedAndUnknownAlike(t *testing.T) {
	termMu.Lock()
	termAll = map[string]*terminal{}
	termMu.Unlock()

	if _, err := lookupTerminal(acp.TerminalRef{TerminalID: "7"}); err == nil {
		t.Error("unknown terminal id did not error")
	}
	termMu.Lock()
	termAll["7"] = &terminal{id: "7"}
	termMu.Unlock()
	if _, err := lookupTerminal(acp.TerminalRef{TerminalID: "7"}); err != nil {
		t.Errorf("known terminal errored: %v", err)
	}
}
