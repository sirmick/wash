package agentd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConfigDir points the default prompt at a throwaway XDG_CONFIG_HOME so a
// test never reads or writes the developer's own default prompt.
func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "wash", defaultPromptFileName)
}

func TestDefaultPromptRoundTrips(t *testing.T) {
	path := withConfigDir(t)
	if got := loadDefaultPrompt(); got != "" {
		t.Fatalf("a fresh machine has a default prompt: %q", got)
	}

	const text = "You are working in the wash repo.\nRead docs/ARCHITECTURE.md first."
	if err := saveDefaultPrompt(text); err != nil {
		t.Fatal(err)
	}
	if got := loadDefaultPrompt(); got != text {
		t.Errorf("round trip = %q, want %q", got, text)
	}

	// A file a human can open and edit, not an escaped JSON string.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("not stored where it says: %v", err)
	}
	if !strings.Contains(string(raw), "Read docs/ARCHITECTURE.md first.") {
		t.Errorf("file is not the plain text: %q", raw)
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestSavingNothingRemovesTheFile(t *testing.T) {
	// "No default prompt" and "an empty default prompt" are the same state. Leaving an
	// empty file behind would make the difference look meaningful to
	// anyone who went looking.
	path := withConfigDir(t)
	if err := saveDefaultPrompt("hello"); err != nil {
		t.Fatal(err)
	}
	if err := saveDefaultPrompt("   \n  "); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("clearing left a file behind: %v", err)
	}
	if got := loadDefaultPrompt(); got != "" {
		t.Errorf("cleared default prompt still loads %q", got)
	}
	// And clearing an already-absent one is not an error.
	if err := saveDefaultPrompt(""); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
}

func TestUnreadableDefaultPromptIsNone(t *testing.T) {
	// A broken config must never stop a session starting.
	path := withConfigDir(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory where the file should be: readable path, unreadable content.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := loadDefaultPrompt(); got != "" {
		t.Errorf("unreadable default prompt returned %q", got)
	}
}

func TestDefaultPromptIsBounded(t *testing.T) {
	withConfigDir(t)
	huge := strings.Repeat("x", maxDefaultPromptBytes+5000)
	if err := saveDefaultPrompt(huge); err != nil {
		t.Fatal(err)
	}
	if got := len(loadDefaultPrompt()); got > maxDefaultPromptBytes {
		t.Errorf("stored %d bytes, cap is %d", got, maxDefaultPromptBytes)
	}
}

// withDefaultPrompt decides what a new session actually hears.
func TestWithDefaultPrompt(t *testing.T) {
	cases := []struct {
		name   string
		preset string
		prompt string
		want   string
	}{
		{"neither", "", "", ""},
		{"prompt only", "", "do the thing", "do the thing"},
		{"default prompt only — a legitimate way to set the scene", "read the docs", "", "read the docs"},
		{"both: standing instructions first", "read the docs", "do the thing", "read the docs\n\ndo the thing"},
		{"whitespace is not content", "  read the docs \n", "\n do the thing  ", "read the docs\n\ndo the thing"},
	}
	for _, c := range cases {
		if got := withDefaultPrompt(c.preset, c.prompt); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The default prompt is an INITIAL prompt: it belongs to starting a session, not
// to every message in one. This pins the boundary — nothing outside
// agent_start may consult it.
func TestDefaultPromptIsNotAppliedToOrdinaryPrompts(t *testing.T) {
	withConfigDir(t)
	if err := saveDefaultPrompt("standing instructions"); err != nil {
		t.Fatal(err)
	}
	// promptHosted is what a typed message goes through; it must send
	// exactly what was typed.
	if got := withDefaultPrompt("", "second message"); got != "second message" {
		t.Errorf("an ordinary prompt was decorated: %q", got)
	}
}

// The file is the truth. A prompt written or removed outside the dialog —
// an editor, a config-management tool, a shell redirect — must reach the
// launcher's "a default prompt will be sent" line without restarting
// agentd, which is what refreshDefaultPrompt gives the sweep.
func TestHandEditedDefaultPromptReachesTheFlag(t *testing.T) {
	path := withConfigDir(t)
	var s State

	if refreshDefaultPrompt(&s) {
		t.Error("no file, but the flag moved")
	}
	if s.HasDefaultPrompt {
		t.Error("no file, but a default prompt is advertised")
	}

	// Written behind agentd's back, as a person with an editor would.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Be terse.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !refreshDefaultPrompt(&s) || !s.HasDefaultPrompt {
		t.Error("a hand-written default prompt never reached the flag")
	}
	// Settled: an unchanged file must not republish on every sweep.
	if refreshDefaultPrompt(&s) {
		t.Error("an unchanged file keeps reporting a change")
	}

	// Deleted the same way.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !refreshDefaultPrompt(&s) || s.HasDefaultPrompt {
		t.Error("a deleted default prompt is still advertised")
	}
}

// A file holding only whitespace is not a default prompt: loadDefaultPrompt
// trims it to "" and nothing is sent, so the flag must not claim otherwise.
// This is why the flag re-reads rather than stats for a non-empty file.
func TestWhitespaceOnlyDefaultPromptIsNotAdvertised(t *testing.T) {
	path := withConfigDir(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\n\t \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var s State
	if refreshDefaultPrompt(&s) || s.HasDefaultPrompt {
		t.Error("whitespace is advertised as a default prompt")
	}
}
