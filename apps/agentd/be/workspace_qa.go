package agentd

import (
	"errors"

	"github.com/sirmick/wash/internal/swarm"
)

// qaView keeps transcript bodies out of general workspace snapshots. Explicit
// thread reads are paginated and byte-bounded, like inbox history.
func qaView(w *swarm.Workspace, a workspaceArgs) (any, error) {
	if a.IncludeMessages {
		return nil, errors.New("QA view does not accept include_messages")
	}
	selected := []swarm.QAThread{}
	for _, q := range w.QA {
		if a.Package != "" && q.Package != a.Package {
			continue
		}
		if a.Thread != "" && q.ID != a.Thread {
			continue
		}
		selected = append(selected, q)
	}
	if a.Thread == "" {
		if a.After != "" || a.Limit != 0 {
			return nil, errors.New("QA pagination requires thread_id")
		}
		for i := range selected {
			selected[i].Events = nil
		}
		filtered := *w
		filtered.QA = nil
		for _, q := range w.QA {
			if a.Package == "" || q.Package == a.Package {
				filtered.QA = append(filtered.QA, q)
			}
		}
		return map[string]any{"threads": selected, "markdown": swarm.QAMarkdown(&filtered)}, nil
	}
	if len(selected) != 1 {
		return nil, errors.New("unknown QA thread")
	}
	limit := a.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("limit must be 1–100")
	}
	q := selected[0]
	events := []swarm.QAEvent{}
	after := a.After == ""
	more := false
	bytes := 0
	for _, e := range q.Events {
		if !after {
			if e.ID == a.After {
				after = true
			}
			continue
		}
		if len(events) >= limit || bytes+len(e.Body) > 200*1024 {
			more = true
			break
		}
		bytes += len(e.Body)
		events = append(events, e)
	}
	if !after {
		return nil, errors.New("unknown QA event cursor")
	}
	q.Events = events
	cursor := a.After
	if len(events) > 0 {
		cursor = events[len(events)-1].ID
	}
	return map[string]any{"thread": q, "cursor": cursor, "has_more": more}, nil
}
func qaSummary(w *swarm.Workspace) {
	w.QAPreamble = ""
	w.QAOriginalHash = ""
	for i := range w.QA {
		w.QA[i].Events = nil
	}
}
