package term

import (
	"os"
	"path/filepath"
	"testing"
)

// resolveOpenDir is the whole --open policy: an existing directory is
// taken (absolute), anything else is ignored rather than fatal.
func TestResolveOpenDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{dir, dir},
		{file, ""},                          // a file is not a cwd
		{filepath.Join(dir, "missing"), ""}, // must exist
	}
	for _, c := range cases {
		if got := resolveOpenDir(c.in); got != c.want {
			t.Errorf("resolveOpenDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Relative paths come back absolute.
	wd, _ := os.Getwd()
	if got := resolveOpenDir("."); got != wd {
		t.Errorf("resolveOpenDir(.) = %q, want %q", got, wd)
	}
}

// cwdOf prefers what the shell reported (OSC 7) when that directory still
// exists, and ignores a report for a directory that has gone — a deleted
// build dir must not stop the next tab from opening.
func TestCwdOfPrefersLiveReport(t *testing.T) {
	initState()
	dir := t.TempDir()
	st.cwd[7] = dir
	if got := cwdOf(7); got != dir {
		t.Fatalf("cwdOf(reported) = %q, want %q", got, dir)
	}
	st.cwd[7] = filepath.Join(dir, "gone")
	// No session behind the id either: nothing to fall back to.
	if got := cwdOf(7); got != "" {
		t.Fatalf("cwdOf(stale report, no session) = %q, want empty", got)
	}
	if got := cwdOf(99); got != "" {
		t.Fatalf("cwdOf(unknown) = %q, want empty", got)
	}
}
