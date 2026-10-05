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

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/agentproto"
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
	args := map[string]any{"workspace": map[string]string{"name": "Team"}, "preview": true, "members": map[string]any{"reviewer": map[string]any{"name": "Red", "lifetime": "resident", "instructions": "Review", "role": "reviewer"}}}
	if _, err := call(args); err != nil {
		t.Fatal(err)
	}
	if s.View(h.sessionID) != nil {
		t.Fatal("preview created workspace")
	}
	// A member on a node the plan does not have fails, and nothing commits.
	args["preview"] = false
	args["members"] = map[string]any{"reviewer": map[string]any{"name": "Red", "lifetime": "resident", "instructions": "Review", "role": "reviewer", "node": "K5"}}
	if _, err := call(args); err == nil || !strings.Contains(err.Error(), "plan_set") {
		t.Fatalf("accepted a member on a missing node: %v", err)
	}
	if s.View(h.sessionID) != nil {
		t.Fatal("partial setup committed")
	}
	delete(args, "members")
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
	raw, _ := json.Marshal(map[string]any{"nodes": map[string]any{"K5": map[string]any{"title": "Review K5"}}})
	if _, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "plan_set", Arguments: raw}); err != nil {
		t.Fatal(err)
	}
	before = s.Snapshot()
	// Reserved members are validated on preview, without a provider process.
	args = map[string]any{"preview": true, "members": map[string]any{"reviewer": map[string]any{"name": "Red", "lifetime": "resident", "instructions": "Review", "role": "reviewer", "node": "K5"}}}
	if _, err := call(args); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("member preview changed state")
	}
}
func TestCombinedMemberUpdateRollsBackAndQAReadback(t *testing.T) {
	s, _ := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	w, _ := s.Setup("lead", "codex", t.TempDir(), "Team", "")
	seedPlan(t, s, "lead", "K5")
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
	raw, _ := json.Marshal(map[string]any{"request_id": "q-open", "recipient": w.Lead, "type": "question", "body": "Which bound?", "qa": map[string]any{"id": "q", "action": "open", "node": "K5", "title": "Bound"}})
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
	w, err := s.Setup("lead", "codex", root, "Project", filepath.Join(root, "project"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(w.Root, 0700); err != nil {
		t.Fatal(err)
	}
	seedPlan(t, s, "lead", "K5")
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
	if _, err := call("member_update", `{"qa_updates":[{"action":"open","id":"q","node":"K5","title":"Question","body":"Body","assignee":"`+w.Lead+`"}]}`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := call("decision_request", `{"questions":[{"question":"Choose A or B","options":[{"label":"A"},{"label":"B"}]}],"thread_id":"q","request_id":"choice"}`); err != nil {
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
	if _, err := s.Setup("lead", "claude", cwd, "Project", project); err != nil {
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
	if err := call(map[string]any{"preview": true, "plan_file": filepath.Join(project, "docs", "plan.toml"), "qa_dir": filepath.Join(project, "docs", "qa"), "members": member(filepath.Join(project, ".worktrees", "k5"))}); err != nil {
		t.Fatalf("path inside the approved root asked: %v", err)
	}
	// Relative paths are the project root's, not the orchestrator's cwd.
	if err := call(map[string]any{"preview": true, "plan_file": "docs/plan.toml", "qa_dir": "docs/qa"}); err != nil {
		t.Fatalf("relative paths under the project root: %v", err)
	}
	// Outside the root is still a question — here refused, as nobody is home.
	if err := call(map[string]any{"preview": true, "members": member(base)}); err == nil {
		t.Fatal("a member cwd outside the project root was accepted without asking")
	}
}

// With titled packages a member's name is its role, so names repeat across
// packages; they must still be unique within one.
func TestMemberNamesAreUniquePerNode(t *testing.T) {
	root := t.TempDir()
	s, err := swarm.Open(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "claude", cwd: root}
	raw, _ := json.Marshal(map[string]any{"workspace": map[string]string{"name": "Team"}})
	if _, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: raw}); err != nil {
		t.Fatal(err)
	}
	seedPlan(t, s, "lead", "CT1", "G1")
	call := func(members map[string]any) error {
		raw, _ := json.Marshal(map[string]any{"preview": true, "members": members})
		_, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: raw})
		return err
	}
	member := func(node string) map[string]any {
		return map[string]any{"name": "Red team", "node": node, "role": "reviewer", "lifetime": "resident", "instructions": "Review"}
	}
	if err := call(map[string]any{"CT1-red": member("CT1"), "G1-red": member("G1")}); err != nil {
		t.Fatalf("same role name on two nodes refused: %v", err)
	}
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, _ *swarm.Member) error {
		w.Members = append(w.Members, swarm.Member{ID: "x", Key: "CT1-red", Name: "Red team", Node: "CT1", State: "available", Lifetime: "resident"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := call(map[string]any{"CT1-red2": member("CT1")}); err == nil {
		t.Fatal("duplicate name on one node accepted")
	}
}

// A decision blocks its asker: nothing else reaches it until the owner
// answers, the answers arrive as its next message, and they are in the QA
// thread word for word, so the thread can be resolved.
func TestADecisionBlocksItsAskerUntilAnswered(t *testing.T) {
	s, err := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Setup("lead", "codex", t.TempDir(), "Team", "")
	if err != nil {
		t.Fatal(err)
	}
	seedPlan(t, s, "lead", "R2")
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, m *swarm.Member) error {
		w.Members = append(w.Members, swarm.Member{ID: "arch", Name: "Architect", Role: "architect", Session: "arch-s", State: "available", Lifetime: "resident", Creator: m.ID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	arch := &hosted{sessionID: "arch-s"}
	call := func(h *hosted, name, raw string) (any, error) {
		return ws.call(context.Background(), h, workspacemcp.Call{Name: name, Arguments: json.RawMessage(raw)})
	}
	if _, err = call(arch, "member_update", `{"qa_updates":[{"action":"open","id":"q","node":"R2","title":"Stub address","body":"Where?","assignee":"`+w.Lead+`"}]}`); err != nil {
		t.Fatal(err)
	}
	res, err := call(arch, "decision_request", `{"title":"Stub address","questions":[{"id":"where","question":"Fixed address or relocatable?","options":[{"label":"Fixed"},{"label":"Relocatable"}],"recommended":"Fixed"},{"question":"Anything else?"}],"thread_id":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	id := res.(map[string]any)["id"].(string)
	if !strings.Contains(res.(map[string]any)["instruction"].(string), "End your turn") {
		t.Fatal(res)
	}
	if got := s.View("lead").QA[0].State; got != "awaiting-owner" {
		t.Fatalf("thread %s, want awaiting-owner", got)
	}
	notes := 0
	for _, m := range s.View("lead").Messages {
		if m.Type == "note" && m.Recipient == w.Lead && strings.Contains(m.Body, "Architect asked the owner") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatal("the orchestrator was not told the Architect waits on the owner")
	}
	// The orchestrator's instruction waits behind the question.
	if _, err := s.Send("lead", "arch", "instruction", "Also review K5", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if next, _ := s.Next("arch-s"); len(next) != 0 {
		t.Fatalf("delivered while the asker waits for the owner: %+v", next)
	}
	if err := s.AnswerDecision(id, map[string]swarm.QuestionAnswer{"where": {Selected: []string{"Fixed"}, Text: "at 0x8000"}, "q2": {}}, false); err != nil {
		t.Fatal(err)
	}
	next, _ := s.Next("arch-s")
	if len(next) != 2 || next[0].Type != "decision_response" || !strings.Contains(next[0].Body, "Fixed — at 0x8000") || !strings.Contains(next[0].Body, "(skipped)") || next[1].Body != "Also review K5" {
		t.Fatalf("the answer does not lead the next turn: %+v", next)
	}
	v := s.View("lead")
	last := v.QA[0].Events[len(v.QA[0].Events)-1]
	if v.QA[0].State != "open" || last.Kind != "decision_response" || !strings.Contains(last.Body, "Fixed — at 0x8000") {
		t.Fatalf("answer not in the thread: %s %+v", v.QA[0].State, last)
	}
	if err := s.AnswerDecision(id, nil, false); err == nil {
		t.Fatal("answered twice")
	}
	if err := s.AnswerDecision("nope", nil, true); err == nil {
		t.Fatal("answered an unknown decision")
	}
	if _, err := call(arch, "decision_request", `{"questions":[{"question":"Pick","options":[{"label":"A"}],"recommended":"B"}]}`); err == nil {
		t.Fatal("a recommendation that is not an option was accepted")
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
	want := builtinLaunch.Catalogs["openrouter-budget"].Slots["small"]
	rev := swarm.GetMember(w, "rev")
	if rev.Model != "small" || rev.Catalog != "openrouter-budget" || rev.LaunchSettings.Model != want.Model || rev.LaunchSettings.Connection != want.Connection || rev.Provider != "opencode" {
		t.Fatalf("reviewer member = %q %+v", rev.Model, rev.LaunchSettings)
	}
	if impl := swarm.GetMember(w, "impl"); impl.LaunchSettings.Model != builtinLaunch.Catalogs["openrouter-budget"].Slots["coding"].Model || impl.LaunchSettings.Effort != "max" {
		t.Fatalf("explicit effort did not override the slot: %+v", impl.LaunchSettings)
	}
	// No model: the catalog's default slot.
	if plain := swarm.GetMember(w, "plain"); plain.LaunchSettings.Provider != "opencode" || plain.LaunchSettings.Connection != "opencode@openrouter" || plain.LaunchSettings.Model != builtinLaunch.Catalogs["openrouter-budget"].Slots["frontier"].Model {
		t.Fatalf("member without a model = %+v", plain.LaunchSettings)
	}
	// A member may name its own catalog, and a model id on it.
	if own := swarm.GetMember(w, "own"); own.Catalog != "anthropic" || own.LaunchSettings.Provider != "claude" || own.LaunchSettings.Model != "haiku" || own.LaunchSettings.Connection != "" {
		t.Fatalf("member on its own catalog = %+v", own.LaunchSettings)
	}
	// A member reads its role and task, never its model.
	if brief := memberBrief(*rev, "", "a1"); strings.Contains(brief, rev.LaunchSettings.Model) {
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
	if _, err := s.Setup("lead", "claude", cwd, "Project", project); err != nil {
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
	w, _ := s.Setup("lead", "codex", t.TempDir(), "Team", "")
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

// A launch that failed takes the changed fields alone, as the receipt
// promises ("omitted fields stay"): one wrong model string cost three calls
// when the correction had to restate name and instructions (Redoubt,
// WASH-R02). The role template is not put on twice.
func TestAFailedLaunchTakesAPatchOfChangedFields(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // every launch fails
	withPolicy(t, agentpolicy.Policy{})
	root := t.TempDir()
	s, _ := swarm.Open(filepath.Join(root, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "claude", catalog: "anthropic", cwd: root}
	call := func(args string) (any, error) {
		return ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: json.RawMessage(args)})
	}
	if _, err := call(`{"workspace":{"name":"Team"},"roles":{"architect":{"instructions":"You design."}},"members":{"arch":{"name":"Architect","role":"architect","lifetime":"resident","instructions":"Own the plan.","provider":"claude","model":"opus[1m]","effort":"high"}}}`); err != nil {
		t.Fatal(err)
	}
	failed := swarm.GetMember(s.View("lead"), "arch")
	if failed.State != "failed" || failed.Session != "" {
		t.Fatalf("launch with no adapter on PATH: %+v", failed)
	}
	if _, err := call(`{"members":{"arch":{"model":"opus"}}}`); err != nil {
		t.Fatalf("a patch to a failed launch was refused: %v", err)
	}
	again := swarm.GetMember(s.View("lead"), "arch")
	if again.ID != failed.ID || again.Name != "Architect" || again.Role != "architect" || again.Instructions != "You design.\n\nOwn the plan." || again.Model != "opus" || again.LaunchSettings.Effort != "high" {
		t.Fatalf("patched member = %+v (settings %+v)", again, again.LaunchSettings)
	}
	// The provider the member was defined on is part of the base the patch
	// lands on, like every other launch setting.
	if again.LaunchSettings.Provider != "claude" {
		t.Fatalf("patch moved the member off its provider: %+v", again.LaunchSettings)
	}
	// A key that launched (or is launching) still takes no silent change.
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, _ *swarm.Member) error {
		swarm.GetMember(w, "arch").Session = "arch-s"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(`{"members":{"arch":{"model":"sonnet"}}}`); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("a launched member was patched: %v", err)
	}
}

// A task on a node whose needs are not done starts with override on the
// member, as it does on assignment_update, instead of launch, wait, assign
// (Redoubt, WASH-R06).
func TestOverrideOnLaunchStartsATaskEarly(t *testing.T) {
	root := t.TempDir()
	s, _ := swarm.Open(filepath.Join(root, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: root}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Team"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := qaFileCall(t, ws, h, "plan_set", map[string]any{"nodes": map[string]any{"K4": map[string]any{"title": "Design"}, "K5": map[string]any{"title": "Build", "needs": []string{"K4"}}}}); err != nil {
		t.Fatal(err)
	}
	member := map[string]any{"name": "Impl", "lifetime": "resident", "instructions": "Build it.", "node": "K5", "task": "Build K5."}
	_, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"preview": true, "members": map[string]any{"impl": member}})
	if err == nil || !strings.Contains(err.Error(), "needs K4") || !strings.Contains(err.Error(), `override:"<reason>" on the member`) {
		t.Fatalf("task on a gated node: %v", err)
	}
	member["override"] = "design is agreed in the thread; building in parallel"
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"preview": true, "members": map[string]any{"impl": member}}); err != nil {
		t.Fatalf("override on the member refused: %v", err)
	}
	delete(member, "task")
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"preview": true, "members": map[string]any{"impl": member}}); err == nil || !strings.Contains(err.Error(), "override goes with a task") {
		t.Fatalf("override without a task: %v", err)
	}
}

// A reviewer launches on any provider; what the adapter enforces is advisory
// and recorded, not a refusal (Redoubt, WASH-R01: six of nine members failed
// at launch, committed, with no way forward but dropping the capability —
// the worst state, unenforced and unrecorded). about says what a reviewer
// launched now would record, the receipt's advisories say so per member
// before commit, and enforcement:"adapter" is the one way to be refused.
func TestReviewerIsAdvisedAndRecordedNotRefused(t *testing.T) {
	withPolicy(t, agentpolicy.Policy{})
	adapterMemMu.Lock()
	adapterMem = map[string]agentproto.AdapterOptions{"claude": {Adapter: "claude", Version: "0.85.0"}}
	adapterMemMu.Unlock()
	t.Cleanup(resetAdapterMemoryForTest)
	root := t.TempDir()
	s, _ := swarm.Open(filepath.Join(root, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "claude", catalog: "anthropic", cwd: root}
	about := ws.about(h)
	profiles := about.Permissions["reviewer_capability_profiles"].(map[string]any)
	claude := profiles["claude"].(map[string]any)
	if claude["installed_version"] != "0.85.0" || claude["enforcement"] != "unverified" || claude["launches"] != true {
		t.Fatalf("about on an unverified adapter: %v", claude)
	}
	if codex := profiles["codex"].(map[string]any); codex["enforcement"] != "host" || codex["launches"] != true {
		t.Fatalf("about for a provider with no allowlist: %v", codex)
	}
	reviewer := map[string]any{"name": "Red", "role": "reviewer", "lifetime": "resident", "instructions": "Review.", "capability": "reviewer"}
	got, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Team"}, "preview": true, "members": map[string]any{"red": reviewer}})
	if err != nil {
		t.Fatalf("advisory reviewer refused in preview: %v", err)
	}
	advisories := got.(map[string]any)["advisories"].(map[string][]string)
	if len(advisories["red"]) != 1 || !strings.Contains(advisories["red"][0], "0.85.0") || !strings.Contains(advisories["red"][0], `enforcement "unverified"`) {
		t.Fatalf("preview advisories: %v", advisories)
	}
	if s.View("lead") != nil {
		t.Fatal("preview committed")
	}
	// Strict is the exception, and is refused where this host knows.
	reviewer["enforcement"] = "adapter"
	_, err = qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Team"}, "preview": true, "members": map[string]any{"red": reviewer}})
	if err == nil || !strings.Contains(err.Error(), "0.85.0") || !strings.Contains(err.Error(), `enforcement "adapter" cannot be met`) {
		t.Fatalf("strict reviewer on an unverified adapter passed preview: %v", err)
	}
	delete(reviewer, "capability")
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Team"}, "preview": true, "members": map[string]any{"red": reviewer}}); err == nil || !strings.Contains(err.Error(), "enforcement goes with") {
		t.Fatalf("enforcement without the capability: %v", err)
	}
	// The launch path agrees: unverified launches and records, strict refuses.
	info := acp.Implementation{Name: claudeAdapter, Version: "0.85.0"}
	if meta, level, err := reviewerMetadata("claude", info, ""); err != nil || meta == nil || level != enforcementUnverified {
		t.Fatalf("launch on an unverified adapter: %v %v %v", meta, level, err)
	}
	if _, _, err := reviewerMetadata("claude", info, "adapter"); err == nil {
		t.Fatal("strict launch on an unverified adapter")
	}
	resetAdapterMemoryForTest()
	if about := ws.about(h); about.Permissions["reviewer_capability_profiles"].(map[string]any)["claude"].(map[string]any)["installed_version"] != nil {
		t.Fatal("about claims a version no session reported")
	}
}
