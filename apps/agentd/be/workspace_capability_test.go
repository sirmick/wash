package agentd

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/internal/workspacemcp"
)

func TestReviewerCapabilityAdapterContractAndResume(t *testing.T) {
	info := acp.Implementation{Name: "@agentclientprotocol/claude-agent-acp", Version: "0.81.1"}
	meta, level, err := reviewerMetadata("claude", info, "")
	if err != nil || level != enforcementAdapter {
		t.Fatal(level, err)
	}
	options := meta["claudeCode"].(map[string]any)["options"].(map[string]any)
	if !reflect.DeepEqual(options["tools"], []string{"Read", "Glob", "Grep"}) || options["strictMcpConfig"] != true || options["allowDangerouslySkipPermissions"] != false {
		t.Fatal(options)
	}
	info.Version = "0.81.2"
	if _, level, err = reviewerMetadata("claude", info, ""); err != nil || level != enforcementAdapter {
		t.Fatal("re-verified adapter:", level, err)
	}
	// An unverified version (0.79.0 was verified once and is no longer
	// installed anywhere: dropped) gets the same allowlist, recorded as
	// unverified; only a member that asked for enforcement "adapter" is
	// refused.
	for _, version := range []string{"", "0.64.2", "0.79.0", "0.80.0", "0.82.0"} {
		info.Version = version
		meta, level, err := reviewerMetadata("claude", info, "")
		if err != nil || level != enforcementUnverified || meta == nil {
			t.Fatal("unverified adapter:", version, level, meta, err)
		}
		if _, _, err := reviewerMetadata("claude", info, "adapter"); err == nil || !strings.Contains(err.Error(), `enforcement "adapter" cannot be met`) {
			t.Fatal("strict reviewer launched unverified", version, err)
		}
	}
	// An adapter Wash has no allowlist for launches with host guards only.
	codex := acp.Implementation{Name: "codex-acp", Version: "1.13.0"}
	if meta, level, err := reviewerMetadata("codex", codex, ""); err != nil || meta != nil || level != enforcementHost {
		t.Fatal("host-only reviewer:", meta, level, err)
	}
	if _, _, err := reviewerMetadata("codex", codex, "adapter"); err == nil {
		t.Fatal("strict reviewer launched on codex")
	}
	// OpenCode's restriction is its launch configuration, not session
	// metadata, and is vouched for only at the version it was checked on.
	oc := acp.Implementation{Name: "OpenCode", Version: "1.18.32"}
	if meta, level, err := reviewerMetadata("opencode", oc, ""); err != nil || meta != nil || level != enforcementAdapter {
		t.Fatal("verified OpenCode:", meta, level, err)
	}
	if _, level, err := reviewerMetadata("opencode", acp.Implementation{Name: "OpenCode", Version: "1.19.0"}, ""); err != nil || level != enforcementUnverified {
		t.Fatal("newer OpenCode:", level, err)
	}
	if _, level, err := reviewerMetadata("opencode", acp.Implementation{Name: "opencode-fork", Version: "1.18.32"}, ""); err != nil || level != enforcementHost {
		t.Fatal("foreign adapter on the opencode provider:", level, err)
	}
	s, _ := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	_, err = s.Setup("review-session", "claude", t.TempDir(), "Review", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Mutate("review-session", true, func(w *swarm.Workspace, m *swarm.Member) error {
		m.LaunchSettings = &swarm.AgentProfile{Provider: "claude", Capability: "reviewer"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old := workspaces
	workspaces = &workspaceService{store: s}
	defer func() { workspaces = old }()
	if l := savedWorkspaceLaunch("review-session"); l.capability != "reviewer" || l.member {
		t.Fatal("resume lost capability or took the lead for a member", l)
	}
}
func TestReviewerCannotWriteExecuteConfigureOrBypassViaYolo(t *testing.T) {
	h := &hosted{capability: "reviewer", yolo: true, cwd: t.TempDir()}
	old := hostedPolicy
	hostedPolicy = func() agentpolicy.Policy { return agentpolicy.Policy{Enabled: true, Default: "allow"} }
	defer func() { hostedPolicy = old }()
	if err := h.WriteTextFile(context.Background(), acp.WriteTextFileRequest{Path: filepath.Join(h.cwd, "bad"), Content: "bad"}); err == nil {
		t.Fatal("write allowed")
	}
	if _, err := h.CreateTerminal(context.Background(), acp.CreateTerminalRequest{Command: "touch bad"}); err == nil {
		t.Fatal("execute allowed")
	}
	ws := &workspaceService{}
	if _, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("configuration allowed")
	}
	options := []acp.PermissionOption{{OptionID: "yes", Kind: acp.OptionAllowOnce}, {OptionID: "no", Kind: acp.OptionRejectOnce}}
	for _, kind := range []string{acp.ToolKindExecute, acp.ToolKindEdit, acp.ToolKindDelete} {
		got, err := h.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: acp.ToolCall{Kind: kind}, Options: options})
		if err != nil || !reflect.DeepEqual(got, acp.Selected("no")) {
			t.Fatal(kind, got, err)
		}
	}
	for _, tool := range []string{"workspace_get", "member_update", "message_send"} {
		meta, _ := json.Marshal(map[string]any{"claudeCode": map[string]any{"toolName": "mcp__wash_workspace__" + tool, "mcpServer": map[string]string{"name": "wash_workspace", "source": "dynamic"}}})
		tc := acp.ToolCall{Meta: meta}
		got, err := h.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: tc, Options: options})
		if err != nil || !reflect.DeepEqual(got, acp.Selected("yes")) {
			t.Fatal(tool, got, err)
		}
	}
	for _, meta := range []string{`{}`, `{"claudeCode":{"toolName":"mcp__wash_workspace__workspace_get","mcpServer":{"name":"wash_workspace","source":"plugin"}}}`, `{"claudeCode":{"toolName":"mcp__wash_workspace__workspace_end","mcpServer":{"name":"wash_workspace","source":"dynamic"}}}`} {
		if h.reviewerPermission(acp.ToolCall{Meta: json.RawMessage(meta)}) {
			t.Fatal("unscoped coordination permission", meta)
		}
	}
	hostedPolicy = func() agentpolicy.Policy { return agentpolicy.Policy{Enabled: true, Default: "deny"} }
	got, err := h.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: acp.ToolCall{Kind: acp.ToolKindRead}, Options: options})
	if err != nil || !reflect.DeepEqual(got, acp.Selected("no")) {
		t.Fatal("profile overrode owner deny", got, err)
	}
}

func washCall(tool, source string) acp.ToolCall {
	meta, _ := json.Marshal(map[string]any{"claudeCode": map[string]any{"toolName": "mcp__wash_workspace__" + tool, "mcpServer": map[string]string{"name": "wash_workspace", "source": source}}})
	return acp.ToolCall{Kind: "other", Meta: meta}
}

// Every member, not just reviewers, coordinates without a human in the loop:
// the observed cost was a subject-less "Acp:other" prompt per inbox read.
func TestCoordinationCallsDoNotAskTheHuman(t *testing.T) {
	resetAsks()
	withState(t, 0) // nobody home: an ask is deferred, so it comes back cancelled
	withPolicy(t, agentpolicy.Policy{Enabled: true, Default: "ask"})
	h := &hosted{key: "acp:t", cwd: t.TempDir()}
	options := []acp.PermissionOption{{OptionID: "yes", Kind: acp.OptionAllowOnce}, {OptionID: "no", Kind: acp.OptionRejectOnce}}
	ask := func(tc acp.ToolCall) acp.RequestPermissionResponse {
		got, err := h.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: tc, Options: options})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, tool := range []string{"workspace_get", "workspace_configure", "member_update", "inbox_read", "message_send", "member_control", "workspace_end"} {
		if got := ask(washCall(tool, "dynamic")); !reflect.DeepEqual(got, acp.Selected("yes")) {
			t.Error(tool, got)
		}
	}
	// A user-configured server borrowing the name still asks.
	if got := ask(washCall("workspace_get", "plugin")); reflect.DeepEqual(got, acp.Selected("yes")) {
		t.Error("approved a configured server without asking")
	}
	// An explicit owner deny outranks the bridge.
	withPolicy(t, agentpolicy.Policy{Enabled: true, Default: "ask", Rules: []agentpolicy.Rule{{Match: "mcp__wash_workspace__message_send", Decision: "deny"}}})
	if got := ask(washCall("message_send", "dynamic")); !reflect.DeepEqual(got, acp.Selected("no")) {
		t.Error("coordination overrode owner deny", got)
	}
}
