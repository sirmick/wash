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
	if err := call(map[string]any{"preview": true, "document": map[string]string{"path": filepath.Join(project, "docs", "PLAN.md")}, "qa_document": map[string]string{"path": filepath.Join(project, "docs", "QA.md")}, "members": member(filepath.Join(project, ".worktrees", "k5"))}); err != nil {
		t.Fatalf("path inside the approved root asked: %v", err)
	}
	// Relative paths are the project root's, not the orchestrator's cwd.
	if err := call(map[string]any{"preview": true, "document": map[string]string{"path": "docs/PLAN.md"}, "qa_document": map[string]string{"path": "docs/QA.md"}}); err != nil {
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
	h := &hosted{sessionID: "lead", agent: "opencode", connection: "opencode@openrouter", stack: "openrouter", cwd: root}
	call := func(args string) error {
		_, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: json.RawMessage(args)})
		return err
	}
	member := func(key, extra string) string {
		return fmt.Sprintf(`"%s":{"name":%q,"lifetime":"resident","instructions":"Wait for assignments."%s}`, key, key, extra)
	}
	if err := call(`{"workspace":{"name":"Team"},"members":{` + member("rev", `,"tier":"review"`) + `,` + member("impl", `,"tier":"coding","thinking":"max"`) + `,` + member("plain", "") + `}}`); err != nil {
		t.Fatal(err)
	}
	w := s.View("lead")
	if w.Stack != "openrouter" {
		t.Fatalf("stack = %q, want the orchestrator's", w.Stack)
	}
	if lead := swarm.GetMember(w, w.Lead); lead.LaunchSettings == nil || lead.LaunchSettings.Connection != "opencode@openrouter" {
		t.Fatalf("orchestrator launch settings = %+v", lead.LaunchSettings)
	}
	want := builtinStacks["openrouter"].Tiers["review"]
	rev := swarm.GetMember(w, "rev")
	if rev.Tier != "review" || rev.LaunchSettings.Model != want.Model || rev.LaunchSettings.Connection != want.Connection || rev.Provider != "opencode" {
		t.Fatalf("review member = tier %q %+v", rev.Tier, rev.LaunchSettings)
	}
	if impl := swarm.GetMember(w, "impl"); impl.LaunchSettings.Model != builtinStacks["openrouter"].Tiers["coding"].Model || impl.LaunchSettings.Thinking != "max" {
		t.Fatalf("explicit thinking did not override the tier: %+v", impl.LaunchSettings)
	}
	if plain := swarm.GetMember(w, "plain"); plain.LaunchSettings.Provider != "opencode" || plain.LaunchSettings.Connection != "opencode@openrouter" {
		t.Fatalf("member without tier = %+v", plain.LaunchSettings)
	}
	// A member reads its role and task, never its model.
	if brief := memberBrief(*rev, "a1"); strings.Contains(brief, rev.LaunchSettings.Model) {
		t.Fatal("the member brief carries a model string")
	}

	// Another stack affects later launches only.
	if err := call(`{"stack":"anthropic","members":{` + member("rev2", `,"tier":"review"`) + `}}`); err != nil {
		t.Fatal(err)
	}
	w = s.View("lead")
	if rev2 := swarm.GetMember(w, "rev2"); rev2.LaunchSettings.Provider != "claude" || rev2.LaunchSettings.Capability != "reviewer" {
		t.Fatalf("after the stack change = %+v", rev2.LaunchSettings)
	}
	if swarm.GetMember(w, "rev").LaunchSettings.Provider != "opencode" {
		t.Fatal("a stack change rewrote an existing member")
	}

	for name, args := range map[string]string{
		"tier and profile": `{"profiles":{"p":{"provider":"claude"}},"members":{` + member("both", `,"tier":"coding","profile":"p"`) + `}}`,
		"unknown tier":     `{"members":{` + member("odd", `,"tier":"huge"`) + `}}`,
		"unknown stack":    `{"stack":"nope"}`,
		"bad connection":   `{"profiles":{"p":{"provider":"codex","connection":"opencode@openrouter"}}}`,
	} {
		if err := call(args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Without a stack there is nothing for a tier to name.
func TestMemberTierNeedsAWorkspaceStack(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	withPolicy(t, agentpolicy.Policy{})
	root := t.TempDir()
	s, _ := swarm.Open(filepath.Join(root, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: root}
	_, err := ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_configure", Arguments: json.RawMessage(`{"workspace":{"name":"Team"},"members":{"a":{"name":"A","lifetime":"resident","instructions":"x","tier":"coding"}}}`)})
	if err == nil || !strings.Contains(err.Error(), "no stack") {
		t.Fatalf("err = %v", err)
	}
}
