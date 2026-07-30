// Git context for roster rows (docs/AGENT_TERM.md §7): "claude · wash ·
// main" tells you which agent is in which checkout, which is the question
// a roster answers that a tab chip can't.
//
// The lookup lives HERE, in the service, and never in the agent's hooks:
// a hook runs inside someone's turn and must stay under 100ms, while this
// can be slow, cached, and best-effort. It shells git rather than reading
// .git by hand because worktrees, submodules and detached HEADs are git's
// problem, not ours.
package agentd

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// gitTimeout bounds one lookup. A repo on a stalled network mount must
// not wedge the sweep or the goroutine pool.
const gitTimeout = 2 * time.Second

type gitInfo struct {
	branch string
	dirty  bool
	at     time.Time
}

var (
	gitMu    sync.Mutex
	gitCache = map[string]gitInfo{}
	// gitInFlight collapses concurrent lookups for the same directory —
	// several tabs in one repo report at once.
	gitInFlight = map[string]bool{}
)

// resolveGit fills a directory's branch/dirty state and pushes the result
// into any rows using it. Runs on its own goroutine; returns immediately
// if the cache is warm or another lookup for this directory is in flight.
func resolveGit(cwd string) {
	gitMu.Lock()
	if info, ok := gitCache[cwd]; ok && time.Since(info.at) < gitCacheTTL {
		gitMu.Unlock()
		applyGit(cwd, info)
		return
	}
	if gitInFlight[cwd] {
		gitMu.Unlock()
		return
	}
	gitInFlight[cwd] = true
	gitMu.Unlock()

	info := lookupGit(cwd)

	gitMu.Lock()
	gitCache[cwd] = info
	delete(gitInFlight, cwd)
	// Bound the cache: directories come and go with terminals, and this
	// process is long-lived.
	if len(gitCache) > 256 {
		for k, v := range gitCache {
			if time.Since(v.at) > gitCacheTTL {
				delete(gitCache, k)
			}
		}
	}
	gitMu.Unlock()
	applyGit(cwd, info)
}

// lookupGit runs the two git commands. Anything unexpected — not a repo,
// no git installed, a timeout — yields the zero value, which renders as
// "no branch shown" rather than an error in the sidebar.
func lookupGit(cwd string) gitInfo {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return gitInfo{at: time.Now()}
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		// Detached: show the short sha instead of a useless "HEAD".
		if sha, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--short", "HEAD").Output(); err == nil {
			branch = strings.TrimSpace(string(sha))
		}
	}
	info := gitInfo{branch: branch, at: time.Now()}
	// --porcelain is emptiness-as-cleanliness; we only need the bit, so
	// cap the read implicitly by asking for the summary form.
	if st, err := exec.CommandContext(ctx, "git", "-C", cwd, "status", "--porcelain", "--untracked-files=no").Output(); err == nil {
		info.dirty = len(strings.TrimSpace(string(st))) > 0
	}
	return info
}

// applyGit writes a resolved lookup into every row in that directory and
// republishes. A no-op when nothing changed, so a cache hit doesn't churn
// the roster.
func applyGit(cwd string, info gitInfo) {
	if svc == nil {
		return
	}
	now := time.Now()
	svc.Mutate(func(s *State) {
		changed := false
		for _, r := range rows {
			if r.Cwd != cwd {
				continue
			}
			if r.Branch != info.branch || r.Dirty != info.dirty {
				r.Branch, r.Dirty = info.branch, info.dirty
				changed = true
			}
		}
		if !changed {
			return
		}
		s.Rows = publish(now)
	})
}
