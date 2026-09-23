package agentd

import (
	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/swarm"
	"log"
	"slices"
)

func (h *hosted) observeWorkspaceActivity(u acp.SessionUpdate) {
	h.turnMu.Lock()
	defer h.turnMu.Unlock()
	if !h.turnLive {
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
	h.turnMu.Lock()
	defer h.turnMu.Unlock()
	if h.closing.Load() {
		return "ended", ""
	}
	if h.activityAsks > 0 || decision {
		return "needs-input", ""
	}
	if !h.turnLive {
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
		h := workspaceHosted(m.Session)
		activity[m.ID], detail[m.ID] = workspaceMemberActivity(m, h, decisions[m.ID])
		if m.Usage != nil {
			usage[m.ID] = *m.Usage
		}
		if h != nil {
			hostedMu.Lock()
			used, size := h.used, h.size
			hostedMu.Unlock()
			if used > 0 || size > 0 {
				usage[m.ID] = swarm.Usage{Used: used, Size: size}
			}
		}
	}
	return activity, detail, usage
}
func (ws *workspaceService) captureUsage(h *hosted) {
	hostedMu.Lock()
	used, size := h.used, h.size
	hostedMu.Unlock()
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
