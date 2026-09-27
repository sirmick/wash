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

	"github.com/sirmick/wash/internal/agentproto"
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

func readThread(t *testing.T, dir, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestQADirWritesOneFilePerThreadAndRecovers(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	qa := filepath.Join(dir, ".wash", "qa")
	s, err := swarm.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_dir": ".wash/qa", "request_id": "setup", "preview": true}
	if _, err = qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(qa); !os.IsNotExist(err) {
		t.Fatal("preview created the QA directory", err)
	}
	delete(setup, "preview")
	if _, err = qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	if got := s.View("lead").QADir; got != qa {
		t.Fatalf("qa_dir = %q, want %q", got, qa)
	}
	for _, id := range []string{"q", "other"} {
		open := map[string]any{"recipient": "orchestrator", "type": "question", "body": "Question " + id, "qa": map[string]any{"id": id, "action": "open", "package": "K5", "title": "Question " + id}}
		if _, err = qaFileCall(t, ws, h, "message_send", open); err != nil {
			t.Fatal(err)
		}
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
	text := readThread(t, qa, "q")
	for i := 0; i < 40; i++ {
		if !strings.Contains(text, fmt.Sprintf("Evidence [%d]", i)) {
			t.Fatal("lost reply", i)
		}
	}
	if !strings.HasPrefix(text, swarm.QAFileMarker("q")+"\n") || !strings.Contains(text, "Question q") || strings.Contains(text, "Evidence [0]") && strings.Contains(readThread(t, qa, "other"), "Evidence") {
		t.Fatal("thread files mixed up", text)
	}
	// Only the thread that changed is rewritten.
	before, _ := os.Stat(filepath.Join(qa, "other.md"))
	result, err := qaFileCall(t, ws, h, "decision_request", map[string]any{"text": "Choose A or B", "thread_id": "q"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": result.(map[string]any)["id"], "body": "Use A"})
	if _, err = ws.answer(h, raw); err != nil {
		t.Fatal(err)
	}
	if text = readThread(t, qa, "q"); !strings.Contains(text, "Owner · Owner decision") || !strings.Contains(text, "Use A") {
		t.Fatal(text)
	}
	after, _ := os.Stat(filepath.Join(qa, "other.md"))
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("an unchanged thread was rewritten")
	}
	// A restarted backend rebuilds a missing file from the store alone.
	if err = os.Remove(filepath.Join(qa, "q.md")); err != nil {
		t.Fatal(err)
	}
	recovered, err := swarm.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	ws = &workspaceService{store: recovered}
	ws.syncQADocuments()
	if readThread(t, qa, "q") != text {
		t.Fatal("recovery changed QA history")
	}
	if _, err = qaFileCall(t, ws, h, "workspace_configure", map[string]any{"qa_dir": nil}); err != nil {
		t.Fatal(err)
	}
	if _, err = qaFileCall(t, ws, h, "member_update", map[string]any{"qa_updates": []any{map[string]any{"id": "q", "action": "reply", "body": "After detaching"}}}); err != nil {
		t.Fatal(err)
	}
	if readThread(t, qa, "q") != text {
		t.Fatal("detached directory modified")
	}
}

func TestQADirLeavesOtherFilesAloneAndReportsWriteFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := swarm.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	if err = os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# Human notes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_dir": "notes.md"}); err == nil {
		t.Fatal("accepted a file as the QA directory")
	}
	// A directory with someone's Markdown in it is usable; their files are not read or written.
	if err = os.WriteFile(filepath.Join(dir, "q.md"), []byte("# Not Wash's"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_dir": "."}); err != nil {
		t.Fatal(err)
	}
	result, err := qaFileCall(t, ws, h, "message_send", map[string]any{"recipient": "orchestrator", "type": "question", "body": "Retain this question", "qa": map[string]any{"id": "q", "action": "open", "package": "K5", "title": "Question"}})
	if err != nil {
		t.Fatal(err)
	}
	if st := result.(map[string]any)["qa_document_status"].(agentproto.QADocumentStatus); st.State != "error" || !strings.Contains(st.Error, "not a Wash QA thread file") {
		t.Fatal(st)
	}
	if len(s.View("lead").QA) != 1 {
		t.Fatal("lost durable question")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "q.md")); string(b) != "# Not Wash's" {
		t.Fatal("overwrote someone's file")
	}
	// A symlink is not written through either.
	if err = os.Remove(filepath.Join(dir, "q.md")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(dir, "notes.md"), filepath.Join(dir, "q.md")); err != nil {
		t.Fatal(err)
	}
	ws.syncQADocuments()
	if ws.qaDocumentStatus(s.View("lead")).State != "error" {
		t.Fatal("wrote through a symlink")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "notes.md")); string(b) != "# Human notes" {
		t.Fatal("symlink target changed")
	}
	if err = os.Remove(filepath.Join(dir, "q.md")); err != nil {
		t.Fatal(err)
	}
	ws.syncQADocuments()
	if ws.qaDocumentStatus(s.View("lead")).State != "saved" {
		t.Fatal("files did not recover")
	}
	if !strings.Contains(readThread(t, dir, "q"), "Retain this question") {
		t.Fatal("recovered file lacks the question")
	}
}

func TestQADirResumesWithoutStoreAndRetainsDecisions(t *testing.T) {
	dir := t.TempDir()
	s, _ := swarm.Open(filepath.Join(dir, "first.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "first", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_dir": "qa"}
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
	// A completely separate backend has only the thread files.
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
// workspace stayed paused with no one able to lead it, holding the QA
// directory, so a new orchestrator's setup failed with "QA directory is used
// by another workspace". A lead whose session never loaded (a failed reopen)
// keeps its workspace for a later resume.
func TestEndingTheLeadSessionEndsTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	s, _ := swarm.Open(state)
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "first", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_dir": "qa"}
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

// An ended workspace keeps retrying a failed final write until a new
// workspace takes the directory over.
func TestQADirFinalFailureRetriesUntilClaimed(t *testing.T) {
	dir := t.TempDir()
	qa := filepath.Join(dir, "qa")
	state := filepath.Join(dir, "state.json")
	s, _ := swarm.Open(state)
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "first", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_dir": "qa"}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	before := s.View(h.sessionID)
	// A directory in the file's place prevents the atomic replacement.
	if err := os.Mkdir(filepath.Join(qa, "q.md"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := qaFileCall(t, ws, h, "member_update", map[string]any{"qa_updates": []any{map[string]any{"id": "q", "action": "open", "package": "K5", "title": "Final", "body": "Newest durable evidence", "assignee": before.Lead}}}); err != nil {
		t.Fatal(err)
	}
	end, err := qaFileCall(t, ws, h, "workspace_end", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if end.(map[string]any)["qa_document_status"].(agentproto.QADocumentStatus).State != "error" {
		t.Fatal(end)
	}
	if err = os.Remove(filepath.Join(qa, "q.md")); err != nil {
		t.Fatal(err)
	}
	recovered, err := swarm.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	ws = &workspaceService{store: recovered}
	ws.syncQADocuments()
	if !strings.Contains(readThread(t, qa, "q"), "Newest durable evidence") || ws.qaDocumentStatus(before).State != "saved" {
		t.Fatal("ended export did not retry")
	}
	h.sessionID = "next"
	if _, err = qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	if recovered.Snapshot().Workspaces[0].QADir != "" {
		t.Fatal("the ended workspace still writes the directory")
	}
	if len(recovered.View("next").QA) != 1 {
		t.Fatal("new workspace did not resume the thread")
	}
}

func TestQADirActiveOwnershipAndDamagedCheckpointAreAtomic(t *testing.T) {
	dir := t.TempDir()
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "one", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_dir": "qa"}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	other := &hosted{sessionID: "two", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, other, "workspace_configure", setup); err == nil || s.View("two") != nil {
		t.Fatal("duplicate active writer")
	}
	broken := filepath.Join(dir, "broken")
	if err := os.Mkdir(broken, 0700); err != nil {
		t.Fatal(err)
	}
	bad := []byte(swarm.QAFileMarker("x") + "\n## x\n<!-- wash-qa-checkpoint-v2: broken -->\n")
	if err := os.WriteFile(filepath.Join(broken, "x.md"), bad, 0600); err != nil {
		t.Fatal(err)
	}
	setup["qa_dir"] = "broken"
	if _, err := qaFileCall(t, ws, other, "workspace_configure", setup); err == nil || !strings.Contains(err.Error(), "x.md") || s.View("two") != nil {
		t.Fatalf("damaged checkpoint committed: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(broken, "x.md")); string(b) != string(bad) {
		t.Fatal("damaged original overwritten")
	}
}

// A workspace whose orchestrator is not running (its reopen failed, or Wash
// restarted and nobody resumed it) held its QA directory with no session able
// to end it, so a new orchestrator could not set up there. Any session other
// than a member ends it by ID; a running one it cannot touch.
func TestWorkspaceEndByIDEndsAStaleWorkspace(t *testing.T) {
	dir := t.TempDir()
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	stale := &hosted{sessionID: "stale", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_dir": "qa"}
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

// A resolved thread comes back as a header only: the new orchestrator's, marked
// resumed, its events left in its file until something reads or reopens it.
func TestResumedResolvedThreadIsAHeaderUntilReopened(t *testing.T) {
	dir := t.TempDir()
	qa := filepath.Join(dir, "qa")
	s, _ := swarm.Open(filepath.Join(dir, "first.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "first", agent: "codex", cwd: dir}
	setup := map[string]any{"workspace": map[string]string{"name": "Project"}, "qa_dir": "qa"}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	if _, err := qaFileCall(t, ws, h, "message_send", map[string]any{"recipient": "orchestrator", "type": "question", "body": "Overflow in " + dir + "/record.go?", "qa": map[string]any{"id": "q", "action": "open", "package": "REC", "title": "Overflow"}}); err != nil {
		t.Fatal(err)
	}
	rev := s.View(h.sessionID).QA[0].Revision
	if _, err := qaFileCall(t, ws, h, "member_update", map[string]any{"qa_updates": []any{map[string]any{"id": "q", "action": "resolve", "expected_revision": rev, "evidence": "fixed"}}}); err != nil {
		t.Fatal(err)
	}
	if text := readThread(t, qa, "q"); strings.Contains(text, dir) || !strings.Contains(text, "./record.go") {
		t.Fatal("the thread file names the project's absolute path:", text)
	}
	if _, err := qaFileCall(t, ws, h, "workspace_end", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	written := readThread(t, qa, "q")
	s2, _ := swarm.Open(filepath.Join(dir, "second.json"))
	ws2 := &workspaceService{store: s2}
	h2 := &hosted{sessionID: "second", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws2, h2, "workspace_configure", setup); err != nil {
		t.Fatal(err)
	}
	w := s2.View(h2.sessionID)
	q := w.QA[0]
	if q.State != "resolved" || q.Assignee != w.Lead || !q.Resumed || !q.Archived || len(q.Events) != 0 {
		t.Fatalf("resumed thread = %+v", q)
	}
	if md := swarm.QAMarkdown(w); !strings.Contains(md, "earlier workspace") {
		t.Fatal("the QA view does not say the resolution is from an earlier workspace")
	}
	ws2.syncQADocuments()
	if readThread(t, qa, "q") != written {
		t.Fatal("a header-only thread overwrote its file")
	}
	read, err := qaFileCall(t, ws2, h2, "workspace_get", map[string]any{"view": "qa", "thread_id": "q"})
	if err != nil {
		t.Fatal(err)
	}
	if events := read.(map[string]any)["thread"].(swarm.QAThread).Events; len(events) < 2 || !strings.Contains(events[0].Body, "./record.go") {
		t.Fatalf("events not read from the file: %+v", events)
	}
	if _, err := qaFileCall(t, ws2, h2, "member_update", map[string]any{"qa_updates": []any{map[string]any{"id": "q", "action": "reopen", "expected_revision": q.Revision, "body": "code changed"}}}); err != nil {
		t.Fatal(err)
	}
	if q := s2.View(h2.sessionID).QA[0]; q.State != "open" || q.Assignee != w.Lead || q.Resumed || q.Archived || len(q.Events) < 3 {
		t.Fatalf("reopened thread = %+v", q)
	}
	ws2.syncQADocuments()
	if text := readThread(t, qa, "q"); !strings.Contains(text, "code changed") || !strings.Contains(text, "Overflow in") {
		t.Fatal("the reopened thread's file lost its history:", text)
	}
}

// DOC1: members pasted 800-line plans into threads, because nothing stopped
// them; the QA file reached 2 MB. A thread body is a pointer.
func TestQABodiesAreCapped(t *testing.T) {
	dir := t.TempDir()
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Project"}}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", swarm.ReportLimit+1)
	if _, err := qaFileCall(t, ws, h, "message_send", map[string]any{"recipient": "orchestrator", "type": "question", "body": long, "qa": map[string]any{"id": "q", "action": "open", "package": "K5", "title": "Plan"}}); err == nil || !strings.Contains(err.Error(), "file") {
		t.Fatalf("a long thread message was accepted: %v", err)
	}
	if _, err := qaFileCall(t, ws, h, "member_update", map[string]any{"qa_updates": []any{map[string]any{"id": "q", "action": "open", "package": "K5", "title": "Plan", "body": long, "assignee": "orchestrator"}}}); err == nil || !strings.Contains(err.Error(), "file") {
		t.Fatalf("a long thread body was accepted: %v", err)
	}
}
