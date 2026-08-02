// Agent approval policy (docs/AGENT_TERM.md §6, M3).
//
// When an agent is about to use a tool, its PreToolUse hook asks wash
// what to do. This file is the answer: an ordered rule table, evaluated
// first-match-wins, loaded from the desktop-wide policy file the Agents
// settings pane writes (~/.config/wash/agents.json).
//
// Three properties the design is built around, in order of importance:
//
//  1. **Off by default.** No policy file, or `enabled:false`, means wash
//     answers "ask" to everything — i.e. the agent's own prompt appears,
//     exactly as if wash weren't there. The feature can only ever start
//     answering on purpose.
//  2. **Fail open to the human.** Every failure — no file, bad JSON, no
//     rule matched, an unparseable request — resolves to "ask". The one
//     thing this machinery must never do is invent an "allow".
//  3. **Pure and testable.** Evaluation is a function of (policy,
//     request); nothing in it touches the filesystem, the clock, or a
//     pty. The tests below ARE the specification of the matcher.
//
// The rule syntax mirrors Claude Code's own permission rules so a user
// can copy lines across: `Read`, `Bash(git status:*)`, `Edit(/etc/**)`.
package term

import (
	"os"
	"path/filepath"
	"strings"
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
type decideRequest struct {
	SessionID      string         `json:"session_id"`
	ToolName       string         `json:"tool_name"`
	ToolInput      map[string]any `json:"tool_input"`
	Cwd            string         `json:"cwd"`
	PermissionMode string         `json:"permission_mode"`
}

// decideResponse is wash's answer. Rule is the matched rule's text, for
// the audit log and the helper's permissionDecisionReason.
type decideResponse struct {
	Decision string `json:"decision"`
	Rule     string `json:"rule,omitempty"`
}

// evaluate answers one request. Returns the decision and the rule that
// produced it ("default", "policy off", or the rule text) — the caller
// logs both, so a surprising answer is always traceable to a line.
//
// Never returns anything but allow/deny/ask, and only returns allow when
// an enabled policy said so explicitly.
func evaluate(p agentPolicy, req decideRequest) decideResponse {
	if !p.Enabled {
		return decideResponse{Decision: DecisionAsk, Rule: "policy off"}
	}
	if req.ToolName == "" {
		return decideResponse{Decision: DecisionAsk, Rule: "no tool"}
	}
	subject := toolSubject(req.ToolName, req.ToolInput)
	for _, r := range p.Rules {
		if !scopeMatches(r, req.Cwd) {
			continue
		}
		if !ruleMatches(r, req.ToolName, subject) {
			continue
		}
		return decideResponse{Decision: normalizeDecision(r.Decision), Rule: r.Match}
	}
	return decideResponse{Decision: normalizeDecision(p.Default), Rule: "default"}
}

// normalizeDecision maps anything unrecognized to "ask" — a policy file
// with a typo'd decision must degrade to asking the human, never to
// allowing.
func normalizeDecision(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case DecisionAllow:
		return DecisionAllow
	case DecisionDeny:
		return DecisionDeny
	}
	return DecisionAsk
}

// scopeMatches implements the per-cwd scoping: a rule with Cwd set only
// applies to requests at or under that directory. Path comparison is
// lexical on cleaned paths (no symlink resolution — the agent's cwd and
// the user's rule are both plain strings, and resolving would make the
// answer depend on the filesystem's state at decide time).
func scopeMatches(r policyRule, cwd string) bool {
	if r.Cwd == "" {
		return true
	}
	if cwd == "" {
		return false
	}
	scope := filepath.Clean(r.Cwd)
	target := filepath.Clean(cwd)
	if target == scope {
		return true
	}
	return strings.HasPrefix(target, scope+string(filepath.Separator))
}

// matches tests one rule against a tool + its subject.
//
//	"Read"              → any Read
//	"Bash(git status*)" → Bash whose command globs
//	"Bash()"            → Bash with an empty subject only
func ruleMatches(r policyRule, tool, subject string) bool {
	name, pattern, hasPattern := splitMatch(r.Match)
	if !strings.EqualFold(name, tool) {
		return false
	}
	if !hasPattern {
		return true
	}
	return globMatch(pattern, subject)
}

// splitMatch parses `Tool` / `Tool(pattern)`. A missing closing paren is
// tolerated (the user is mid-typing in the settings pane) by treating
// the rest as the pattern.
func splitMatch(match string) (name, pattern string, hasPattern bool) {
	match = strings.TrimSpace(match)
	open := strings.Index(match, "(")
	if open < 0 {
		return match, "", false
	}
	name = strings.TrimSpace(match[:open])
	rest := match[open+1:]
	if close := strings.LastIndex(rest, ")"); close >= 0 {
		rest = rest[:close]
	}
	return name, rest, true
}

// toolSubject is the string a rule's pattern is matched against: the one
// field of the tool's input that says WHAT it is about to do. Unknown
// tools have no subject, so only a bare `Tool` rule can match them —
// a pattern rule can never accidentally allow a tool wash doesn't
// understand.
func toolSubject(tool string, input map[string]any) string {
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := input[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	switch tool {
	case "Bash", "BashOutput", "KillShell":
		return str("command", "shell_id")
	case "Read", "Write", "Edit", "NotebookEdit", "MultiEdit":
		return str("file_path", "notebook_path", "path")
	case "Glob", "Grep":
		return str("pattern", "path")
	case "WebFetch":
		return str("url")
	case "WebSearch":
		return str("query")
	case "Task":
		return str("subagent_type", "description")
	}
	return ""
}

// globMatch is a full-string glob: `*` matches any run of characters
// (including none, and including `/`), `?` matches exactly one. Nothing
// else is special — a rule is a literal otherwise.
//
// Claude Code writes its own rules as `Bash(git status:*)`, so a `:` that
// immediately precedes a trailing `*` is dropped, letting a line be
// copied across from ~/.claude/settings.json unchanged.
//
// Deliberately NOT regexp: rules come from a text box, and a regexp there
// is a footgun (an unanchored `.*` in an allow rule allows everything).
func globMatch(pattern, s string) bool {
	if strings.HasSuffix(pattern, ":*") {
		pattern = strings.TrimSuffix(pattern, ":*") + "*"
	}
	return globHere(pattern, s)
}

// globHere is the matcher proper: linear, backtracking on `*`, no
// allocation. p and s are consumed left to right; star/ss remember the
// most recent `*` so a failed match can retry it one character longer.
func globHere(p, s string) bool {
	var star = -1
	var ss int
	i, j := 0, 0
	for j < len(s) {
		switch {
		case i < len(p) && (p[i] == '?' || p[i] == s[j]):
			i++
			j++
		case i < len(p) && p[i] == '*':
			star = i
			ss = j
			i++
		case star >= 0:
			i = star + 1
			ss++
			j = ss
		default:
			return false
		}
	}
	for i < len(p) && p[i] == '*' {
		i++
	}
	return i == len(p)
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
