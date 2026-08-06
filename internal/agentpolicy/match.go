// The rule matcher (docs/AGENT_APP.md §11, M2).
//
// Moved out of apps/term/be so that both tiers — the terminal's decide
// socket and agentd's ACP host — evaluate the same rule file the same
// way. What a rule MEANS belongs with the file's schema; only the
// transport around it is per-tier.
//
// Three properties, in order of importance:
//
//  1. **Off by default.** No policy file, or `enabled:false`, answers
//     "ask" to everything — the agent behaves exactly as if wash weren't
//     there. It can only start answering on purpose.
//  2. **Fail open to the human.** Every failure — bad JSON, no rule
//     matched, an unparseable request — resolves to "ask". This
//     machinery must never invent an "allow".
//  3. **Pure.** Evaluate is a function of (policy, request); nothing in
//     it touches the filesystem, the clock, or a pty. The tests ARE the
//     specification of the matcher.
//
// The rule syntax mirrors Claude Code's own permission rules so a user
// can copy lines across: `Read`, `Bash(git status:*)`, `Edit(/etc/**)`.
package agentpolicy

import (
	"path/filepath"
	"strings"
)

type Request struct {
	SessionID      string         `json:"session_id"`
	ToolName       string         `json:"tool_name"`
	ToolInput      map[string]any `json:"tool_input"`
	Cwd            string         `json:"cwd"`
	PermissionMode string         `json:"permission_mode"`
}

// Response is wash's answer. Rule is the matched rule's text, for
// the audit log and the helper's permissionDecisionReason.
type Response struct {
	Decision string `json:"decision"`
	Rule     string `json:"rule,omitempty"`
}

// Evaluate answers one request. Returns the decision and the rule that
// produced it ("default", "policy off", or the rule text) — the caller
// logs both, so a surprising answer is always traceable to a line.
//
// Never returns anything but allow/deny/ask, and only returns allow when
// an enabled policy said so explicitly.
func Evaluate(p Policy, req Request) Response {
	if !p.Enabled {
		return Response{Decision: DecisionAsk, Rule: "policy off"}
	}
	if req.ToolName == "" {
		return Response{Decision: DecisionAsk, Rule: "no tool"}
	}
	subject := ToolSubject(req.ToolName, req.ToolInput)
	for _, r := range p.Rules {
		if !scopeMatches(r, req.Cwd) {
			continue
		}
		if !ruleMatches(r, req.ToolName, subject) {
			continue
		}
		return Response{Decision: normalizeDecision(r.Decision), Rule: r.Match}
	}
	return Response{Decision: normalizeDecision(p.Default), Rule: "default"}
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
func scopeMatches(r Rule, cwd string) bool {
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
func ruleMatches(r Rule, tool, subject string) bool {
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
func ToolSubject(tool string, input map[string]any) string {
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
