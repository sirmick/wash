package agentd

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
)

// Background work: a command an agent started in the background (a Bash run
// with run_in_background), which outlives the turn that started it. With the
// turn over, the session looked idle while it waited for that work; it is
// shown as "Background: <what>" instead. claude-agent-acp reports these
// tasks when the client advertises asyncTasks (adapters.go).

const (
	updateAsyncTaskSpawned  = "async_task_spawned"
	updateAsyncTaskState    = "async_task_state_update"
	updateAsyncTaskProgress = "async_task_progress"
)

// trackAsyncTask follows one background-task update. raw is the
// session/update notification as it came (the update inside it).
func (h *hosted) trackAsyncTask(raw json.RawMessage) {
	var envelope struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Update) > 0 {
		raw = envelope.Update
	}
	var u struct {
		Kind        string `json:"sessionUpdate"`
		ID          string `json:"asyncTaskId"`
		Name        string `json:"name"`
		Description string `json:"description"`
		TaskType    string `json:"taskType"`
		State       string `json:"state"`
	}
	if json.Unmarshal(raw, &u) != nil || u.ID == "" {
		return
	}
	what := u.Description
	if what == "" {
		what = u.Name
	}
	if what == "" {
		what = u.TaskType
	}
	h.mu.Lock()
	prior := backgroundLabel(h.bgTasks)
	if h.bgTasks == nil {
		h.bgTasks = map[string]string{}
	}
	switch u.Kind {
	case updateAsyncTaskSpawned:
		if what == "" {
			what = "background task"
		}
		h.bgTasks[u.ID] = firstLine(what, 80)
	case updateAsyncTaskProgress:
		if _, ok := h.bgTasks[u.ID]; ok && what != "" {
			h.bgTasks[u.ID] = firstLine(what, 80)
		}
	case updateAsyncTaskState:
		if u.State == "completed" || u.State == "failed" || u.State == "stopped" {
			delete(h.bgTasks, u.ID)
		}
	}
	label := backgroundLabel(h.bgTasks)
	h.mu.Unlock()
	if prior != label {
		log.Printf("agentd: acp background key=%s task=%s %s state=%s now=%q", h.key, u.ID, u.Kind, u.State, label)
		h.publishRow()
		if workspaces != nil {
			workspaces.signal()
		}
	}
}

// backgroundLabel says what a session's background work is: the one task,
// or the first and how many more.
func backgroundLabel(tasks map[string]string) string {
	if len(tasks) == 0 {
		return ""
	}
	ids := make([]string, 0, len(tasks))
	for id := range tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	label := tasks[ids[0]]
	if len(ids) > 1 {
		label += fmt.Sprintf(" (+%d more)", len(ids)-1)
	}
	return strings.TrimSpace(label)
}

// background is what the session is running in the background, or "".
func (h *hosted) background() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return backgroundLabel(h.bgTasks)
}
