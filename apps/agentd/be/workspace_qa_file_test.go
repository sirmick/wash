package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/internal/workspacemcp"
)

func qaFileCall(t *testing.T, ws *workspaceService, h *hosted, name string, args any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return ws.call(context.Background(), h, workspacemcp.Call{Name: name, Arguments: raw})
}
func TestQADocumentLiveConcurrentHistoryRecoveryAndDetach(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	path := filepath.Join(dir, "QA.md")
	s, err := swarm.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_document": map[string]string{"path": path, "title": "Project questions"}, "request_id": "setup", "preview": true}
	if _, err = qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preview wrote QA file", err)
	}
	delete(setup, "preview")
	if _, err = qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	lead := s.View("lead").Lead
	open := map[string]any{"recipient": lead, "type": "question", "body": "Original question", "qa": map[string]any{"id": "q", "action": "open", "package": "K5", "title": "Question"}}
	if _, err = qaFileCall(t, ws, h, "message_send", open); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := qaFileCall(t, ws, h, "member_update", map[string]any{"request_id": fmt.Sprintf("reply-%d", i), "qa_updates": []any{map[string]any{"id": "q", "action": "reply", "body": fmt.Sprintf("Evidence [%d]", i)}}})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	read := func() string {
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	text := read()
	for i := 0; i < 40; i++ {
		if !strings.Contains(text, fmt.Sprintf("Evidence [%d]", i)) {
			t.Fatal("lost reply", i)
		}
	}
	if !strings.Contains(text, "Original question") || strings.Contains(text, "omitted") || !strings.Contains(text, "# Project questions") {
		t.Fatal("incomplete export", text)
	}
	// Human answers are exported through the GUI path too.
	result, err := qaFileCall(t, ws, h, "decision_request", map[string]any{"text": "Choose A or B", "thread_id": "q"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": result.(map[string]any)["id"], "body": "Use A"})
	if _, err = ws.answer(h, raw); err != nil {
		t.Fatal(err)
	}
	if text = read(); !strings.Contains(text, "Owner · Owner decision") || !strings.Contains(text, "Use A") {
		t.Fatal(text)
	}
	// Reopening the backend and rebuilding a missing projection needs no live GUI.
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	recovered, err := swarm.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	ws = &workspaceService{store: recovered}
	ws.syncQADocuments()
	if read() != text {
		t.Fatal("recovery changed QA history")
	}
	before, _ := os.Stat(path)
	ws.syncQADocuments()
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("unchanged QA rewrote file")
	}
	if _, err = qaFileCall(t, ws, h, "workspace_configure", map[string]any{"qa_document": nil}); err != nil {
		t.Fatal(err)
	}
	if _, err = qaFileCall(t, ws, h, "member_update", map[string]any{"qa_updates": []any{map[string]any{"id": "q", "action": "reply", "body": "After detaching"}}}); err != nil {
		t.Fatal(err)
	}
	if read() != text {
		t.Fatal("detached file modified")
	}
}
func TestQADocumentProtectsExistingFilesAndReportsWriteFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := swarm.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	plan := filepath.Join(dir, "PLAN.md")
	if err = os.WriteFile(plan, []byte("# Human plan"), 0600); err != nil {
		t.Fatal(err)
	}
	config := func(path string) (any, error) {
		return qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_document": map[string]string{"path": path}})
	}
	if _, err = config(plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(swarm.QAMarkdown(s.View("lead")), "# Human plan") {
		t.Fatal("existing Markdown was not loaded")
	}
	if _, err = qaFileCall(t, ws, h, "workspace_configure", map[string]any{"qa_document": nil}); err != nil {
		t.Fatal(err)
	}
	// Keep the unrelated symlink target separate from the adopted Markdown.
	plan = filepath.Join(dir, "UNRELATED.md")
	if err = os.WriteFile(plan, []byte("# Human plan"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "QA.md")
	if err = os.Symlink(plan, path); err != nil {
		t.Fatal(err)
	}
	if _, err = config(path); err == nil {
		t.Fatal("accepted symlink")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = config(path); err != nil {
		t.Fatal(err)
	}
	lead := s.View("lead").Lead
	// Replace output with a symlink after setup. QA commits, export fails visibly,
	// and the unrelated target stays intact. Retry after repair regenerates it.
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(plan, path); err != nil {
		t.Fatal(err)
	}
	result, err := qaFileCall(t, ws, h, "message_send", map[string]any{"recipient": lead, "type": "question", "body": "Retain this question", "qa": map[string]any{"id": "q", "action": "open", "package": "K5", "title": "Question"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["qa_document_status"].(qaDocumentStatus).State != "error" {
		t.Fatal(result)
	}
	if len(s.View("lead").QA) != 1 {
		t.Fatal("lost durable question")
	}
	b, _ := os.ReadFile(plan)
	if string(b) != "# Human plan" {
		t.Fatal("overwrote unrelated file")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ws.syncQADocuments()
	if ws.qaDocumentStatus(s.View("lead")).State != "saved" {
		t.Fatal("projection did not recover")
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "Retain this question") {
		t.Fatal(string(b))
	}
}

func TestQADocumentResumesFromFileWithoutStoreAndRetainsDecisions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "QA.md")
	s, _ := swarm.Open(filepath.Join(dir, "first.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "first", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_document": map[string]string{"path": path}}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	lead := s.View(h.sessionID).Lead
	if _, err := qaFileCall(t, ws, h, "message_send", map[string]any{"recipient": lead, "type": "question", "body": "Keep evidence", "qa": map[string]any{"id": "q", "action": "open", "package": "K5", "title": "Decision"}}); err != nil {
		t.Fatal(err)
	}
	result, err := qaFileCall(t, ws, h, "decision_request", map[string]any{"text": "Use A?", "thread_id": "q"})
	if err != nil {
		t.Fatal(err)
	}
	decision := result.(map[string]any)["id"]
	old := s.View(h.sessionID)
	if _, err = qaFileCall(t, ws, h, "workspace_end", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	// A completely separate backend has only the Markdown file.
	s2, _ := swarm.Open(filepath.Join(dir, "second.json"))
	ws2 := &workspaceService{store: s2}
	h2 := &hosted{sessionID: "second", agent: "codex", cwd: dir}
	if _, err = qaFileCall(t, ws2, h2, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	w := s2.View(h2.sessionID)
	if len(w.QA) != 1 || w.QA[0].State != "awaiting-owner" || w.QA[0].Assignee != w.Lead || w.QA[0].Events[0].Author != old.Lead || w.QAAuthors[old.Lead] == "" {
		t.Fatalf("bad restore: %+v", w)
	}
	raw, _ := json.Marshal(map[string]any{"id": decision, "body": "Use A"})
	if _, err = ws2.answer(h2, raw); err != nil {
		t.Fatal(err)
	}
	if s2.View(h2.sessionID).QA[0].State != "open" {
		t.Fatal("restored decision cannot be answered")
	}
}

// Stopping the orchestrator's session from the Agent controls ends its
// workspace, as workspace_end would. Before, only the lead's turn ended: the
// workspace stayed paused with no one able to lead it, holding the QA file, so
// a new orchestrator's setup failed with "QA document is used by another
// workspace". Observed live after a restart. A lead whose session never loaded
// (a failed reopen) keeps its workspace for a later resume.
func TestEndingTheLeadSessionEndsTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "QA.md")
	state := filepath.Join(dir, "state.json")
	s, _ := swarm.Open(state)
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "first", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_document": map[string]string{"path": path}}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	s, err := swarm.Open(state) // the restart pauses the workspace
	if err != nil {
		t.Fatal(err)
	}
	ws = &workspaceService{store: s}
	ws.retired(h) // never loaded
	if w := s.View(h.sessionID); w == nil || w.State != "paused" {
		t.Fatalf("failed reopen ended the workspace: %+v", w)
	}
	h.sessionReady.Store(true)
	ws.retired(h)
	if w := s.View(h.sessionID); w != nil {
		t.Fatalf("workspace survived its lead: %q", w.State)
	}
	for _, m := range s.Snapshot().Workspaces[0].Members {
		if m.State != "ended" {
			t.Fatalf("member %s left %q", m.Name, m.State)
		}
	}
	next := &hosted{sessionID: "next", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, next, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
}

func TestQADocumentFinalFailureRetriesAndNewRunTakesLatestHistory(t *testing.T) {
	for _, reopen := range []bool{false, true} {
		t.Run(fmt.Sprint(reopen), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "QA.md")
			state := filepath.Join(dir, "state.json")
			s, _ := swarm.Open(state)
			ws := &workspaceService{store: s}
			h := &hosted{sessionID: "first", agent: "codex", cwd: dir}
			setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_document": map[string]string{"path": path}}
			if _, err := qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
				t.Fatal(err)
			}
			before := s.View(h.sessionID)
			// A directory prevents atomic replacement without relying on user privileges.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := qaFileCall(t, ws, h, "member_update", map[string]any{"qa_updates": []any{map[string]any{"id": "q", "action": "open", "package": "K5", "title": "Final", "body": "Newest durable evidence", "assignee": before.Lead}}}); err != nil {
				t.Fatal(err)
			}
			end, err := qaFileCall(t, ws, h, "workspace_end", map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			if end.(map[string]any)["qa_document_status"].(qaDocumentStatus).State != "error" {
				t.Fatal(end)
			}
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
			recovered, err := swarm.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			ws = &workspaceService{store: recovered}
			if reopen {
				h.sessionID = "next"
				if _, err = qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
					t.Fatal(err)
				}
				if len(recovered.View(h.sessionID).QA) != 1 {
					t.Fatal("new run lost unsaved history")
				}
				if recovered.Snapshot().Workspaces[0].QADocument != nil {
					t.Fatal("old run still owns projection")
				}
			} else {
				ws.syncQADocuments()
			}
			b, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(b), "Newest durable evidence") {
				t.Fatal(string(b), err)
			}
			if !reopen && ws.qaDocumentStatus(before).State != "saved" {
				t.Fatal("ended export did not retry")
			}
		})
	}
}
func TestQADocumentActiveOwnershipAndMalformedCheckpointAreAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "QA.md")
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "one", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_document": map[string]string{"path": path}}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	other := &hosted{sessionID: "two", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, other, "workspace_configure", setup); err == nil || s.View("two") != nil {
		t.Fatal("duplicate active writer")
	}
	bad := []byte("# History\n<!-- wash-qa-checkpoint-v1: broken -->\n")
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), bad, 0600); err != nil {
		t.Fatal(err)
	}
	setup["qa_document"] = map[string]string{"path": filepath.Join(dir, "broken.md")}
	if _, err := qaFileCall(t, ws, other, "workspace_configure", setup); err == nil || s.View("two") != nil {
		t.Fatal("damaged checkpoint committed")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "broken.md"))
	if string(b) != string(bad) {
		t.Fatal("damaged original overwritten")
	}
}

// A workspace whose orchestrator is not running (its reopen failed, or Wash
// restarted and nobody resumed it) held its QA file with no session able to
// end it, so a new orchestrator could not set up there. Any session other
// than a member ends it by ID; a running one it cannot touch.
func TestWorkspaceEndByIDEndsAStaleWorkspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "QA.md")
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	stale := &hosted{sessionID: "stale", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_document": map[string]string{"path": path}}
	if _, err := qaFileCall(t, ws, stale, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	old := s.View(stale.sessionID)
	next := &hosted{sessionID: "next", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, next, "workspace_configure", setup); err == nil || !strings.Contains(err.Error(), old.ID) {
		t.Fatalf("conflict does not name the holder: %v", err)
	}
	if _, err := qaFileCall(t, ws, next, "workspace_end", map[string]any{"workspace_id": old.ID[:4]}); err == nil {
		t.Fatal("ended by a short prefix")
	}
	hostedMu.Lock()
	hostedAll["stale"] = stale
	hostedMu.Unlock()
	if _, err := qaFileCall(t, ws, next, "workspace_end", map[string]any{"workspace_id": old.ID}); err == nil {
		t.Fatal("ended a running workspace")
	}
	hostedMu.Lock()
	delete(hostedAll, "stale")
	hostedMu.Unlock()
	if _, err := qaFileCall(t, ws, next, "workspace_end", map[string]any{"workspace_id": old.ID[:8]}); err != nil {
		t.Fatal(err)
	}
	if w := s.View(stale.sessionID); w != nil {
		t.Fatalf("stale workspace survived: %q", w.State)
	}
	if _, err := qaFileCall(t, ws, next, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
}
