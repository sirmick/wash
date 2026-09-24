package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/internal/workspacemcp"
)

// planWorkspace is a lead plus one plan-mode implementer and one reviewer.
func planWorkspace(t *testing.T) (*swarm.Store, *workspaceService) {
	t.Helper()
	s, err := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Setup("lead", "claude", t.TempDir(), "Team", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Mutate("lead", true, func(w *swarm.Workspace, m *swarm.Member) error {
		w.Members = append(w.Members,
			swarm.Member{ID: "impl", Name: "Implementer", Session: "impl-s", State: "available", Creator: m.ID,
				LaunchSettings: &swarm.AgentProfile{Provider: "claude", Configs: map[string]string{"mode": "plan"}}},
			swarm.Member{ID: "rev", Name: "Reviewer", Session: "rev-s", State: "available", Creator: m.ID,
				LaunchSettings: &swarm.AgentProfile{Provider: "claude", Capability: "reviewer"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s, &workspaceService{store: s}
}

// claude-agent-acp 0.81.1 sends ExitPlanMode as a switch_mode permission
// request whose first "allow" can clear the context and switch to auto mode.
var exitPlanOptions = []acp.PermissionOption{
	{OptionID: "exit_plan_clear_auto", Name: "Yes, clear context and use auto mode", Kind: acp.OptionAllowAlways},
	{OptionID: "default", Name: "Yes, and manually approve edits", Kind: acp.OptionAllowOnce},
	{OptionID: "plan", Name: "No, keep planning", Kind: acp.OptionRejectOnce},
}

// K5 went past a plan-only brief; with auto-approval, wash would have granted
// its exit from plan mode itself. A member never can, whatever the policy.
func TestAMemberCannotApproveItsOwnExitFromPlanMode(t *testing.T) {
	resetAsks()
	withState(t, 1)
	withPolicy(t, agentpolicy.Policy{Enabled: true, Default: "allow", Rules: []agentpolicy.Rule{{Match: "Acp:switch_mode", Decision: agentpolicy.DecisionAllow}}})
	old := workspaces
	workspaces = nil
	defer func() { workspaces = old }()
	exit := acp.RequestPermissionRequest{ToolCall: acp.ToolCall{Kind: acp.ToolKindSwitchMode, Title: "Approve Plan"}, Options: exitPlanOptions}

	member := &hosted{key: "acp:m", agent: "claude", cwd: t.TempDir(), workspaceMember: true, yolo: true}
	got, err := member.RequestPermission(context.Background(), exit)
	if err != nil || !reflect.DeepEqual(got, acp.Selected("plan")) {
		t.Fatal("member left plan mode on its own", got, err)
	}
	// Anything else still runs under the member's auto-approval.
	got, _ = member.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: acp.ToolCall{Kind: acp.ToolKindEdit}, Options: stdOptions})
	if !reflect.DeepEqual(got, acp.Selected("once")) {
		t.Fatal("the guard caught an ordinary tool call", got)
	}
	// A person's own session (and the orchestrator's) keeps its choice.
	own := &hosted{key: "acp:o", agent: "claude", cwd: t.TempDir(), yolo: true}
	if got, _ = own.RequestPermission(context.Background(), exit); reflect.DeepEqual(got, acp.Selected("plan")) {
		t.Fatal("a non-member session was held in plan mode")
	}
}

func TestADeclinedPlanExitTellsTheOrchestratorWithoutWakingIt(t *testing.T) {
	s, ws := planWorkspace(t)
	ws.planExitDenied(&hosted{sessionID: "impl-s"})
	v := s.View("lead")
	last := v.Messages[len(v.Messages)-1]
	if last.Recipient != v.Lead || last.Sender != "impl" || last.Type != "progress" || !strings.Contains(last.Body, `"configure"`) {
		t.Fatalf("orchestrator not told how to approve: %+v", last)
	}
}

// Approving a plan means switching the member out of plan mode without
// ending it and losing the context it built. Only the orchestrator may, and
// the change outlives a restart without rewriting the keyed definition.
func TestOrchestratorConfiguresALiveMember(t *testing.T) {
	s, ws := planWorkspace(t)
	control := func(session, raw string) (map[string]any, error) {
		res, err := ws.call(context.Background(), &hosted{sessionID: session}, workspacemcp.Call{Name: "member_control", Arguments: json.RawMessage(raw)})
		if err != nil {
			return nil, err
		}
		out := res.(map[string]any)["outcomes"].([]any)[0].(map[string]any)
		if e, _ := out["error"].(string); e != "" {
			return nil, &controlError{e}
		}
		return out, nil
	}
	if _, err := control("impl-s", `{"action":"configure","member_ids":["impl"],"configs":{"mode":"default"}}`); err == nil {
		t.Fatal("a member configured itself")
	}
	if _, err := control("lead", `{"action":"configure","member_ids":["impl"]}`); err == nil {
		t.Fatal("configure without configs accepted")
	}
	if _, err := control("lead", `{"action":"pause","member_ids":["impl"],"configs":{"mode":"default"}}`); err == nil {
		t.Fatal("configs accepted on another action")
	}
	if _, err := control("lead", `{"action":"configure","member_ids":["rev"],"configs":{"mode":"bypassPermissions"}}`); err == nil {
		t.Fatal("a reviewer was given a permission mode")
	}
	if _, err := control("lead", `{"action":"configure","member_ids":["impl"],"configs":{"mode":"default"}}`); err != nil {
		t.Fatal(err)
	}
	m := swarm.GetMember(s.View("lead"), "impl")
	if m.Adjusted["mode"] != "default" || m.LaunchSettings.Configs["mode"] != "plan" {
		t.Fatalf("adjusted=%v launch=%v: the change must be recorded beside the keyed definition", m.Adjusted, m.LaunchSettings.Configs)
	}
	if got := memberLaunch(*s.View("lead"), *m); !got.member || got.noSubagents {
		t.Fatalf("launch = %+v", got)
	}
}

type controlError struct{ s string }

func (e *controlError) Error() string { return e.s }

// A member's subagents are background work outside its transcript and the
// workspace's accounting. subagents "deny" removes the tool at launch, and
// fails closed where wash cannot remove it.
func TestSubagentsDenyRemovesClaudesAgentTool(t *testing.T) {
	meta, err := noSubagentMetadata(acp.Implementation{Name: "@agentclientprotocol/claude-agent-acp", Version: "0.81.1"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(meta)
	if !strings.Contains(string(b), `"disallowedTools":["Agent","Task"]`) {
		t.Fatal(string(b))
	}
	if _, err = noSubagentMetadata(acp.Implementation{Name: "codex-acp", Version: "1.13.0"}); err == nil {
		t.Fatal("an adapter wash cannot restrict was accepted")
	}
	if _, err = startHostedCapability("codex", t.TempDir(), nil, workspaceLaunch{member: true, noSubagents: true}); err == nil || !strings.Contains(err.Error(), "no session started") {
		t.Fatal("codex launched with subagents deny", err)
	}
	if err = swarm.ValidateProfile(swarm.AgentProfile{Provider: "claude", Subagents: "sometimes"}); err == nil {
		t.Fatal("invalid subagents value accepted")
	}
	w := swarm.Workspace{Lead: "lead"}
	if l := memberLaunch(w, swarm.Member{ID: "x", State: "available", LaunchSettings: &swarm.AgentProfile{Provider: "claude", Subagents: "deny"}}); !l.noSubagents || !l.member {
		t.Fatalf("resume would drop the restriction: %+v", l)
	}
}

// Interrupt is the gentle stop: the turn ends, what it carried counts as
// delivered and the member stays available. The same cancel from the human's
// Stop button still pauses the member.
func TestInterruptEndsTheTurnWithoutPausingTheMember(t *testing.T) {
	for _, interrupted := range []bool{true, false} {
		withStateDir(t)
		reset()
		withState(t, 1)
		s, ws := planWorkspace(t)
		old := workspaces
		workspaces = ws
		msg, err := s.Send("lead", "impl", "instruction", "Plan the loader", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Next("impl-s"); err != nil {
			t.Fatal(err)
		}

		toAgentR, toAgentW := io.Pipe()
		toClientR, toClientW := io.Pipe()
		h := &hosted{key: "acp:int", agent: "claude", sessionID: "impl-s", cwd: t.TempDir(), workspaceMember: true}
		h.client = acp.NewClient(toClientR, toAgentW, h)
		bindTranscript(h.key, h.sessionID, h.agent, h.cwd, time.Now())
		h.register()
		go func() {
			sc := bufio.NewScanner(toAgentR)
			for sc.Scan() {
				var m struct {
					ID     json.Number `json:"id"`
					Method string      `json:"method"`
				}
				if json.Unmarshal(sc.Bytes(), &m) == nil && m.Method == acp.MethodSessionPrompt {
					_, _ = io.WriteString(toClientW, `{"jsonrpc":"2.0","id":`+m.ID.String()+`,"result":{"stopReason":"cancelled"}}`+"\n")
				}
			}
		}()
		h.interrupted.Store(interrupted)
		promptHosted(h, turn{text: "Plan the loader", mailIDs: []string{msg.ID}})

		v := s.View("lead")
		member, delivery := swarm.GetMember(v, "impl").State, v.Messages[len(v.Messages)-1].State
		if interrupted && (member != "available" || delivery != "delivered") {
			t.Fatalf("interrupt: member %s, message %s; want available, delivered", member, delivery)
		}
		if !interrupted && member != "paused" {
			t.Fatalf("human stop: member %s, want paused", member)
		}
		if h.interrupted.Load() {
			t.Fatal("interrupt flag outlived its turn")
		}
		h.client.Close()
		toAgentW.Close()
		toClientW.Close()
		workspaces = old
	}
}
