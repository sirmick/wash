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
	if _, err = config(plan); err == nil {
		t.Fatal("claimed existing project file")
	}
	if s.View("lead") != nil {
		t.Fatal("invalid file committed setup")
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
