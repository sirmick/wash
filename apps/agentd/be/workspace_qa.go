package agentd

import (
	"errors"

	"github.com/sirmick/wash/internal/swarm"
)

// qaView answers view=qa. Without a thread it is the index, one row per
// thread; with thread_id, that thread's events, paginated and byte-bounded
// like inbox history. A resolved thread read back as a header only has its
// events read from its file.
func qaView(w *swarm.Workspace, a workspaceArgs) (any, error) {
	if a.IncludeMessages {
		return nil, errors.New("QA view does not accept include_messages")
	}
	if a.Thread == "" {
		if a.After != "" || a.Limit != 0 {
			return nil, errors.New("QA pagination requires thread_id")
		}
		index := []map[string]any{}
		for _, q := range w.QA {
			if a.Package != "" && q.Package != a.Package {
				continue
			}
			row := map[string]any{"id": q.ID, "package": q.Package, "title": q.Title, "state": q.State, "assignee": q.Assignee, "revision": q.Revision}
			if q.Blocking {
				row["blocking"] = true
			}
			if q.Resumed {
				row["resumed"] = true
			}
			index = append(index, row)
		}
		return map[string]any{"threads": index}, nil
	}
	q := swarm.QA(w, a.Thread)
	if q == nil || a.Package != "" && q.Package != a.Package {
		return nil, errors.New("unknown QA thread")
	}
	thread := *q
	if thread.Archived {
		events, err := loadQAEvents(w.QADir, thread.ID)
		if err != nil {
			return nil, err
		}
		thread.Events = events
	}
	limit := a.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("limit must be 1–100")
	}
	events := []swarm.QAEvent{}
	after := a.After == ""
	more := false
	bytes := 0
	for _, e := range thread.Events {
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
	thread.Events = events
	cursor := a.After
	if len(events) > 0 {
		cursor = events[len(events)-1].ID
	}
	return map[string]any{"thread": thread, "cursor": cursor, "has_more": more}, nil
}
func qaSummary(w *swarm.Workspace) {
	for i := range w.QA {
		w.QA[i].Events = nil
	}
}
