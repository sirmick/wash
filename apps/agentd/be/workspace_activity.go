package agentd

import (
	"fmt"
	"log"
	"slices"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
)

func (h *hosted) observeWorkspaceActivity(u acp.SessionUpdate) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.busy() {
		return
	} // Late events must not resurrect a finished turn.
	switch u.SessionUpdate {
	case acp.UpdateAgentThoughtChunk:
		h.activityPhase = "thinking"
	case acp.UpdateAgentMessageChunk:
		h.activityPhase = "responding"
	case acp.UpdateToolCall, acp.UpdateToolCallUpdate:
		if u.ToolCallID == "" {
			return
		}
		if h.activityTools == nil {
			h.activityTools = map[string]string{}
		}
		if u.Status == acp.ToolStatusCompleted || u.Status == acp.ToolStatusFailed {
			delete(h.activityTools, u.ToolCallID)
			h.activityPhase = "working"
		} else if u.SessionUpdate == acp.UpdateToolCall || u.Status == acp.ToolStatusInProgress || u.Status == acp.ToolStatusPending {
			title := u.Title
			if title == "" {
				title = h.activityTools[u.ToolCallID]
			}
			h.activityTools[u.ToolCallID] = title
		}
	}
}

func workspaceMemberActivity(m swarm.Member, h *hosted, decision bool) (string, string) {
	if m.State != "available" {
		return m.State, ""
	}
	if h == nil {
		return "offline", ""
	}
	if h.closing.Load() {
		return "ended", ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.activityAsks > 0 || decision {
		return "needs-input", ""
	}
	if !h.busy() {
		// Work left running in the background outlives the turn: the
		// member is waiting on it, not idle.
		if bg := backgroundLabel(h.bgTasks); bg != "" {
			return "background", bg
		}
		if m.Waiting != "" {
			return "waiting-message", m.Waiting
		}
		return "idle", ""
	}
	if len(h.activityTools) > 0 {
		ids := make([]string, 0, len(h.activityTools))
		for id := range h.activityTools {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return "tool", h.activityTools[ids[0]]
	}
	if h.activityPhase != "" {
		return h.activityPhase, ""
	}
	return "working", ""
}
func workspaceRuntime(w *swarm.Workspace) (map[string]string, map[string]string, map[string]swarm.Usage) {
	activity, detail, usage := map[string]string{}, map[string]string{}, map[string]swarm.Usage{}
	decisions := map[string]bool{}
	for _, msg := range w.Messages {
		if msg.Type == "decision_request" && msg.State == "recorded" {
			decisions[msg.Sender] = true
		}
	}
	for _, m := range w.Members {
		h := hostedBySession(m.Session)
		activity[m.ID], detail[m.ID] = workspaceMemberActivity(m, h, decisions[m.ID])
		if m.Usage != nil {
			usage[m.ID] = *m.Usage
		}
		if h != nil {
			h.mu.Lock()
			used, size := h.used, h.size
			h.mu.Unlock()
			if used > 0 || size > 0 {
				usage[m.ID] = swarm.Usage{Used: used, Size: size}
			}
		}
	}
	return activity, detail, usage
}
func (ws *workspaceService) captureUsage(h *hosted) {
	h.mu.Lock()
	used, size := h.used, h.size
	h.mu.Unlock()
	if used == 0 && size == 0 {
		return
	}
	// Ordinary sessions have no workspace to checkpoint.
	found := false
	for _, w := range ws.store.Snapshot().Workspaces {
		if w.State == "ended" {
			continue
		}
		for _, m := range w.Members {
			if m.Session == h.sessionID {
				found = true
				break
			}
		}
	}
	if !found {
		return
	}
	if err := ws.store.RecordUsage(h.sessionID, used, size); err != nil {
		log.Printf("agentd: workspace usage checkpoint: %v", err)
	}
}

// Pending approvals for every teammate, independent of the selected preview.
func workspaceApprovals(w *swarm.Workspace) []agentproto.WorkspaceApproval {
	out := []agentproto.WorkspaceApproval{}
	if svc == nil {
		return out
	}
	byKey := map[string]string{}
	for _, m := range w.Members {
		if h := hostedBySession(m.Session); h != nil {
			byKey[h.key] = m.ID
		}
	}
	for _, ask := range svc.Snapshot().Asks {
		if id := byKey[ask.RowKey]; id != "" {
			out = append(out, agentproto.WorkspaceApproval{ID: ask.ID, MemberID: id, Tool: ask.Tool, Subject: ask.Subject})
		}
	}
	return out
}

// defaultContextWarn is the share of a member's context window at which the
// orchestrator hears about it, unless the workspace says otherwise.
const defaultContextWarn = 0.6

// contextNudges tells each orchestrator, once per member, when a member has
// used most of its context window: DOC1 found a lead at 630K by accident,
// after it had stalled. The fix then is a handoff and a fresh member.
func (ws *workspaceService) contextNudges() {
	for _, w := range ws.store.Snapshot().Workspaces {
		if w.State != "active" {
			continue
		}
		warn := w.ContextWarn
		if warn == 0 {
			warn = defaultContextWarn
		}
		_, _, usage := workspaceRuntime(&w)
		for _, m := range w.Members {
			u, ok := usage[m.ID]
			if m.ID == w.Lead || m.State == "ended" || !ok || u.Size <= 0 || float64(u.Used) < warn*float64(u.Size) {
				continue
			}
			key := "context:" + m.ID
			if slices.Contains(w.Nudged, key) {
				continue
			}
			body := fmt.Sprintf("%s has used %d%% of its context window (%d of %d tokens). Before it stalls: have it write a handoff (member_update handoff), end it, and launch a replacement with handoff_from:%q.", m.Name, int(100*float64(u.Used)/float64(u.Size)), u.Used, u.Size, memberRef(m))
			_ = ws.store.Mutate(workspaceLeadSession(w), false, func(w *swarm.Workspace, _ *swarm.Member) error {
				swarm.NudgeOnce(w, key, body)
				return nil
			})
			ws.signal()
		}
	}
}

// memberRef is how a member is addressed: its key, or its id.
func memberRef(m swarm.Member) string {
	if m.Key != "" {
		return m.Key
	}
	return m.ID
}
