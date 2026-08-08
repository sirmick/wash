package agentd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/acp"
)

func intp(n int) *int { return &n }

// The agent is not trusted to stay in the folder it was given; it is held
// there. Every one of these paths is a plausible thing an agent asks for.
func TestAgentFsRefusesOutsideTheSessionCwd(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &hosted{key: "acp:test", cwd: root}

	for _, p := range []string{
		outside,
		filepath.Join(root, "..", filepath.Base(filepath.Dir(outside)), "secret.txt"),
		"/etc/passwd",
		filepath.Join(root, "..", "..", "etc", "passwd"),
	} {
		if _, err := h.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: p}); err == nil {
			t.Errorf("read %q succeeded — the sandbox let the agent out", p)
		}
		if err := h.WriteTextFile(context.Background(), acp.WriteTextFileRequest{Path: p, Content: "x"}); err == nil {
			t.Errorf("write %q succeeded — the sandbox let the agent out", p)
		}
	}
	// The file outside must be untouched by the attempted writes.
	if b, err := os.ReadFile(outside); err != nil || string(b) != "private\n" {
		t.Errorf("file outside the root was modified: %q, %v", b, err)
	}
}

func TestAgentFsRoundTripsInsideTheRoot(t *testing.T) {
	root := t.TempDir()
	h := &hosted{key: "acp:test", cwd: root}

	if err := h.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		Path: filepath.Join(root, "notes.md"), Content: "# hello\n",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Written through internal/fs, so it lands where the desktop watches.
	if b, err := os.ReadFile(filepath.Join(root, "notes.md")); err != nil || string(b) != "# hello\n" {
		t.Fatalf("on disk: %q, %v", b, err)
	}
	res, err := h.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: filepath.Join(root, "notes.md")})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.Content != "# hello\n" {
		t.Errorf("read back %q, want %q", res.Content, "# hello\n")
	}
}

func TestAgentFsRejectsDirectoriesAndOversizedReads(t *testing.T) {
	root := t.TempDir()
	h := &hosted{key: "acp:test", cwd: root}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: filepath.Join(root, "sub")}); err == nil {
		t.Error("reading a directory succeeded")
	}
	big := filepath.Join(root, "big.log")
	if err := os.WriteFile(big, make([]byte, maxAgentReadBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	// An agent asking for a huge log gets an error, not the desktop's memory.
	if _, err := h.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: big}); err == nil {
		t.Error("oversized read succeeded")
	}
	if err := h.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		Path: filepath.Join(root, "x"), Content: strings.Repeat("a", maxAgentWriteBytes+1),
	}); err == nil {
		t.Error("oversized write succeeded")
	}
}

// ACP's line window: 1-based, inclusive, and a probe past the end returns
// what exists rather than erroring.
func TestSliceLines(t *testing.T) {
	const text = "one\ntwo\nthree\nfour\nfive"
	for _, tc := range []struct {
		name  string
		line  *int
		limit *int
		want  string
	}{
		{"whole file when unset", nil, nil, text},
		{"from line 3", intp(3), nil, "three\nfour\nfive"},
		{"first two", nil, intp(2), "one\ntwo"},
		{"window", intp(2), intp(2), "two\nthree"},
		{"line 1 is the start", intp(1), intp(1), "one"},
		{"limit past the end clamps", intp(4), intp(99), "four\nfive"},
		{"start past the end is empty", intp(99), nil, ""},
	} {
		if got := sliceLines(text, tc.line, tc.limit); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
