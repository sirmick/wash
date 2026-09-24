package agentd

import (
	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/swarm"
	"testing"
)

func TestWorkspaceActivityTracksToolsAndDoesNotResurrectIdleTurns(t *testing.T) {
	h := &hosted{turnLive: true}
	m := swarm.Member{State: "available", Waiting: "Awaiting instructions"}
	check := func(want string) {
		t.Helper()
		got, _ := workspaceMemberActivity(m, h, false)
		if got != want {
			t.Fatalf("got %s want %s", got, want)
		}
	}
	check("working") // A wait request is not idle until the turn ends.
	h.observeWorkspaceActivity(acp.SessionUpdate{SessionUpdate: acp.UpdateAgentThoughtChunk})
	check("thinking")
	for _, id := range []string{"a", "b"} {
		h.observeWorkspaceActivity(acp.SessionUpdate{SessionUpdate: acp.UpdateToolCall, ToolCall: acp.ToolCall{ToolCallID: id, Title: "Run tests", Status: acp.ToolStatusInProgress}})
	}
	check("tool")
	h.observeWorkspaceActivity(acp.SessionUpdate{SessionUpdate: acp.UpdateToolCallUpdate, ToolCall: acp.ToolCall{ToolCallID: "a", Status: acp.ToolStatusCompleted}})
	check("tool")
	h.observeWorkspaceActivity(acp.SessionUpdate{SessionUpdate: acp.UpdateAgentThoughtChunk})
	check("tool") // One remaining tool, despite concurrent narration.
	h.activityAsks = 1
	check("needs-input")
	h.activityAsks = 0
	h.observeWorkspaceActivity(acp.SessionUpdate{SessionUpdate: acp.UpdateToolCallUpdate, ToolCall: acp.ToolCall{ToolCallID: "b", Status: acp.ToolStatusFailed}})
	check("working")
	h.observeWorkspaceActivity(acp.SessionUpdate{SessionUpdate: acp.UpdateAgentMessageChunk})
	check("responding")
	h.turnLive = false
	h.observeWorkspaceActivity(acp.SessionUpdate{SessionUpdate: acp.UpdateToolCall, ToolCall: acp.ToolCall{ToolCallID: "late"}})
	check("waiting-message")
	if len(h.activityTools) != 0 {
		t.Fatal("late event retained as an active tool")
	}
	if got, _ := workspaceMemberActivity(m, h, true); got != "needs-input" {
		t.Fatal(got)
	}
	m.State = "paused"
	if got, _ := workspaceMemberActivity(m, h, true); got != "paused" {
		t.Fatal("paused lifecycle lost", got)
	}
	m.State = "ended"
	if got, _ := workspaceMemberActivity(m, nil, false); got != "ended" {
		t.Fatal(got)
	}
}
