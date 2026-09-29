package edit

import "testing"

// An editor opened by an Agent window says which conversation it belongs to.
// A taskbar of windows all called "Editor" cannot be told apart, and the
// editor is one window per Agent window.
func TestTitleForSession(t *testing.T) {
	if got := titleForSession("Fix the reconnect banner race"); got != "Editor · Fix the reconnect banner race" {
		t.Fatalf("title = %q", got)
	}
	// An editor the user launched, or one whose session has no name yet,
	// keeps the plain manifest title rather than a dangling separator.
	if got := titleForSession(""); got != "Editor" {
		t.Fatalf("untitled = %q", got)
	}
}
