// Agent approval policy — this tier's half (docs/AGENT_TERM.md §6, M3).
//
// The matcher moved to internal/agentpolicy in M2 (docs/AGENT_APP.md §11)
// so that agentd's ACP host and this decide socket answer the same rule
// file identically. What remains here is the *loading*: the mtime-checked
// read of ~/.config/wash/agents.json, and thin aliases so this tier's call
// sites did not have to change on their way to deletion (§10).
//
// The property that survives the move unchanged: every failure — no file,
// bad JSON, no rule matched, an unparseable request — resolves to "ask",
// i.e. the agent's own prompt. This machinery never invents an "allow".
package term

import (
	"os"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/agentpolicy"
)

// Decisions wash can return. They are Claude Code's PreToolUse
// permissionDecision values; "ask" is also our universal fallback. Aliased
// from internal/agentpolicy, which owns the file schema now that agentd
// writes to it too (docs/AGENT_TERM.md §12).
const (
	DecisionAllow = agentpolicy.DecisionAllow
	DecisionDeny  = agentpolicy.DecisionDeny
	DecisionAsk   = agentpolicy.DecisionAsk
)

// agentPolicy / policyRule are the shared schema; the matcher below is
// this package's own.
type agentPolicy = agentpolicy.Policy
type policyRule = agentpolicy.Rule

// decideRequest is what the hook helper asks about (docs/AGENT_TERM.md §4).
// The matcher moved to internal/agentpolicy (M2) so agentd evaluates the
// same rules the same way. These aliases keep this tier's call sites
// unchanged until it is deleted (docs/AGENT_APP.md §10).
type decideRequest = agentpolicy.Request
type decideResponse = agentpolicy.Response

func evaluate(p agentPolicy, req decideRequest) decideResponse { return agentpolicy.Evaluate(p, req) }

func toolSubject(tool string, input map[string]any) string {
	return agentpolicy.ToolSubject(tool, input)
}

// ---- loading ----

// agentPolicyFile is where the Agents settings pane persists the policy.
// It is a settings *domain* (apps/settings/be domainFile), so the pane
// writes it through the same atomic path as every other wash config.
const agentPolicyDomain = agentpolicy.Domain

// policyCache re-reads the policy file when its mtime moves. Decisions are
// rare — a handful per turn — so the file is stat'ed on EVERY decision and
// re-parsed only when it actually changed.
//
// There was a 500ms "don't stat too often" window here. It was wrong: a
// human who clicks "always allow" (§12) makes the very next tool call test
// the rule they just created, and agentd writes it milliseconds earlier.
// One stat per permission request is not a cost worth a stale answer.
type policyCache struct {
	mu      sync.Mutex
	loaded  agentPolicy
	modTime time.Time
	size    int64
	path    string
}

var policyStore policyCache

// current returns the policy in force, reloading if the file changed.
// Any error (missing file, bad JSON) yields the zero policy — disabled,
// which answers "ask" to everything.
func (c *policyCache) current(now time.Time) agentPolicy {
	c.mu.Lock()
	defer c.mu.Unlock()
	path := c.path
	if path == "" {
		path = agentPolicyPath()
	}
	if path == "" {
		c.loaded = agentPolicy{}
		return c.loaded
	}
	fi, err := os.Stat(path)
	if err != nil {
		c.loaded, c.modTime, c.size = agentPolicy{}, time.Time{}, 0
		return c.loaded
	}
	if fi.ModTime().Equal(c.modTime) && fi.Size() == c.size {
		return c.loaded
	}
	c.modTime, c.size = fi.ModTime(), fi.Size()
	c.loaded = readPolicyFile(path)
	return c.loaded
}

// readPolicyFile decodes the policy, degrading to "disabled" on any
// problem rather than half-applying a broken table.
func readPolicyFile(path string) agentPolicy { return agentpolicy.Load(path) }

// agentPolicyPath is ~/.config/wash/agents.json (XDG_CONFIG_HOME aware).
func agentPolicyPath() string { return agentpolicy.Path() }
