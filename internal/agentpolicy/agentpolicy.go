// Package agentpolicy owns the on-disk shape of the coding-agent approval
// policy (docs/AGENT_TERM.md §6) and the rule text that describes one
// permission.
//
// It exists because the file has three parties now and they must agree:
// the Agents settings pane writes it, every wash-term reads it to answer a
// request, and com.wash.agentd appends to it when a human clicks "always
// allow" (§12). The MATCHER — what a rule means — deliberately stays in
// wash-term with its tests; this package is the schema, the file I/O, and
// the rule-suggestion text, i.e. exactly what more than one process needs.
package agentpolicy

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Decisions. "ask" is also the universal fallback: everything unknown,
// malformed or unmatched resolves to it, never to allow.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
	DecisionAsk   = "ask"
)

// Domain is the settings domain (and therefore the file basename) the
// Agents pane writes through the settings host.
const Domain = "agents"

// Policy is ~/.config/wash/agents.json.
type Policy struct {
	// Enabled is the kill switch. False — the zero value, and the state
	// of a box that has never opened the Agents pane — means wash answers
	// "ask" to everything.
	Enabled bool `json:"enabled"`
	// Default is the decision for a request no rule matched. Anything
	// other than allow/deny reads as "ask", so a typo cannot open a door.
	Default string `json:"default,omitempty"`
	// Rules are evaluated in order; first match wins.
	Rules []Rule `json:"rules,omitempty"`
	// LegacyAutoApprove turns on the hookless "watch for (y/n) and type
	// y" path. Spoofable by construction, opt-in, and gated by Enabled.
	LegacyAutoApprove bool `json:"legacy_autoapprove,omitempty"`
	// AskDesktop lets an unmatched request ask the human through the
	// desktop before falling back to the agent's own prompt (§12). Only
	// consulted when Enabled. Defaults to true via AskDesktopOrDefault —
	// asking is the point of turning the policy on.
	AskDesktop *bool `json:"ask_desktop,omitempty"`
}

// Rule is one line of the table.
type Rule struct {
	// Match is `Tool` or `Tool(pattern)`.
	Match string `json:"match"`
	// Decision is allow | deny | ask.
	Decision string `json:"decision"`
	// Cwd scopes the rule to requests at or under this directory.
	Cwd string `json:"cwd,omitempty"`
}

// AskDesktopOrDefault reports whether unmatched requests should ask the
// desktop. Absent means yes: a user who enabled the policy wants wash to
// participate, and the ask is the safe half of participating (it can only
// ever produce a prompt, never an allow).
func (p *Policy) AskDesktopOrDefault() bool {
	if p == nil || !p.Enabled {
		return false
	}
	return p.AskDesktop == nil || *p.AskDesktop
}

// Path is ~/.config/wash/agents.json, XDG_CONFIG_HOME aware — the same
// layout apps/settings/be's domain files use.
func Path() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "wash", Domain+".json")
}

// Load reads a policy file. Any problem — missing, unreadable, malformed —
// yields the zero Policy, which is disabled, which answers "ask" to
// everything. A broken file must never half-apply.
func Load(path string) Policy {
	data, err := os.ReadFile(path)
	if err != nil {
		return Policy{}
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}
	}
	return p
}

// Append adds a rule to the policy file and saves it, creating the file if
// needed. Used by the "always allow" button (§12).
//
// Read-modify-write against the file rather than an in-memory copy: the
// Agents settings pane is the other writer, and re-reading immediately
// before the write keeps the losing window to the length of this call.
// Appends (not prepends) so a hand-written deny higher up the table keeps
// beating a click made later.
//
// A rule that is already present is a no-op, so double-clicking "always
// allow" doesn't grow the file.
func Append(path, match, decision string) error {
	if match == "" {
		return nil
	}
	p := Load(path)
	for _, r := range p.Rules {
		if r.Match == match && r.Decision == decision && r.Cwd == "" {
			return nil
		}
	}
	p.Rules = append(p.Rules, Rule{Match: match, Decision: decision})
	// Appending a rule implies the table is meant to be consulted.
	p.Enabled = true
	return Save(path, p)
}

// Save writes the policy atomically (temp file in the same directory, then
// rename), so a reader never sees a half-written table.
func Save(path string, p Policy) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agents-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// SuggestRule proposes the rule text an "always allow" button should name
// (and then write). Deliberately conservative: a rule clicked in a hurry
// should buy the least that still makes the click worth making.
//
//	Bash(git push origin main) → Bash(git push*)   two tokens, not one —
//	                                               Bash(git *) would also
//	                                               buy `git reset --hard`
//	Bash(ls -la)               → Bash(ls*)
//	Read /etc/hosts            → Read              read-only: the tool IS
//	                                               the risk surface
//	Edit /home/mick/wash/x.go  → Edit(/home/mick/wash/*)   writes get
//	                                               scoped to the agent's cwd
//
// Pure: the suggestion is a table in the tests, not a surprise on a button.
func SuggestRule(tool, subject, cwd string) string {
	switch tool {
	case "Bash", "BashOutput", "KillShell":
		fields := strings.Fields(subject)
		if len(fields) == 0 {
			return tool
		}
		head := fields[0]
		if len(fields) > 1 && !strings.HasPrefix(fields[1], "-") && isSubcommand(fields[1]) {
			head += " " + fields[1]
		}
		return tool + "(" + head + "*)"
	case "Read", "Glob", "Grep", "WebSearch", "Task":
		return tool
	case "Write", "Edit", "NotebookEdit", "MultiEdit":
		if cwd != "" {
			return tool + "(" + strings.TrimRight(cwd, "/") + "/*)"
		}
		return tool
	case "WebFetch":
		if host := urlHost(subject); host != "" {
			return tool + "(*" + host + "*)"
		}
		return tool
	}
	return tool
}

// isSubcommand rejects second tokens that are really arguments — a path, a
// URL, a quoted string. `git push` is a subcommand; `cat /etc/hosts` is not.
func isSubcommand(s string) bool {
	if s == "" || strings.ContainsAny(s, "/'\"$`|&;*") {
		return false
	}
	for _, r := range s {
		if !(r == '-' || r == '_' || r == ':' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// urlHost pulls the host out of a URL without importing net/url for one
// field, and without pretending to validate it.
func urlHost(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if s == "" || strings.ContainsAny(s, " *") {
		return ""
	}
	return path.Base(s)
}
