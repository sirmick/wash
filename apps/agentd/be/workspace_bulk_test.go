package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/agentpolicy"
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
	state, err := call("workspace_get", `{"view":"state"}`)
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

// The orchestrator answers the project-root folder question once, at setup.
// Paths inside that root, such as the plan, the QA file and member worktrees,
// must not ask again on later configure calls. Observed live: "Read
// …/redoubt-g1 (outside this session's folders)" on every launch.
func TestConfigureInsideApprovedRootDoesNotAsk(t *testing.T) {
	resetAsks()
	withState(t, 0) // nobody home: any ask comes back as a refusal
	withPolicy(t, agentpolicy.Policy{Enabled: true, Default: "ask"})
	base := t.TempDir()
	cwd, project := filepath.Join(base, "wash"), filepath.Join(base, "riscv")
	for _, d := range []string{cwd, filepath.Join(project, "docs"), filepath.Join(project, ".worktrees", "k5")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "docs", "PLAN.md"), []byte("# Plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := swarm.Open(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Setup("lead", "claude", cwd, "Project", project, nil); err != nil {
		t.Fatal(err)
	}
	// Set up directly rather than through workspace_configure, which would
	// have given the workspace the orchestrator's catalog.
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, _ *swarm.Member) error { w.Catalog = "anthropic"; return nil }); err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{key: "acp:lead", sessionID: "lead", agent: "claude", cwd: cwd}
	call := func(args any) error {
		raw, _ := json.Marshal(args)
		_, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: raw})
		return err
	}
	member := func(dir string) map[string]any {
		return map[string]any{"k5-implementer": map[string]any{"name": "K5", "lifetime": "resident", "instructions": "Implement", "cwd": dir}}
	}
	if err := call(map[string]any{"preview": true, "document": map[string]string{"path": filepath.Join(project, "docs", "PLAN.md")}, "qa_dir": filepath.Join(project, "docs", "qa"), "members": member(filepath.Join(project, ".worktrees", "k5"))}); err != nil {
		t.Fatalf("path inside the approved root asked: %v", err)
	}
	// Relative paths are the project root's, not the orchestrator's cwd.
	if err := call(map[string]any{"preview": true, "document": map[string]string{"path": "docs/PLAN.md"}, "qa_dir": "docs/qa"}); err != nil {
		t.Fatalf("relative paths under the project root: %v", err)
	}
	// Outside the root is still a question — here refused, as nobody is home.
	if err := call(map[string]any{"preview": true, "members": member(base)}); err == nil {
		t.Fatal("a member cwd outside the project root was accepted without asking")
	}
}

// With titled packages a member's name is its role, so names repeat across
// packages; they must still be unique within one.
func TestMemberNamesAreUniquePerPackage(t *testing.T) {
	root := t.TempDir()
	s, err := swarm.Open(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "claude", cwd: root}
	call := func(members map[string]any) error {
		raw, _ := json.Marshal(map[string]any{"workspace": map[string]string{"name": "Team"}, "preview": true, "members": members})
		_, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: raw})
		return err
	}
	member := func(pkg string) map[string]any {
		return map[string]any{"name": "Red team", "package": pkg, "role": "reviewer", "lifetime": "resident", "instructions": "Review"}
	}
	if err := call(map[string]any{"CT1-red": member("CT1"), "G1-red": member("G1")}); err != nil {
		t.Fatalf("same role name in two packages refused: %v", err)
	}
	if _, err := s.Setup("lead", "claude", root, "Team", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, _ *swarm.Member) error {
		w.Members = append(w.Members, swarm.Member{ID: "x", Key: "CT1-red", Name: "Red team", Package: "CT1", State: "available", Lifetime: "resident"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"preview": true, "members": map[string]any{"CT1-red2": member("CT1")}})
	if _, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: raw}); err == nil {
		t.Fatal("duplicate name within one package accepted")
	}
}

// Observed in Redoubt: the human answered the Architect's decision in the
// member pane's message box. That sent a plain instruction, left the decision
// recorded, and pinned its QA thread at awaiting-owner, where resolve refuses.
func TestMessagingAMemberAnswersItsPendingDecision(t *testing.T) {
	s, err := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Setup("lead", "codex", t.TempDir(), "Team", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	lead := &hosted{sessionID: "lead"}
	call := func(name, raw string) (any, error) {
		return ws.call(context.Background(), lead, workspacemcp.Call{Name: name, Arguments: json.RawMessage(raw)})
	}
	if _, err = call("member_update", `{"qa_updates":[{"action":"open","id":"q","package":"R2","title":"Stub address","body":"Where?","assignee":"`+w.Lead+`"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err = call("decision_request", `{"text":"Fixed address or relocatable?","thread_id":"q"}`); err != nil {
		t.Fatal(err)
	}
	if got := s.View("lead").QA[0].State; got != "awaiting-owner" {
		t.Fatalf("thread %s, want awaiting-owner", got)
	}
	raw, _ := json.Marshal(map[string]any{"recipient": w.Lead, "body": "Fixed address."})
	res, err := ws.humanMessage(lead, raw)
	if err != nil || res.(map[string]any)["answered"] == nil {
		t.Fatal("message did not answer the pending decision", res, err)
	}
	v := s.View("lead")
	if v.QA[0].State != "open" {
		t.Fatalf("thread %s after the answer, want open", v.QA[0].State)
	}
	last := v.QA[0].Events[len(v.QA[0].Events)-1]
	if last.Kind != "decision_response" || last.Body != "Fixed address." {
		t.Fatalf("answer not in the thread: %+v", last)
	}
	rev := v.QA[0].Revision
	if _, err = call("member_update", fmt.Sprintf(`{"qa_updates":[{"action":"resolve","id":"q","expected_revision":%d,"evidence":"Human chose a fixed address."}]}`, rev)); err != nil {
		t.Fatal("thread still cannot be resolved:", err)
	}
	// With nothing pending, a message is an ordinary instruction again.
	res, err = ws.humanMessage(lead, raw)
	if err != nil || res.(map[string]any)["answered"] != nil {
		t.Fatal("a plain message was taken as a decision answer", res, err)
	}
}

// A member's tier resolves against the orchestrator's stack when its key is
// reserved, and a member with neither tier nor profile launches through the
// orchestrator's connection. PATH is emptied so no adapter can start: the
// reservation is what is under test.
func TestMemberTierResolvesFromTheOrchestratorsStack(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	withPolicy(t, agentpolicy.Policy{})
	root := t.TempDir()
	s, err := swarm.Open(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "opencode", connection: "opencode@openrouter", catalog: "openrouter-budget", cwd: root}
	call := func(args string) error {
		_, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: json.RawMessage(args)})
		return err
	}
	member := func(key, extra string) string {
		return fmt.Sprintf(`"%s":{"name":%q,"lifetime":"resident","instructions":"Wait for assignments."%s}`, key, key, extra)
	}
	if err := call(`{"workspace":{"name":"Team"},"members":{` + member("rev", `,"model":"small"`) + `,` + member("impl", `,"model":"coding","effort":"max"`) + `,` + member("plain", "") + `,` + member("own", `,"catalog":"anthropic","model":"haiku"`) + `}}`); err != nil {
		t.Fatal(err)
	}
	w := s.View("lead")
	if w.Catalog != "openrouter-budget" {
		t.Fatalf("catalog = %q, want the orchestrator's", w.Catalog)
	}
	if lead := swarm.GetMember(w, w.Lead); lead.LaunchSettings == nil || lead.LaunchSettings.Connection != "opencode@openrouter" {
		t.Fatalf("orchestrator launch settings = %+v", lead.LaunchSettings)
	}
	want := builtinCatalogs["openrouter-budget"].Slots["small"]
	rev := swarm.GetMember(w, "rev")
	if rev.Model != "small" || rev.Catalog != "openrouter-budget" || rev.LaunchSettings.Model != want.Model || rev.LaunchSettings.Connection != want.Connection || rev.Provider != "opencode" {
		t.Fatalf("reviewer member = %q %+v", rev.Model, rev.LaunchSettings)
	}
	if impl := swarm.GetMember(w, "impl"); impl.LaunchSettings.Model != builtinCatalogs["openrouter-budget"].Slots["coding"].Model || impl.LaunchSettings.Effort != "max" {
		t.Fatalf("explicit effort did not override the slot: %+v", impl.LaunchSettings)
	}
	// No model: the catalog's default slot.
	if plain := swarm.GetMember(w, "plain"); plain.LaunchSettings.Provider != "opencode" || plain.LaunchSettings.Connection != "opencode@openrouter" || plain.LaunchSettings.Model != builtinCatalogs["openrouter-budget"].Slots["frontier"].Model {
		t.Fatalf("member without a model = %+v", plain.LaunchSettings)
	}
	// A member may name its own catalog, and a model id on it.
	if own := swarm.GetMember(w, "own"); own.Catalog != "anthropic" || own.LaunchSettings.Provider != "claude" || own.LaunchSettings.Model != "haiku" || own.LaunchSettings.Connection != "" {
		t.Fatalf("member on its own catalog = %+v", own.LaunchSettings)
	}
	// A member reads its role and task, never its model.
	if brief := memberBrief(*rev, "a1"); strings.Contains(brief, rev.LaunchSettings.Model) {
		t.Fatal("the member brief carries a model string")
	}

	// Another catalog affects later launches only.
	if err := call(`{"catalog":"anthropic-pro","members":{` + member("rev2", `,"model":"coding"`) + `}}`); err != nil {
		t.Fatal(err)
	}
	w = s.View("lead")
	if rev2 := swarm.GetMember(w, "rev2"); rev2.LaunchSettings.Provider != "claude" || rev2.LaunchSettings.Model != "opus[1m]" {
		t.Fatalf("after the catalog change = %+v", rev2.LaunchSettings)
	}
	if swarm.GetMember(w, "rev").LaunchSettings.Provider != "opencode" {
		t.Fatal("a catalog change rewrote an existing member")
	}

	for name, args := range map[string]string{
		"provider off the catalog": `{"members":{` + member("odd", `,"model":"coding","provider":"codex"`) + `}}`,
		"unknown catalog":          `{"catalog":"nope"}`,
		"unknown member catalog":   `{"members":{` + member("odd2", `,"catalog":"nope"`) + `}}`,
	} {
		if err := call(args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Without a catalog there is nothing for a member's model to name.
func TestMemberModelNeedsAWorkspaceCatalog(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	withPolicy(t, agentpolicy.Policy{})
	root := t.TempDir()
	s, _ := swarm.Open(filepath.Join(root, "state.json"))
	ws := &workspaceService{store: s}
	// A launcher on a connection no catalog lists: nothing to resolve a
	// member's model against.
	h := &hosted{sessionID: "lead", agent: "codex", connection: "codex@nowhere", cwd: root}
	_, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: json.RawMessage(`{"workspace":{"name":"Team"},"members":{"a":{"name":"A","lifetime":"resident","instructions":"x","model":"coding"}}}`)})
	if err == nil || !strings.Contains(err.Error(), "no catalog") {
		t.Fatalf("err = %v", err)
	}
}

// Tally shakedown finding 1: members given no cwd launched in the
// orchestrator's folder, not project_root, so an implementer edited the
// orchestrator's repository. A relative cwd is inside the project too.
func TestMembersDefaultToTheProjectRoot(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	withPolicy(t, agentpolicy.Policy{})
	base := t.TempDir()
	cwd, project := filepath.Join(base, "orchestrator"), filepath.Join(base, "project")
	for _, d := range []string{cwd, filepath.Join(project, "sub")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := swarm.Open(filepath.Join(base, "state.json"))
	if _, err := s.Setup("lead", "claude", cwd, "Project", project, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, _ *swarm.Member) error { w.Catalog = "anthropic"; return nil }); err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "claude", cwd: cwd}
	_, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: json.RawMessage(`{"members":{"a":{"name":"A","lifetime":"resident","instructions":"x"},"b":{"name":"B","lifetime":"resident","instructions":"x","cwd":"sub"}}}`)})
	if err != nil {
		t.Fatal(err)
	}
	w := s.View("lead")
	if got := swarm.GetMember(w, "a").Cwd; got != project {
		t.Fatalf("member without cwd runs in %s, want the project root %s", got, project)
	}
	if got := swarm.GetMember(w, "b").Cwd; got != filepath.Join(project, "sub") {
		t.Fatalf("relative cwd = %s", got)
	}
}

// Tally shakedown findings 7, 12, 14 and 15, all before any process runs:
// preview reports no ids it would not keep, a bad member names itself and
// the field, a slot on an adapter's own list fails in preview, and a failed
// launch's key and name take a corrected definition.
func TestConfigureReportsWhatTheOrchestratorCanActOn(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // every launch fails
	withPolicy(t, agentpolicy.Policy{})
	root := t.TempDir()
	s, _ := swarm.Open(filepath.Join(root, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "claude", catalog: "anthropic", cwd: root}
	call := func(args string) (any, error) {
		return ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: json.RawMessage(args)})
	}
	res, err := call(`{"workspace":{"name":"Team"},"preview":true,"members":{"a":{"name":"A","lifetime":"resident","instructions":"x","model":"haiku"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	preview := res.(map[string]any)
	if preview["workspace_id"] != "" || preview["members"].(map[string]string)["a"] != "" {
		t.Fatalf("preview reported ids the commit will not use: %v", preview)
	}

	_, err = call(`{"workspace":{"name":"Team"},"preview":true,"members":{"probe":{"name":"P","lifetime":"ephemeral","instructions":"x"}}}`)
	if err == nil || !strings.Contains(err.Error(), "member probe") || !strings.Contains(err.Error(), "task") {
		t.Fatalf("ephemeral without task: %v", err)
	}
	_, err = call(`{"workspace":{"name":"Team"},"preview":true,"members":{"a":{"name":"A","lifetime":"resident","instructions":"x","model":"coding"}}}`)
	if err == nil || !strings.Contains(err.Error(), "no slots") || !strings.Contains(err.Error(), "anthropic-budget") {
		t.Fatalf("slot on an adapter's own list passed preview: %v", err)
	}

	if _, err = call(`{"workspace":{"name":"Team"},"members":{"a":{"name":"A","lifetime":"resident","instructions":"x","model":"haiku"}}}`); err != nil {
		t.Fatal(err)
	}
	failed := swarm.GetMember(s.View("lead"), "a")
	if failed.State != "failed" || failed.Session != "" {
		t.Fatalf("launch with no adapter on PATH: %+v", failed)
	}
	// The same key, a different model: the definition is replaced in place.
	if _, err = call(`{"members":{"a":{"name":"A","lifetime":"resident","instructions":"x","model":"sonnet"}}}`); err != nil {
		t.Fatalf("a failed launch's key refused a corrected definition: %v", err)
	}
	w := s.View("lead")
	if again := swarm.GetMember(w, "a"); again.ID != failed.ID || again.LaunchSettings.Model != "sonnet" {
		t.Fatalf("redefined member = %+v", again)
	}
	// A new key may take a failed member's name.
	if _, err = call(`{"members":{"a2":{"name":"A","lifetime":"resident","instructions":"x"}}}`); err != nil {
		t.Fatalf("a failed launch held its name: %v", err)
	}
}

// Tally shakedown findings 16 and 19: a caller's assignment id was dropped
// without a word, and a status set before waiting outlived it.
func TestAssignmentIDsAndWaitingStatus(t *testing.T) {
	s, _ := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	w, _ := s.Setup("lead", "codex", t.TempDir(), "Team", "", nil)
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, _ *swarm.Member) error {
		w.Members = append(w.Members, swarm.Member{ID: "m", Key: "rec", Session: "m-session", Name: "Rec", State: "available", Lifetime: "resident"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	lead := &hosted{sessionID: "lead"}
	call := func(name, raw string) (any, error) {
		return ws.call(context.Background(), lead, workspacemcp.Call{Name: name, Arguments: json.RawMessage(raw)})
	}
	_, err := call("assignment_update", `{"updates":[{"action":"create","id":"rec-fix","member_id":"rec","text":"Fix"}]}`)
	if err == nil || !strings.Contains(err.Error(), "create takes no id") {
		t.Fatalf("caller id: %v", err)
	}
	if _, err = call("member_update", `{"status":"waiting on REC fix"}`); err != nil {
		t.Fatal(err)
	}
	if _, err = call("member_update", `{"waiting":{"reason":"idle"}}`); err != nil {
		t.Fatal(err)
	}
	if m := swarm.GetMember(s.View("lead"), w.Lead); m.Status != "" || m.Waiting != "idle" {
		t.Fatalf("status %q waiting %q", m.Status, m.Waiting)
	}
	if _, err = call("member_update", `{"status":"reviewing","waiting":{"reason":"on the reviewer"}}`); err != nil {
		t.Fatal(err)
	}
	if m := swarm.GetMember(s.View("lead"), w.Lead); m.Status != "reviewing" {
		t.Fatalf("status set with waiting was dropped: %q", m.Status)
	}
}
