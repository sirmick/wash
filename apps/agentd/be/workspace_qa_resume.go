package agentd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirmick/wash/internal/swarm"
)

// maxQAFileBytes bounds one thread file read back.
const maxQAFileBytes = 16 << 20

// readQADir reads every Wash thread file in dir, in thread-id order. Other
// Markdown there is left alone; a Wash file that is damaged stops the resume
// with its name, rather than resuming without it.
func readQADir(dir string) ([]swarm.QAFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []swarm.QAFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := readQAFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if !swarm.IsQAFile(b) {
			continue
		}
		f, err := swarm.DecodeQAFile(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if f.Thread.ID+".md" != e.Name() {
			return nil, fmt.Errorf("%s holds thread %s", e.Name(), f.Thread.ID)
		}
		files = append(files, *f)
	}
	if len(files) > 500 {
		return nil, errors.New("QA directory holds more than 500 threads")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Thread.ID < files[j].Thread.ID })
	return files, nil
}

// restoreQA resumes an earlier workspace's threads. Open threads come back
// whole and return to the new orchestrator until it assigns its team.
// Resolved threads come back as headers only (their files keep the events)
// and are marked resumed, since their evidence is about the code as it was.
// Pending owner decisions are asked again.
func restoreQA(w *swarm.Workspace, files []swarm.QAFile) error {
	if len(w.QA) > 0 {
		return errors.New("workspace already has QA threads; resume the directory in a new workspace")
	}
	if w.QAAuthors == nil {
		w.QAAuthors = map[string]string{}
	}
	for _, f := range files {
		for id, name := range f.Authors {
			w.QAAuthors[id] = name
		}
	}
	lead := swarm.GetMember(w, w.Lead)
	for _, f := range files {
		q := f.Thread
		if q.State == "resolved" {
			q.Assignee, q.Resumed, q.Archived, q.Events = w.Lead, true, true, nil
			w.QA = append(w.QA, q)
			continue
		}
		if q.Events == nil {
			q.Events = []swarm.QAEvent{}
		}
		w.QA = append(w.QA, q)
		rev, prior := q.Revision, q.State
		if _, err := swarm.UpdateQA(w, lead, swarm.QAUpdate{ID: q.ID, Action: "assign", Expected: &rev, Assignee: w.Lead, Body: "Resumed from the QA directory; the orchestrator assigns the current team."}); err != nil {
			return err
		}
		if prior == "blocked" {
			swarm.QA(w, q.ID).State = "blocked"
		}
	}
	for _, f := range files {
		for _, msg := range f.Decisions {
			msg.Swarm, msg.Sender, msg.Recipient, msg.State = w.ID, w.Lead, "human", "recorded"
			w.Messages = append(w.Messages, msg)
			swarm.QA(w, msg.Thread).State = "awaiting-owner"
		}
	}
	return nil
}

// loadQAEvents reads a header-only thread's events back from its file.
func loadQAEvents(dir, id string) ([]swarm.QAEvent, error) {
	if dir == "" {
		return nil, fmt.Errorf("QA thread %s has its events in a file, and the workspace has no QA directory", id)
	}
	b, err := readQAFile(filepath.Join(dir, id+".md"))
	if err != nil {
		return nil, err
	}
	f, err := swarm.DecodeQAFile(b)
	if err != nil {
		return nil, fmt.Errorf("%s.md: %w", id, err)
	}
	if f.Thread.ID != id {
		return nil, fmt.Errorf("%s.md holds thread %s", id, f.Thread.ID)
	}
	if f.Thread.Events == nil {
		return []swarm.QAEvent{}, nil
	}
	return f.Thread.Events, nil
}

// unarchiveQA loads the events of the header-only threads ids names, so they
// can change again (a reopen appends to the history it had).
func (ws *workspaceService) unarchiveQA(session string, ids []string) error {
	w := ws.store.View(session)
	if w == nil {
		return nil
	}
	for _, id := range ids {
		q := swarm.QA(w, id)
		if q == nil || !q.Archived {
			continue
		}
		events, err := loadQAEvents(w.QADir, id)
		if err != nil {
			return err
		}
		if err := ws.store.Mutate(session, false, func(w *swarm.Workspace, _ *swarm.Member) error {
			if q := swarm.QA(w, id); q != nil && q.Archived {
				q.Events, q.Archived = events, false
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
