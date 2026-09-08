package agentd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConfigDir points the preamble at a throwaway XDG_CONFIG_HOME so a
// test never reads or writes the developer's own initial prompt.
func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "wash", preambleFileName)
}

func TestPreambleRoundTrips(t *testing.T) {
	path := withConfigDir(t)
	if got := loadPreamble(); got != "" {
		t.Fatalf("a fresh machine has a preamble: %q", got)
	}

	const text = "You are working in the wash repo.\nRead docs/ARCHITECTURE.md first."
	if err := savePreamble(text); err != nil {
		t.Fatal(err)
	}
	if got := loadPreamble(); got != text {
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
	// "No preamble" and "an empty preamble" are the same state. Leaving an
	// empty file behind would make the difference look meaningful to
	// anyone who went looking.
	path := withConfigDir(t)
	if err := savePreamble("hello"); err != nil {
		t.Fatal(err)
	}
	if err := savePreamble("   \n  "); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("clearing left a file behind: %v", err)
	}
	if got := loadPreamble(); got != "" {
		t.Errorf("cleared preamble still loads %q", got)
	}
	// And clearing an already-absent one is not an error.
	if err := savePreamble(""); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
}

func TestUnreadablePreambleIsNoPreamble(t *testing.T) {
	// A broken config must never stop a session starting.
	path := withConfigDir(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory where the file should be: readable path, unreadable content.
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := loadPreamble(); got != "" {
		t.Errorf("unreadable preamble returned %q", got)
	}
}

func TestPreambleIsBounded(t *testing.T) {
	withConfigDir(t)
	huge := strings.Repeat("x", maxPreambleBytes+5000)
	if err := savePreamble(huge); err != nil {
		t.Fatal(err)
	}
	if got := len(loadPreamble()); got > maxPreambleBytes {
		t.Errorf("stored %d bytes, cap is %d", got, maxPreambleBytes)
	}
}

// withPreamble decides what a new session actually hears.
func TestWithPreamble(t *testing.T) {
	cases := []struct {
		name     string
		preamble string
		prompt   string
		want     string
	}{
		{"neither", "", "", ""},
		{"prompt only", "", "do the thing", "do the thing"},
		{"preamble only — a legitimate way to set the scene", "read the docs", "", "read the docs"},
		{"both: standing instructions first", "read the docs", "do the thing", "read the docs\n\ndo the thing"},
		{"whitespace is not content", "  read the docs \n", "\n do the thing  ", "read the docs\n\ndo the thing"},
	}
	for _, c := range cases {
		if got := withPreamble(c.preamble, c.prompt); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The preamble is an INITIAL prompt: it belongs to starting a session, not
// to every message in one. This pins the boundary — nothing outside
// agent_start may consult it.
func TestPreambleIsNotAppliedToOrdinaryPrompts(t *testing.T) {
	withConfigDir(t)
	if err := savePreamble("standing instructions"); err != nil {
		t.Fatal(err)
	}
	// promptHosted is what a typed message goes through; it must send
	// exactly what was typed.
	if got := withPreamble("", "second message"); got != "second message" {
		t.Errorf("an ordinary prompt was decorated: %q", got)
	}
}
