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
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
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
	// AskDesktop lets an unmatched request ask the human through the
	// desktop before falling back to the agent's own prompt (§12). Only
	// consulted when Enabled. Defaults to true via AskDesktopOrDefault —
	// asking is the point of turning the policy on.
	AskDesktop *bool `json:"ask_desktop,omitempty"`
	// Agents configures how each adapter is LAUNCHED, keyed by adapter id
	// (see AgentConfig). Nothing here affects the approval table above.
	Agents map[string]AgentConfig `json:"agents,omitempty"`
	// MCPServers are offered to every session, on top of whatever an
	// individual agent's entry adds.
	MCPServers []MCPServer `json:"mcp_servers,omitempty"`
	// Connections add named ways to reach an adapter ("claude@openrouter"),
	// replacing a built-in connection of the same name (see Connection).
	Connections map[string]Connection `json:"connections,omitempty"`
	// Catalogs override the built-in catalogs by id, or add the user's own.
	// Kept as raw JSON here: a catalog's slots are swarm.AgentProfile
	// values, swarm imports this package, and agentd, which reads them,
	// owns the check. Keeping them at all matters: Save rewrites the whole
	// file, and a field this struct did not know would be dropped by the
	// next "always allow" click.
	Catalogs map[string]json.RawMessage `json:"catalogs,omitempty"`
	// Launch is what the Agents window's launcher starts a session with
	// unless changed for that one start (LaunchPrefs). Written by the
	// launcher's Permissions row; a change made on a RUNNING session is
	// that session's and never lands here.
	Launch *LaunchPrefs `json:"launch,omitempty"`
}

// LaunchPrefs is the remembered default for a launch: the catalog and model
// "start an agent" means, and the two permission settings it starts with —
// the adapter's own approval preset and wash's auto-approval.
type LaunchPrefs struct {
	// Catalog is the catalog id a start that names none of its own uses,
	// and Model the slot or model id within it. This is what the Editor's
	// "new agent here", the Places agent icon and `wash ai <dir>` all
	// resolve against, so one setting decides what starting an agent means
	// everywhere (docs/PLACES.md §4.5).
	Catalog string `json:"catalog,omitempty"`
	Model   string `json:"model,omitempty"`
	// Mode is the adapter's session mode to start in, by adapter id: the
	// names are the adapter's own ("acceptEdits" on Claude Code, "read-only"
	// on Codex), so one remembered value cannot serve two adapters.
	Mode map[string]string `json:"mode,omitempty"`
	// Yolo starts every session with host-side auto-approval on.
	Yolo bool `json:"yolo,omitempty"`
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
// needed. Used by the "always allow" button (§12). cwd, when non-empty,
// scopes the rule to requests at or under that directory (Rule.Cwd);
// RuleScope says which tools get one.
//
// Read-modify-write against the file rather than an in-memory copy: the
// Agents settings pane is the other writer, and re-reading immediately
// before the write keeps the losing window to the length of this call.
// Appends (not prepends) so a hand-written deny higher up the table keeps
// beating a click made later.
//
// A rule that is already present — same match, decision AND scope — is a
// no-op, so double-clicking "always allow" doesn't grow the file.
func Append(path, match, decision, cwd string) error {
	if match == "" {
		return nil
	}
	p := Load(path)
	for _, r := range p.Rules {
		if r.Match == match && r.Decision == decision && r.Cwd == cwd {
			return nil
		}
	}
	p.Rules = append(p.Rules, Rule{Match: match, Decision: decision, Cwd: cwd})
	// Appending a rule implies the table is meant to be consulted.
	p.Enabled = true
	return Save(path, p)
}

// Update changes the policy file in place: read, change, write. Unlike
// Load, a file that exists but does not parse is an error here rather than
// a zero policy — a read-modify-write on a zero policy would replace the
// whole file, rules and all, with only what fn set.
func Update(path string, fn func(*Policy) error) error {
	var p Policy
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &p); err != nil {
			return fmt.Errorf("%s does not parse, not rewriting it: %w", path, err)
		}
	case !os.IsNotExist(err):
		return err
	}
	if err := fn(&p); err != nil {
		return err
	}
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

// RuleScope is the directory an "always allow" rule for tool should be
// confined to: the session's cwd for the shell tools, nothing for the
// rest.
//
// A Bash rule is about a command, and a command means different things in
// different trees — `make deploy*` allowed for a toy project must not
// also be allowed in production's checkout. Write/Edit already carry the
// cwd in their pattern (SuggestRule), and the read-only tools are the same
// risk everywhere, so a scope would only make those rules brittle.
func RuleScope(tool, cwd string) string {
	switch tool {
	case "Bash", "BashOutput", "KillShell":
		return strings.TrimRight(cwd, "/")
	}
	return ""
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

// ---- adapter configuration -------------------------------------------
//
// The approval table above is what wash decides. This is how the adapter
// is STARTED, which wash had no opinion about at all: the command was a
// hardcoded name on PATH, the args were a hardcoded list, the environment
// was whatever the router inherited, and `mcpServers` on session/new was
// literally always `[]` — so an agent under wash could not reach a single
// MCP server, however many the same agent reached from a terminal.
//
// It lives in the same file as the policy because it is the same file on
// disk: one place a person configures agents, not two.

// AgentConfig is one adapter's entry under `agents`, keyed by adapter id
// ("claude", "codex", "gemini").
type AgentConfig struct {
	// Command replaces the binary wash would have looked up. A wrapper
	// script, a version in ~/bin, a nix store path. Empty keeps the
	// built-in name.
	Command string `json:"command,omitempty"`
	// Args are EXTRA arguments, appended after the ones the adapter needs
	// to speak ACP at all — those are not a user's to remove, and an
	// adapter launched without them is not an ACP adapter.
	Args []string `json:"args,omitempty"`
	// Env is added to the adapter's environment. The router's own
	// environment is still inherited: this adds and overrides, it does not
	// replace, so an adapter does not lose PATH by gaining an API key.
	Env map[string]string `json:"env,omitempty"`
	// MCPServers are offered to this adapter in addition to the top-level
	// list.
	MCPServers []MCPServer `json:"mcp_servers,omitempty"`
}

// MCPServer is one MCP server, in wash's shape rather than ACP's — env as
// a map, because a config file is written by a person.
type MCPServer struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Connection is a named way to reach one adapter: the adapter plus the
// environment it runs with. `agents` has one entry per adapter, so it cannot
// say "Claude Code direct" and "Claude Code through OpenRouter" at once; a
// connection can. The adapter itself, by its id, is the direct connection
// and needs no entry.
type Connection struct {
	Adapter string `json:"adapter"`
	// Env is added after the adapter's own `agents` env.
	Env map[string]string `json:"env,omitempty"`
	// Key names a secret in the key store (keys.json), and KeyEnv the
	// variables it is given to. A connection whose key is not set cannot
	// start anything.
	Key    string   `json:"key,omitempty"`
	KeyEnv []string `json:"key_env,omitempty"`
}

// Launch is how one adapter is actually started, after the built-in
// defaults and the user's file have been merged.
type Launch struct {
	Command string
	Args    []string
	// Env is KEY=VALUE, to be APPENDED to the inherited environment.
	Env        []string
	MCPServers []MCPServer
}

// AgentFor returns the configuration for one adapter id, or the zero
// value. Nil-safe: an absent file configures nothing, which is the
// behaviour every box had before this existed.
func (p *Policy) AgentFor(id string) AgentConfig {
	if p == nil {
		return AgentConfig{}
	}
	return p.Agents[id]
}

// Merge folds this policy's configuration for `id` over the built-in
// launch for that adapter.
//
// Precedence, stated once because every part of it is a decision:
//
//   - command: the user's wins outright when set.
//   - args: built-in FIRST, then the user's. The built-in args are what
//     make the process an ACP adapter (`--experimental-acp`); appending
//     keeps them and still lets a later flag override an earlier one,
//     which is how every CLI resolves a repeat.
//   - env: added to what the process inherits, never replacing it.
//     Sorted, so a launch is reproducible and a test can read it.
//   - MCP servers: the top-level list, then this agent's. A per-agent
//     entry with the same name REPLACES the global one — naming it again
//     is how you say "not that one, this one" — and order is otherwise
//     preserved.
func (p *Policy) Merge(id string, base Launch) Launch {
	cfg := p.AgentFor(id)
	out := Launch{Command: base.Command, Args: append([]string(nil), base.Args...)}
	if cfg.Command != "" {
		out.Command = cfg.Command
	}
	out.Args = append(out.Args, cfg.Args...)
	out.Env = envList(cfg.Env)
	var global []MCPServer
	if p != nil {
		global = p.MCPServers
	}
	out.MCPServers = mergeMCP(global, cfg.MCPServers)
	return out
}

// envList renders an env map as sorted KEY=VALUE. An entry with an empty
// key is dropped: it cannot be set and would corrupt the block.
func envList(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

// mergeMCP concatenates the global and per-agent lists, letting a
// per-agent entry replace a global one of the same name IN PLACE, so the
// order a person wrote is the order the agent sees. Entries missing a
// name or a command are dropped: an MCP server wash cannot start is worse
// than one that was never offered.
func mergeMCP(global, own []MCPServer) []MCPServer {
	out := make([]MCPServer, 0, len(global)+len(own))
	for _, s := range global {
		if s.Name != "" && s.Command != "" {
			out = append(out, s)
		}
	}
	for _, s := range own {
		if s.Name == "" || s.Command == "" {
			continue
		}
		replaced := false
		for i := range out {
			if out[i].Name == s.Name {
				out[i], replaced = s, true
				break
			}
		}
		if !replaced {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// EnvPairs renders an env map as sorted {key, value} pairs — the shape a
// caller needs when the destination is not KEY=VALUE (ACP's mcpServers
// wants name/value objects). Same ordering and same empty-key rule as
// envList, so one launch is one order everywhere.
func EnvPairs(m map[string]string) [][2]string {
	out := make([][2]string, 0, len(m))
	for _, kv := range envList(m) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}
