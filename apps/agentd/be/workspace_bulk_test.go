package agentd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/internal/workspacemcp"
)

func TestBulkSetupPreviewRollbackAndStableMemberKeys(t *testing.T) {
	root := t.TempDir()
	s, err := swarm.Open(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "bulk-lead", agent: "codex", cwd: root}
	call := func(args any) (any, error) {
		raw, _ := json.Marshal(args)
		return ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: raw})
	}
	args := map[string]any{"workspace": map[string]string{"name": "Team"}, "preview": true, "members": map[string]any{"reviewer": map[string]any{"name": "Red", "lifetime": "resident", "instructions": "Review", "package": "K5", "role": "reviewer"}}, "plan": map[string]any{"items": map[string]any{"k5": map[string]string{"text": "Review K5", "state": "active"}}}}
	if _, err := call(args); err != nil {
		t.Fatal(err)
	}
	if s.View(h.sessionID) != nil {
		t.Fatal("preview created workspace")
	}
	args["preview"] = false
	args["plan"] = map[string]any{"items": map[string]any{"bad": map[string]string{"state": "bad"}}}
	if _, err := call(args); err == nil {
		t.Fatal("accepted invalid plan")
	}
	if s.View(h.sessionID) != nil {
		t.Fatal("partial setup committed")
	}
	delete(args, "members")
	args["plan"] = map[string]any{"items": map[string]any{"k5": map[string]string{"text": "Review K5"}}}
	args["request_id"] = "setup"
	if _, err := call(args); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	if _, err := call(args); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("setup retry mutated workspace")
	}
	// Reserved members are validated on preview, without a provider process.
	args = map[string]any{"preview": true, "members": map[string]any{"reviewer": map[string]any{"name": "Red", "lifetime": "resident", "instructions": "Review", "role": "reviewer", "package": "K5"}}}
	if _, err := call(args); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("member preview changed state")
	}
}
func TestCombinedMemberUpdateRollsBackAndQAReadback(t *testing.T) {
	s, _ := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	w, _ := s.Setup("lead", "codex", t.TempDir(), "Team", "", nil)
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex"}
	call := func(name, raw string) (any, error) {
		return ws.call(context.Background(), h, workspacemcp.Call{Name: name, Arguments: json.RawMessage(raw)})
	}
	before := s.Snapshot()
	if _, err := call("member_update", `{"status":"must roll back","qa_updates":[{"id":"missing","action":"reply","body":"oops"}]}`); err == nil {
		t.Fatal("invalid report succeeded")
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("partial report")
	}
	raw, _ := json.Marshal(map[string]any{"request_id": "q-open", "recipient": w.Lead, "type": "question", "body": "Which bound?", "qa": map[string]any{"id": "q", "action": "open", "package": "K5", "title": "Bound"}})
	if _, err := call("message_send", string(raw)); err != nil {
		t.Fatal(err)
	}
	q := s.View("lead").QA[0]
	if len(q.Events) != 1 {
		t.Fatal("opening duplicated QA text", q.Events)
	}
	before = s.Snapshot()
	if _, err := call("message_send", string(raw)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("opening retry changed history")
	}
	if _, err := call("workspace_get", `{"view":"qa","thread_id":"q","limit":1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call("workspace_get", `{"view":"qa","thread_id":"q","after":"missing"}`); err == nil {
		t.Fatal("invalid QA cursor")
	}
	state, err := call("workspace_get", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.(map[string]any)["workspace"].(*swarm.Workspace).QA[0].Events) != 0 {
		t.Fatal("general state included QA transcript")
	}
	if len(s.View("lead").QA[0].Events) != 1 {
		t.Fatal("summary erased durable events")
	}
}

func TestBulkRenamePreservesProjectRootAndBatchesRollback(t *testing.T) {
	root := t.TempDir()
	s, err := swarm.Open(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A project may be attached to a conversation launched from another directory.
	w, err := s.Setup("lead", "codex", root, "Project", filepath.Join(root, "project"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(w.Root, 0700); err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: root}
	call := func(name, raw string) (any, error) {
		return ws.call(context.Background(), h, workspacemcp.Call{Name: name, Arguments: json.RawMessage(raw)})
	}
	if _, err := call("workspace_configure", `{"workspace":{"name":"Renamed"}}`); err != nil {
		t.Fatal(err)
	}
	if got := s.View("lead"); got.Name != "Renamed" || got.Root != w.Root {
		t.Fatal(got)
	}
	before := s.Snapshot()
	raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"recipient": w.Lead, "type": "progress", "body": "must roll back"}, map[string]any{"recipient": "unknown", "type": "progress", "body": "invalid"}}})
	if _, err := call("message_send", string(raw)); err == nil {
		t.Fatal("accepted invalid batch")
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("batch partially committed")
	}
	if _, err := call("member_update", `{"qa_updates":[{"action":"open","id":"q","package":"K5","title":"Question","body":"Body","assignee":"`+w.Lead+`"}]}`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := call("decision_request", `{"text":"Choose A or B","thread_id":"q","request_id":"choice"}`); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.View("lead").QA[0].Events) != 2 {
		t.Fatal("decision retry duplicated QA")
	}
}
