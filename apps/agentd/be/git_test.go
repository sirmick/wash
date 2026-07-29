package agentd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// gitRepo makes a throwaway repo with one commit on a known branch.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		// Keep the developer's global config (and hooks, and signing) out
		// of it — this must behave the same on every box.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=trunk")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-m", "one")
	return dir
}

func TestLookupGit(t *testing.T) {
	dir := gitRepo(t)
	info := lookupGit(dir)
	if info.branch != "trunk" {
		t.Errorf("branch = %q, want trunk", info.branch)
	}
	if info.dirty {
		t.Error("a fresh checkout reported dirty")
	}

	// A modified tracked file makes it dirty — the star the sidebar shows.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info := lookupGit(dir); !info.dirty {
		t.Error("a modified file did not report dirty")
	}
}

// Everything that isn't a repo degrades to "no branch", never an error in
// the sidebar: the roster is useful for agents outside checkouts too.
func TestLookupGitNonRepo(t *testing.T) {
	info := lookupGit(t.TempDir())
	if info.branch != "" || info.dirty {
		t.Errorf("non-repo → %+v, want empty", info)
	}
	info = lookupGit(filepath.Join(t.TempDir(), "does-not-exist"))
	if info.branch != "" {
		t.Errorf("missing dir → %+v, want empty", info)
	}
	if info.at.IsZero() {
		t.Error("failed lookups must still stamp the cache, or we re-shell git forever")
	}
}

// The cache is what makes this safe to call on every roster update.
func TestResolveGitCaches(t *testing.T) {
	reset()
	svc = nil // applyGit must tolerate a service that isn't up yet
	dir := gitRepo(t)

	gitMu.Lock()
	gitCache = map[string]gitInfo{}
	gitInFlight = map[string]bool{}
	gitMu.Unlock()

	resolveGit(dir)
	gitMu.Lock()
	first, ok := gitCache[dir]
	gitMu.Unlock()
	if !ok || first.branch != "trunk" {
		t.Fatalf("cache after first lookup = %+v", first)
	}

	// A second call inside the TTL must not re-run git: prove it by
	// poisoning the cache entry and checking it survives.
	gitMu.Lock()
	gitCache[dir] = gitInfo{branch: "sentinel", at: time.Now()}
	gitMu.Unlock()
	resolveGit(dir)
	gitMu.Lock()
	second := gitCache[dir]
	gitMu.Unlock()
	if second.branch != "sentinel" {
		t.Errorf("cache re-resolved inside the TTL: %+v", second)
	}

	// Past the TTL it refreshes.
	gitMu.Lock()
	gitCache[dir] = gitInfo{branch: "sentinel", at: time.Now().Add(-2 * gitCacheTTL)}
	gitMu.Unlock()
	resolveGit(dir)
	gitMu.Lock()
	third := gitCache[dir]
	gitMu.Unlock()
	if third.branch != "trunk" {
		t.Errorf("cache did not refresh past the TTL: %+v", third)
	}
}
