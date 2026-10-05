package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
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
	if _, err = s.Setup("lead", "claude", t.TempDir(), "Team", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.Mutate("lead", true, func(w *swarm.Workspace, m *swarm.Member) error {
		w.Plan = []swarm.Node{{ID: "K5a", Title: "Loader", State: "todo", Revision: 1}}
		w.Members = append(w.Members,
			swarm.Member{ID: "impl", Name: "Implementer", Node: "K5a", Session: "impl-s", State: "available", Creator: m.ID,
				LaunchSettings: &swarm.AgentProfile{Provider: "claude", Configs: map[string]string{"mode": "plan"}}},
			swarm.Member{ID: "rev", Name: "Reviewer", Node: "K5a", Session: "rev-s", State: "available", Creator: m.ID,
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

// Refusing the plan exit ends the member's turn (claude-agent-acp answers it
// with deny+interrupt), so the plan would reach nobody. Observed: the member
// paused, its mail "uncertain", the plan fished out of ~/.claude/plans. Wash
// hands the orchestrator the plan and wakes it.
func TestARefusedPlanExitHandsTheOrchestratorThePlan(t *testing.T) {
	withStateDir(t)
	s, ws := planWorkspace(t)
	if _, err := s.Assign("lead", "impl", "", "", "Plan K5a", "", ""); err != nil {
		t.Fatal(err)
	}
	plan := "# K5a plan v3\n\n1. Loader stub at a fixed address.\n" + strings.Repeat("Detail line.\n", 400)
	ws.planExitDenied(&hosted{sessionID: "impl-s"}, plan)
	v := s.View("lead")
	last := v.Messages[len(v.Messages)-1]
	if last.Recipient != v.Lead || last.Sender != "impl" || last.Type != "question" || last.State != "queued" || !strings.Contains(last.Body, `"configure"`) || !strings.Contains(last.Body, "Loader stub at a fixed address") {
		t.Fatalf("orchestrator not woken with the plan: %+v", last)
	}
	if len(last.Body) > swarm.ReportLimit {
		t.Fatalf("report is %d bytes", len(last.Body))
	}
	_, path, _ := strings.Cut(last.Body, "Full plan: ")
	path, _, _ = strings.Cut(path, "\n")
	if b, err := os.ReadFile(path); err != nil || strings.TrimSpace(string(b)) != strings.TrimSpace(plan) {
		t.Fatalf("full plan not saved at %q: %v", path, err)
	}
}

// An idle plan-mode member wrote an empty plan and asked to leave plan mode;
// the orchestrator was woken to approve nothing. Without an open assignment
// there is no plan to hand over: no question, no plan file.
func TestARefusedPlanExitWithoutAnAssignmentWakesNobody(t *testing.T) {
	withStateDir(t)
	s, ws := planWorkspace(t)
	before := len(s.View("lead").Messages)
	ws.planExitDenied(&hosted{sessionID: "impl-s"}, "# Plan\n\nNothing assigned yet.")
	if got := s.View("lead").Messages; len(got) != before {
		t.Fatalf("orchestrator woken for an unassigned member's plan: %+v", got[len(got)-1])
	}
	if entries, err := os.ReadDir(filepath.Join(filepath.Dir(transcriptDir()), "workspace-plans")); err == nil && len(entries) != 0 {
		t.Fatalf("plan file written for an unassigned member: %v", entries)
	}
	// A completed assignment is not an open one either.
	a, err := s.Assign("lead", "impl", "", "", "Plan K5a", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Complete("impl-s", a.ID, "Done", false); err != nil {
		t.Fatal(err)
	}
	before = len(s.View("lead").Messages)
	ws.planExitDenied(&hosted{sessionID: "impl-s"}, "# Plan\n\nAfterthought.")
	if got := s.View("lead").Messages; len(got) != before {
		t.Fatalf("orchestrator woken after the assignment closed: %+v", got[len(got)-1])
	}
}

func TestARefusedPlanExitDoesNotPauseTheMember(t *testing.T) {
	resetAsks()
	withState(t, 1)
	withPolicy(t, agentpolicy.Policy{Enabled: true, Default: "allow"})
	old := workspaces
	workspaces = nil
	defer func() { workspaces = old }()
	h := &hosted{key: "acp:p", agent: "claude", cwd: t.TempDir(), workspaceMember: true}
	_, _ = h.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: acp.ToolCall{Kind: acp.ToolKindSwitchMode, RawInput: json.RawMessage(`{"plan":"x"}`)}, Options: exitPlanOptions})
	h.mu.Lock()
	interrupted := h.interrupted
	h.mu.Unlock()
	if !interrupted {
		t.Fatal("the turn the refusal ends would pause the member")
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
// workspace's accounting. subagents "deny" removes the tool at launch where
// the adapter takes session metadata; elsewhere the member is instructed,
// and the launch records which (the setting is advisory, not a refusal).
func TestSubagentsDenyRemovesClaudesAgentTool(t *testing.T) {
	meta, applied := noSubagentMetadata(acp.Implementation{Name: "@agentclientprotocol/claude-agent-acp", Version: "0.81.1"})
	b, _ := json.Marshal(meta)
	if applied != "denied" || !strings.Contains(string(b), `"disallowedTools":["Agent","Task"]`) {
		t.Fatal(applied, string(b))
	}
	if meta, applied = noSubagentMetadata(acp.Implementation{Name: "codex-acp", Version: "1.13.0"}); meta != nil || applied != "instructed" {
		t.Fatal("an adapter wash cannot restrict:", meta, applied)
	}
	if err := swarm.ValidateProfile(swarm.AgentProfile{Provider: "claude", Subagents: "sometimes"}); err == nil {
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
		bindTranscript(h.key, h.sessionID, h.record(), h.cwd, time.Now())
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
		h.mu.Lock()
		h.interrupted = interrupted
		h.mu.Unlock()
		promptHosted(h, turn{text: "Plan the loader", mailIDs: []string{msg.ID}})

		v := s.View("lead")
		member, delivery := swarm.GetMember(v, "impl").State, v.Messages[len(v.Messages)-1].State
		if interrupted && (member != "available" || delivery != "delivered") {
			t.Fatalf("interrupt: member %s, message %s; want available, delivered", member, delivery)
		}
		if !interrupted && member != "paused" {
			t.Fatalf("human stop: member %s, want paused", member)
		}
		h.mu.Lock()
		leftover := h.interrupted
		h.mu.Unlock()
		if leftover {
			t.Fatal("interrupt flag outlived its turn")
		}
		h.client.Close()
		toAgentW.Close()
		toClientW.Close()
		workspaces = old
	}
}

// member_control is the orchestrator's: a member's tool list leaves it out,
// and a member that calls it anyway, even on itself, is refused.
func TestMemberControlIsOrchestratorOnly(t *testing.T) {
	s, err := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Setup("lead", "claude", t.TempDir(), "Team", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.Mutate("lead", true, func(w *swarm.Workspace, m *swarm.Member) error {
		w.Members = append(w.Members, swarm.Member{ID: "worker", Key: "worker", Session: "worker-s", State: "available", Lifetime: "resident", Creator: m.ID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	args := json.RawMessage(`{"action":"pause","member_ids":["worker"]}`)
	if _, err = ws.call(context.Background(), &hosted{sessionID: "worker-s"}, workspacemcp.Call{Name: "member_control", Arguments: args}); err == nil || !strings.Contains(err.Error(), "orchestrator operation") {
		t.Fatalf("member controlled itself: %v", err)
	}
	for _, tool := range workspacemcp.MemberTools() {
		if tool.Name == "member_control" {
			t.Fatal("member tool list offers member_control")
		}
	}
}

// Relaunching a failed member while the orchestrator's failure has paused the
// workspace is refused; it used to force the workspace active behind the
// paused orchestrator.
func TestRelaunchDoesNotUnpauseTheWorkspace(t *testing.T) {
	s, err := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Setup("lead", "claude", t.TempDir(), "Team", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.Mutate("lead", true, func(w *swarm.Workspace, m *swarm.Member) error {
		w.Members = append(w.Members, swarm.Member{ID: "worker", Key: "worker", State: "failed", Lifetime: "resident", Creator: m.ID})
		w.State, m.State = "paused", "paused"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	result, err := ws.call(context.Background(), &hosted{sessionID: "lead"}, workspacemcp.Call{Name: "member_control", Arguments: json.RawMessage(`{"action":"resume","member_ids":["worker"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(result); !strings.Contains(string(b), "resume the orchestrator first") {
		t.Fatalf("relaunch outcome %s", b)
	}
	if s.View("lead").State != "paused" {
		t.Fatal("relaunch unpaused the workspace")
	}
}

// A setting the orchestrator adjusted on a live member (plan mode lifted,
// say) survives a relaunch as it survives a resume.
func TestMemberSettingsCarryAdjustments(t *testing.T) {
	launch := swarm.AgentProfile{Provider: "claude", Configs: map[string]string{"mode": "plan", "effort": "low"}}
	got := memberSettings(swarm.Member{LaunchSettings: &launch, Adjusted: map[string]string{"mode": "default"}})
	if got.Configs["mode"] != "default" || got.Configs["effort"] != "low" {
		t.Fatalf("settings %v", got.Configs)
	}
	if launch.Configs["mode"] != "plan" {
		t.Fatal("memberSettings changed the stored launch settings")
	}
}

// checkpoint is the safe stop: a priority instruction to save and hand off
// goes to the front of the member's queue and supersedes the orchestrator's
// earlier queued instructions, in one call (Redoubt, R12: a free-text
// "write your handoff" waited behind ten queued messages).
func TestCheckpointLeadsTheQueueAndSupersedes(t *testing.T) {
	s, _ := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	w, _ := s.Setup("lead", "codex", t.TempDir(), "Team", "")
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, _ *swarm.Member) error {
		w.Members = append(w.Members, swarm.Member{ID: "m", Key: "impl", Session: "m-session", Name: "Impl", State: "available", Lifetime: "resident"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	lead := &hosted{sessionID: "lead"}
	call := func(name, raw string) (any, error) {
		return ws.call(context.Background(), lead, workspacemcp.Call{Name: name, Arguments: json.RawMessage(raw)})
	}
	for _, body := range []string{"Fix the tests.", "Then the docs."} {
		if _, err := call("message_send", `{"recipient":"impl","type":"instruction","body":"`+body+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := call("member_control", `{"action":"interrupt","member_ids":["impl"],"body":"x"}`); err == nil || !strings.Contains(err.Error(), "body goes with checkpoint") {
		t.Fatalf("body on interrupt: %v", err)
	}
	got, err := call("member_control", `{"action":"checkpoint","member_ids":["impl"],"body":"Successor launches at 09:00."}`)
	if err != nil {
		t.Fatal(err)
	}
	outcome := got.(map[string]any)["outcomes"].([]any)[0].(map[string]any)
	result := outcome["result"].(map[string]any)
	if outcome["error"] != nil || result["superseded"] != 2 || result["interrupt"] != nil {
		t.Fatalf("checkpoint outcome: %+v", outcome)
	}
	batch, err := s.Next("m-session")
	if err != nil || len(batch) != 1 || batch[0].Priority != "checkpoint" || !strings.Contains(batch[0].Body, "member_update {handoff}") || !strings.Contains(batch[0].Body, "Successor launches at 09:00.") {
		t.Fatalf("the member's next turn: %+v %v", batch, err)
	}
	// message_send takes the same priority, on instructions only.
	if _, err := call("message_send", `{"recipient":"impl","type":"question","body":"?","priority":"checkpoint"}`); err == nil {
		t.Fatal("priority on a question")
	}
	if _, err := call("message_send", `{"recipient":"impl","type":"instruction","body":"Stop.","priority":"checkpoint"}`); err != nil {
		t.Fatal(err)
	}
	if n := len(w.Messages); n != 0 {
		t.Fatalf("setup view mutated: %d", n)
	}
}
