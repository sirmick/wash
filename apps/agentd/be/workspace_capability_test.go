package agentd

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/internal/workspacemcp"
)

func TestReviewerCapabilityAdapterContractAndResume(t *testing.T) {
	info := acp.Implementation{Name: "@agentclientprotocol/claude-agent-acp", Version: "0.79.0"}
	meta, err := reviewerMetadata("claude", info)
	if err != nil {
		t.Fatal(err)
	}
	options := meta["claudeCode"].(map[string]any)["options"].(map[string]any)
	if !reflect.DeepEqual(options["tools"], []string{"Read", "Glob", "Grep"}) || options["strictMcpConfig"] != true || options["allowDangerouslySkipPermissions"] != false {
		t.Fatal(options)
	}
	info.Version = "0.81.1"
	if _, err = reviewerMetadata("claude", info); err != nil {
		t.Fatal("re-verified adapter refused:", err)
	}
	for _, version := range []string{"", "0.64.2", "0.80.0", "0.82.0"} {
		info.Version = version
		if _, err = reviewerMetadata("claude", info); err == nil {
			t.Fatal("unverified adapter accepted", version)
		}
	}
	if _, err = startHostedCapability("codex", t.TempDir(), nil, "reviewer", true); err == nil {
		t.Fatal("unsupported adapter launched")
	}
	s, _ := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	_, err = s.Setup("review-session", "claude", t.TempDir(), "Review", "", nil)
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
	if capability, member := savedWorkspaceLaunch("review-session"); capability != "reviewer" || member {
		t.Fatal("resume lost capability or took the lead for a member", capability, member)
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
