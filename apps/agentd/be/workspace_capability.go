package agentd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/swarm"
)

// This is a provider tool capability, not an OS sandbox. Pin the adapter contract:
// unknown versions must be reviewed before we claim their launch metadata works.
// createSession passes _meta.claudeCode.options to the SDK on both session/new
// and session/load. Codex's read-only mode is workspaceWrite.
//
// 0.81.1 verified against its dist/acp-agent.js: tools comes from options;
// disallowedTools is merged with the adapter's own; settingSources and
// strictMcpConfig reach the SDK through the options spread, after the
// adapter's defaults; settings (disableAllHooks) survives the provider merge;
// allowDangerouslySkipPermissions:false turns allowBypass off; permission
// requests for MCP tools carry _meta.claudeCode.mcpServer {name, source}.
//
// 0.81.2 re-verified, same file, each point unchanged: tools (options first,
// else the claude_code preset) and the disallowedTools merge sit after the
// spread; nothing after the spread re-sets settingSources or strictMcpConfig;
// the provider merge copies settings and adds only apiKeyHelper and env;
// allowBypass also needs settings.permissions.disableBypassPermissionsMode
// unset; load reaches createSession through getOrCreateSession with the
// request's _meta, and re-creates the session when its fingerprint changed.
var reviewerVerifiedVersions = []string{"0.81.1", "0.81.2"}

// reviewerVerifiedOpenCode are the OpenCode versions whose configuration
// layering and tool names opencodeReviewer was checked against. OpenCode's
// restriction is its launch environment, so it needs no session metadata.
var reviewerVerifiedOpenCode = []string{"1.18.32"}

// providerCapability is which launch settings a provider's adapter can
// enforce itself. Wash is agnostic to the provider: a setting the adapter
// cannot enforce is advisory, not a refusal. The member launches, Wash's own
// host guards apply where they can, and Member.Applied records what held, so
// the record says which reviews rest on enforcement and which on
// instruction. Only enforcement:"adapter" on a member turns this table into
// a gate. Discovery publishes it. A provider absent from a list does not
// enforce that setting.
var providerCapability = map[string][]string{
	// capability:"reviewer" — read/search only, enforced by the adapter.
	"reviewer": {"claude", "opencode"},
	// subagents:"deny" — removing the member's own subagent tool needs
	// claude-agent-acp's session metadata; no other adapter takes it.
	"subagents": {"claude"},
}

// Enforcement levels Member.Applied records for capability:"reviewer".
const (
	enforcementAdapter    = "adapter"    // the adapter's verified tool allowlist, plus host guards
	enforcementUnverified = "unverified" // the same allowlist on an adapter version Wash has not verified
	enforcementHost       = "host"       // Wash's host guards only; the adapter's own tools unrestricted
)

// reviewerEnforcement is how far capability:"reviewer" holds on the adapter
// a session reported, and why in one line when it is less than the adapter's
// own verified allowlist. Nothing here refuses: that is the caller's call,
// and only when the member asked for enforcement:"adapter".
func reviewerEnforcement(provider string, info acp.Implementation) (level, note string) {
	name, verified := reviewerAdapter(provider)
	switch {
	case name == "" || info.Name != name:
		return enforcementHost, fmt.Sprintf("reviewer restriction on %s %s is Wash's host guards only (file writes and terminals refused, edit/execute denied, coordination scoped); the adapter's own tools are not restricted and mode names are not read-only guarantees", info.Name, info.Version)
	case slices.Contains(verified, info.Version):
		return enforcementAdapter, ""
	default:
		return enforcementUnverified, fmt.Sprintf("reviewer tool allowlist applied on %s %s, not among the versions Wash verified it on (%s)", info.Name, info.Version, strings.Join(verified, ", "))
	}
}

// modelAdvisory says when a model id is not in the list the provider's
// adapter reported the last time it ran on this host, or "" when it is, or
// when the host cannot know yet. The launch does not fail on it (the member
// runs on the adapter's default, recorded in applied.notes); this is the
// warning before that. A catalog written as "claude-opus-5-5" against a
// list that only ever said "opus" failed every slot that resolved to it.
func modelAdvisory(provider, model string) string {
	if model == "" {
		return ""
	}
	mem := loadAdapterMemory()[provider]
	for _, c := range mem.Configs {
		if c.Category != "model" || len(c.Values) == 0 {
			continue
		}
		values := make([]string, 0, len(c.Values))
		for _, v := range c.Values {
			if v.Value == model {
				return ""
			}
			values = append(values, v.Value)
		}
		return fmt.Sprintf("model %q is not in the list %s %s last reported on this host (%s); the member would run on the adapter's default, recorded in applied.notes", model, provider, mem.Version, strings.Join(values, ", "))
	}
	return ""
}

// launchAdvisories is what an orchestrator should know about a member's
// advisory settings before it launches: which of them this provider, or the
// adapter version this host last ran, will not enforce. Returned with the
// configure receipt (preview too), so a review panel staffed on a provider
// with no tool allowlist is told so up front rather than discovered in a
// merge trailer.
func launchAdvisories(settings swarm.AgentProfile) []string {
	var out []string
	if settings.Capability == "reviewer" {
		name, _ := reviewerAdapter(settings.Provider)
		installed := loadAdapterMemory()[settings.Provider].Version
		switch {
		case name == "":
			out = append(out, fmt.Sprintf(`capability "reviewer" on %s: host guards only; the adapter's own tools are not restricted (enforcement "host")`, settings.Provider))
		case installed == "":
			out = append(out, fmt.Sprintf(`capability "reviewer" on %s: enforcement is decided at launch; no %s session has run on this host yet`, settings.Provider, settings.Provider))
		default:
			if level, note := reviewerEnforcement(settings.Provider, acp.Implementation{Name: name, Version: installed}); level != enforcementAdapter {
				out = append(out, fmt.Sprintf("capability \"reviewer\": %s (enforcement %q)", note, level))
			}
		}
	}
	if note := modelAdvisory(settings.Provider, settings.Model); note != "" {
		out = append(out, note)
	}
	if settings.Subagents == "deny" && !slices.Contains(providerCapability["subagents"], settings.Provider) {
		out = append(out, fmt.Sprintf(`subagents "deny" on %s: the adapter has no subagent control; the member is instructed not to spawn (enforced by %s only)`, settings.Provider, strings.Join(providerCapability["subagents"], ", ")))
	}
	return out
}

// reviewerAdapter is the adapter each provider's reviewer profile is checked
// against, and the versions it was verified on.
func reviewerAdapter(provider string) (name string, verified []string) {
	switch provider {
	case "claude":
		return claudeAdapter, reviewerVerifiedVersions
	case "opencode":
		return "OpenCode", reviewerVerifiedOpenCode
	}
	return "", nil
}

// strictReviewer says why a member that asked for enforcement:"adapter" will
// not launch on the adapter a session reported, or nil when it will. The pin
// (reviewerVerifiedVersions) is right — unknown versions must be reviewed
// before Wash claims their launch metadata works — but it gates only the
// member that asked to be gated; everyone else launches and is recorded.
func strictReviewer(provider string, info acp.Implementation) error {
	level, note := reviewerEnforcement(provider, info)
	if level == enforcementAdapter {
		return nil
	}
	return fmt.Errorf(`enforcement "adapter" cannot be met: %s. Install a verified version, or drop enforcement to launch with what holds recorded on the member (applied.enforcement) and in its Reviewed-by trailer`, note)
}

// reviewerHost is what this host can say about capability:"reviewer" for a
// provider before any session starts: the adapter version its last session
// reported (adapter memory), and the enforcement a reviewer launched now
// would record. A fresh machine gets the newest adapter and used to find
// out at launch, after the members were committed.
func reviewerHost(provider string) map[string]any {
	name, verified := reviewerAdapter(provider)
	out := map[string]any{"adapter": name, "verified_versions": verified}
	installed := loadAdapterMemory()[provider].Version
	switch {
	case name == "":
		out["installed_version"] = nil
		out["enforcement"] = enforcementHost
	case installed == "":
		out["installed_version"] = nil
		out["enforcement"] = "unknown until a " + provider + " session has run on this host; a verified version records \"adapter\", any other \"unverified\""
	default:
		level, _ := reviewerEnforcement(provider, acp.Implementation{Name: name, Version: installed})
		out["installed_version"] = installed
		out["enforcement"] = level
	}
	out["launches"] = true
	return out
}

// reviewerHostCheck refuses, before it is committed, a reviewer that asked
// for enforcement:"adapter" on a provider whose adapter cannot give it, or
// whose version this host last ran is unverified. Advisory reviewers (the
// default) pass; a host that has never run the provider is checked at
// launch.
func reviewerHostCheck(settings swarm.AgentProfile) error {
	if settings.Capability != "reviewer" || settings.Enforcement != "adapter" {
		return nil
	}
	name, _ := reviewerAdapter(settings.Provider)
	if name == "" {
		return fmt.Errorf(`enforcement "adapter" cannot be met on %s: no adapter Wash enforces a reviewer through (%s). Drop enforcement to launch with host guards only, recorded`, settings.Provider, strings.Join(providerCapability["reviewer"], ", "))
	}
	installed := loadAdapterMemory()[settings.Provider].Version
	if installed == "" {
		return nil
	}
	return strictReviewer(settings.Provider, acp.Implementation{Name: name, Version: installed})
}

// reviewerMetadata is the session metadata that restricts a reviewer's tools
// on the adapter a session reported, and the enforcement level that results.
// claude-agent-acp takes an allowlist (applied verified or not; a version
// difference changes what Wash can vouch for, not whether to try); OpenCode's
// restriction is its launch environment; any other adapter gets no metadata
// and Wash's host guards alone. Refuses only a member that asked for
// enforcement:"adapter" and cannot have it.
func reviewerMetadata(provider string, info acp.Implementation, enforcement string) (meta map[string]any, level string, err error) {
	level, _ = reviewerEnforcement(provider, info)
	if enforcement == "adapter" {
		if err := strictReviewer(provider, info); err != nil {
			return nil, level, err
		}
	}
	if level == enforcementHost || provider == "opencode" {
		return nil, level, nil
	}
	return map[string]any{"claudeCode": map[string]any{"options": map[string]any{
		"tools":           []string{"Read", "Glob", "Grep"},
		"disallowedTools": []string{"Write", "Edit", "MultiEdit", "NotebookEdit", "Bash", "Agent", "Task", "Skill", "EnterWorktree", "ExitWorktree"},
		"settingSources":  []string{}, "strictMcpConfig": true,
		"allowDangerouslySkipPermissions": false,
		"settings":                        map[string]any{"disableAllHooks": true},
	}}}, level, nil
}
func reviewerWorkspaceTool(name string) bool {
	switch name {
	case "workspace_get", "plan_get", "inbox_read", "member_update", "message_send", "assignment_update", "decision_request", "flash_message":
		return true
	}
	return false
}
func (h *hosted) reviewerPermission(tc acp.ToolCall) bool {
	meta, ok := claudeToolMeta(tc)
	if !ok {
		return false
	}
	// A foreign MCP tool cannot gain approval by claiming a read/search kind.
	if meta.Server.Name == "" && !strings.HasPrefix(meta.Tool, "mcp__") {
		return tc.Kind == acp.ToolKindRead || tc.Kind == acp.ToolKindSearch
	}
	tool, ok := washWorkspaceTool(tc)
	return ok && reviewerWorkspaceTool(tool)
}

// claudeMeta is what claude-agent-acp says about a tool call beyond its
// ACP kind: the provider's own tool name and, for MCP tools, which server.
type claudeMeta struct {
	Tool   string `json:"toolName"`
	Server struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	} `json:"mcpServer"`
}

func claudeToolMeta(tc acp.ToolCall) (claudeMeta, bool) {
	var meta struct {
		Claude claudeMeta `json:"claudeCode"`
	}
	if len(tc.Meta) > 0 && json.Unmarshal(tc.Meta, &meta) != nil {
		return claudeMeta{}, false
	}
	return meta.Claude, true
}

// washWorkspaceTool names the wash_workspace tool a call invokes, when it is
// one: the server Wash injected itself ("dynamic"), not a same-named server
// from the user's own configuration.
func washWorkspaceTool(tc acp.ToolCall) (string, bool) {
	meta, ok := claudeToolMeta(tc)
	const prefix = "mcp__wash_workspace__"
	if !ok || meta.Server.Name != "wash_workspace" || meta.Server.Source != "dynamic" || !strings.HasPrefix(meta.Tool, prefix) {
		return "", false
	}
	return strings.TrimPrefix(meta.Tool, prefix), true
}

// coordinationPermission is the approval a workspace's own coordination
// calls get without a human. Asking gained nothing: the bridge derives the
// caller from its session credentials and enforces every role limit itself
// (only the orchestrator configures, a reviewer cannot spawn), so a click
// could only ever approve what the server was going to check anyway. What it
// cost was one prompt per inbox read, status report and message, per member —
// a question with no subject that trained the human to click without reading.
func coordinationPermission(tc acp.ToolCall) bool {
	_, ok := washWorkspaceTool(tc)
	return ok
}

// savedWorkspaceLaunch is what a reopened session was launched as.
func savedWorkspaceLaunch(session string) sessionLaunch {
	if workspaces != nil {
		for _, w := range workspaces.store.Snapshot().Workspaces {
			for _, m := range w.Members {
				if m.Session == session {
					return memberLaunch(w, m)
				}
			}
		}
	}
	return sessionLaunch{}
}

// memberLaunch is how member m of w is (re)started.
func memberLaunch(w swarm.Workspace, m swarm.Member) sessionLaunch {
	l := sessionLaunch{member: m.ID != w.Lead && m.State != "ended"}
	if m.LaunchSettings != nil {
		l.connection = m.LaunchSettings.Connection
		l.capability = m.LaunchSettings.Capability
		l.enforcement = m.LaunchSettings.Enforcement
		l.noSubagents = m.LaunchSettings.Subagents == "deny"
	}
	return l
}

// noSubagentMetadata removes Claude's own subagent tool, for a member whose
// profile says subagents "deny": its work then stays in its own transcript
// and the workspace's accounting, instead of in background agents. Another
// adapter has no such control: no metadata, and the member is recorded as
// "instructed" — told not to spawn, not prevented.
func noSubagentMetadata(info acp.Implementation) (meta map[string]any, applied string) {
	if info.Name != claudeAdapter {
		return nil, "instructed"
	}
	return map[string]any{"claudeCode": map[string]any{"options": map[string]any{
		"disallowedTools": []string{"Agent", "Task"},
	}}}, "denied"
}

// workspaceApprovalPolicy is the decision path's one view of workspace-scoped
// rules. Nil-safe on purpose: the workspace service is optional (it logs and
// carries on if it cannot start), and an ordinary agent conversation has no
// workspace at all, so both must read as "nothing to say" rather than as a
// reason for the permission ladder to behave differently.
func workspaceApprovalPolicy(session string) (id, name string, policy agentpolicy.Policy) {
	if workspaces == nil || session == "" {
		return "", "", agentpolicy.Policy{}
	}
	return workspaces.store.ApprovalsFor(session)
}

// decideWithWorkspace runs the two rule tables in the order the permission
// ladder requires, and names which one answered so the log can say.
//
// The global table stays authoritative in BOTH directions. An allow or a deny
// in ~/.config/wash/agents.json is a decision the user made about this whole
// machine, and a per-workspace table — which an agent's own question can talk
// the user into writing — must be able to reopen neither. So the workspace
// table is reached only where global said "ask", which is exactly the case
// that would otherwise become one prompt per member per worktree.
func decideWithWorkspace(global, workspace agentpolicy.Policy, req agentpolicy.Request) (agentpolicy.Response, string) {
	res := agentpolicy.Evaluate(global, req)
	if res.Decision != agentpolicy.DecisionAsk || !workspace.Enabled {
		return res, "global"
	}
	if w := agentpolicy.Evaluate(workspace, req); w.Decision != agentpolicy.DecisionAsk {
		return w, "workspace"
	}
	return res, "global"
}
