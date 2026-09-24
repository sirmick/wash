package agentd

import (
	"testing"

	"github.com/sirmick/wash/internal/agentpolicy"
)

func bashReq(cmd, cwd string) agentpolicy.Request {
	return agentpolicy.Request{ToolName: "Bash", ToolInput: map[string]any{"command": cmd}, Cwd: cwd}
}

// The workspace table exists to answer questions the global one leaves open.
// It must never be able to answer one the global table already closed.
func TestWorkspaceRulesNeverReopenAGlobalDecision(t *testing.T) {
	workspace := agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk, Rules: []agentpolicy.Rule{
		{Match: "Bash(rm*)", Decision: agentpolicy.DecisionAllow},
		{Match: "Bash(git*)", Decision: agentpolicy.DecisionAllow},
	}}

	t.Run("a global deny holds", func(t *testing.T) {
		global := agentpolicy.Policy{Enabled: true, Rules: []agentpolicy.Rule{
			{Match: "Bash(rm*)", Decision: agentpolicy.DecisionDeny},
		}}
		res, scope := decideWithWorkspace(global, workspace, bashReq("rm -rf /", "/w"))
		if res.Decision != agentpolicy.DecisionDeny || scope != "global" {
			t.Fatalf("decision=%s scope=%s, want deny/global", res.Decision, scope)
		}
	})

	t.Run("a global allow is not narrowed", func(t *testing.T) {
		global := agentpolicy.Policy{Enabled: true, Rules: []agentpolicy.Rule{
			{Match: "Read", Decision: agentpolicy.DecisionAllow},
		}}
		deny := agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk, Rules: []agentpolicy.Rule{
			{Match: "Read", Decision: agentpolicy.DecisionDeny},
		}}
		res, scope := decideWithWorkspace(global, deny, agentpolicy.Request{ToolName: "Read", Cwd: "/w"})
		if res.Decision != agentpolicy.DecisionAllow || scope != "global" {
			t.Fatalf("decision=%s scope=%s, want allow/global", res.Decision, scope)
		}
	})

	t.Run("an open question reaches the workspace", func(t *testing.T) {
		global := agentpolicy.Policy{Enabled: true}
		res, scope := decideWithWorkspace(global, workspace, bashReq("git status", "/anywhere"))
		if res.Decision != agentpolicy.DecisionAllow || scope != "workspace" {
			t.Fatalf("decision=%s scope=%s, want allow/workspace", res.Decision, scope)
		}
	})
}

// A session with no workspace, and a box with no policy file, are the two
// ordinary states of this code. Neither may change what happens.
func TestNoWorkspaceLeavesTheLadderAlone(t *testing.T) {
	none := agentpolicy.Policy{}
	for _, tc := range []struct {
		name   string
		global agentpolicy.Policy
		want   string
	}{
		{"policy off", agentpolicy.Policy{}, agentpolicy.DecisionAsk},
		{"enabled, unmatched", agentpolicy.Policy{Enabled: true}, agentpolicy.DecisionAsk},
		{"enabled, matched", agentpolicy.Policy{Enabled: true, Rules: []agentpolicy.Rule{
			{Match: "Bash", Decision: agentpolicy.DecisionAllow},
		}}, agentpolicy.DecisionAllow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, scope := decideWithWorkspace(tc.global, none, bashReq("ls", "/w"))
			if res.Decision != tc.want || scope != "global" {
				t.Fatalf("decision=%s scope=%s, want %s/global", res.Decision, scope, tc.want)
			}
		})
	}
}

// The reason this feature exists: one answer has to cover members working in
// directories that are neither each other nor under project_root.
func TestOneWorkspaceAnswerCoversEveryWorktree(t *testing.T) {
	global := agentpolicy.Policy{Enabled: true}
	workspace := agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk, Rules: []agentpolicy.Rule{
		{Match: "Bash(go test*)", Decision: agentpolicy.DecisionAllow},
	}}
	for _, cwd := range []string{"/data/project", "/data/project/branches/k5", "/data/project-worktree", "/elsewhere"} {
		res, scope := decideWithWorkspace(global, workspace, bashReq("go test ./...", cwd))
		if res.Decision != agentpolicy.DecisionAllow || scope != "workspace" {
			t.Fatalf("cwd %s: decision=%s scope=%s, want allow/workspace", cwd, res.Decision, scope)
		}
	}
}
