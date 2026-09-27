package agentd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/swarm"
)

// The plan lives in Wash and in the project's plan file, never only in the
// orchestrator's context: a new workspace loads it back.
func TestPlanFileIsWrittenLoadedAndGuarded(t *testing.T) {
	dir := t.TempDir()
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Plan"}, "plan_file": ".wash/plan.toml", "qa_dir": ".wash/qa", "legend": "🔴 blocked on the owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := qaFileCall(t, ws, h, "plan_set", map[string]any{"nodes": map[string]any{
		"M1": map[string]any{"title": "Plan", "template": "milestone", "state": "active"},
		"M2": map[string]any{"title": "Build", "template": "milestone", "needs": []string{"M1"}},
		"A":  map[string]any{"title": "alpha.txt", "parent": "M2", "body": "Write words/alpha.txt"},
	}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".wash", "plan.toml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.HasPrefix(text, planFileMarker) || !strings.Contains(text, "[[node]]") || !strings.Contains(text, `parent = "M2"`) || !strings.Contains(text, "🔴 blocked on the owner") {
		t.Fatal(text)
	}
	got, err := qaFileCall(t, ws, h, "plan_get", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	lines := got.(map[string]any)["nodes"].([]string)
	if len(lines) != 3 || lines[0] != "M1 · active · Plan [milestone]" || !strings.HasPrefix(lines[2], "  A · todo · alpha.txt · needs M1 (active)") {
		t.Fatalf("plan_get lines: %q", lines)
	}
	if _, err := qaFileCall(t, ws, h, "workspace_end", map[string]any{}); err == nil || !strings.Contains(err.Error(), "M1") {
		t.Fatalf("ended with M1 active: %v", err)
	}
	if _, err := qaFileCall(t, ws, h, "workspace_end", map[string]any{"confirm": true}); err != nil {
		t.Fatal(err)
	}
	// A new workspace on the same file resumes the plan and takes it over.
	h2 := &hosted{sessionID: "next", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, h2, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Plan again"}, "plan_file": ".wash/plan.toml"}); err != nil {
		t.Fatal(err)
	}
	w := s.View("next")
	if len(w.Plan) != 3 || swarm.PlanNode(w, "A").Body != "Write words/alpha.txt" || w.Legend != "🔴 blocked on the owner" {
		t.Fatalf("loaded plan: %+v", w.Plan)
	}
	// Someone's own TOML is loaded, never overwritten.
	own := filepath.Join(dir, "mine.toml")
	if err := os.WriteFile(own, []byte("[[node]]\nid = \"X\"\ntitle = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := qaFileCall(t, ws, h2, "workspace_configure", map[string]any{"plan_file": "mine.toml"}); err == nil {
		t.Fatal("took someone's TOML as the plan file")
	}
	if _, err := qaFileCall(t, ws, h2, "plan_set", map[string]any{"from": "mine.toml"}); err != nil {
		t.Fatal(err)
	}
	if w := s.View("next"); len(w.Plan) != 1 || w.Plan[0].State != "todo" {
		t.Fatalf("hand-written plan: %+v", w.Plan)
	}
	if _, err := qaFileCall(t, ws, h2, "plan_set", map[string]any{"from": "mine.toml", "nodes": map[string]any{"Y": map[string]any{"title": "y", "parent": "missing"}}}); err == nil {
		t.Fatal("a broken plan loaded")
	}
}

func TestPlanAcceptReturnsTrailersAndFilesToStage(t *testing.T) {
	dir := t.TempDir()
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Plan"}, "plan_file": ".wash/plan.toml", "qa_dir": ".wash/qa"}); err != nil {
		t.Fatal(err)
	}
	addNodes(t, ws, h, "A")
	if _, err := qaFileCall(t, ws, h, "message_send", map[string]any{"recipient": "orchestrator", "type": "question", "body": "Case?", "qa": map[string]any{"id": "A-case", "action": "open", "node": "A", "title": "Case"}}); err != nil {
		t.Fatal(err)
	}
	got, err := qaFileCall(t, ws, h, "plan_accept", map[string]any{"node": "A", "gates": []any{map[string]any{"command": "grep -q alpha words/alpha.txt", "exit_code": 0}}})
	if err != nil {
		t.Fatal(err)
	}
	out := got.(map[string]any)
	if out["trailers"] != "Plan-Node: A\nQA: A-case\nGates: grep -q alpha words/alpha.txt 0" {
		t.Fatalf("trailers %q", out["trailers"])
	}
	stage := out["stage"].([]string)
	if len(stage) != 2 || stage[0] != ".wash/plan.toml" || stage[1] != ".wash/qa/A-case.md" {
		t.Fatalf("stage %v", stage)
	}
	for _, p := range stage {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Fatalf("%s not written by the time accept returned: %v", p, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".wash", "plan.toml")); !strings.Contains(string(b), `state = "done"`) {
		t.Fatal("plan file does not show A done")
	}
}

// A project's workspace is defined once, in .wash/workspace.toml, not
// transcribed from a template into every setup call (DOC1's drifted).
func TestWorkspaceConfiguresFromItsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wash"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := `name = "Shakedown"
max_active = 2
max_members = 6
qa_dir = ".wash/qa"
plan_file = ".wash/plan.toml"
legend = "🧪 in review"
context_warn = 0.5

[roles.implementer]
instructions = "Write one line; report with member_update."

[members.architect]
name = "Architect"
role = "architect"
lifetime = "resident"
instructions = "Plan the next milestone when asked."
`
	if err := os.WriteFile(filepath.Join(dir, ".wash", "workspace.toml"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"from": ".wash/workspace.toml", "max_active": 3, "preview": true}); err != nil {
		t.Fatal(err)
	}
	w := s.View("lead")
	if w != nil {
		t.Fatal("preview created the workspace")
	}
	// Members launch later (no adapter here); the file's definition is what
	// is under test, so leave its members out of this call.
	os.WriteFile(filepath.Join(dir, ".wash", "team.toml"), []byte(strings.Split(file, "[members.architect]")[0]), 0o644)
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"from": ".wash/team.toml", "max_active": 3}); err != nil {
		t.Fatal(err)
	}
	w = s.View("lead")
	if w.Name != "Shakedown" || w.MaxActive != 3 || w.MaxMembers != 6 || w.Legend != "🧪 in review" || w.ContextWarn != 0.5 || w.Roles["implementer"] == "" || !strings.HasSuffix(w.QADir, filepath.Join(".wash", "qa")) || !strings.HasSuffix(w.PlanFile, "plan.toml") {
		t.Fatalf("workspace from file: %+v", w)
	}
	addNodes(t, ws, h, "A")
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"preview": true, "members": map[string]any{"impl": map[string]any{"name": "Implementer", "role": "implementer", "node": "A", "lifetime": "resident", "instructions": "Write alpha."}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"from": ".wash/nope.toml"}); err == nil {
		t.Fatal("a missing file configured")
	}
	os.WriteFile(filepath.Join(dir, "bad.toml"), []byte("nmae = \"typo\"\n"), 0o644)
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"from": "bad.toml"}); err == nil || !strings.Contains(err.Error(), "nmae") {
		t.Fatalf("an unknown key was accepted: %v", err)
	}
}

// Creating a review round and waiting on it is one call, not two with an id
// copied between them (DOC1: "until_assignments rejects placeholders").
func TestAssignmentsCreatedAndWaitedOnInOneCall(t *testing.T) {
	dir := t.TempDir()
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Round"}}); err != nil {
		t.Fatal(err)
	}
	addNodes(t, ws, h, "A")
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, m *swarm.Member) error {
		for _, id := range []string{"r1", "r2"} {
			w.Members = append(w.Members, swarm.Member{ID: id, Name: id, Node: "A", Session: id + "-s", State: "available", Lifetime: "resident", Creator: m.ID})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := qaFileCall(t, ws, h, "assignment_update", map[string]any{"updates": []any{map[string]any{"action": "create", "member_id": "r1", "text": "Review"}, map[string]any{"action": "create", "member_id": "r2", "text": "Review"}}, "wait": map[string]any{"reason": "Review round 1"}})
	if err != nil {
		t.Fatal(err)
	}
	ids := got.(map[string]any)["waiting_on"].([]string)
	lead := swarm.GetMember(s.View("lead"), "orchestrator")
	if len(ids) != 2 || len(lead.WaitingOn) != 2 || lead.Waiting != "Review round 1" {
		t.Fatalf("not waiting on the round: %v %+v", ids, lead)
	}
}

// A member near the end of its context writes a handoff; its replacement
// reads it in its first message; nothing of it is meant for git.
func TestAHandoffReachesTheReplacement(t *testing.T) {
	dir := t.TempDir()
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Handoff"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, m *swarm.Member) error {
		w.Members = append(w.Members, swarm.Member{ID: "old", Key: "writer", Name: "Writer", Session: "old-s", State: "available", Lifetime: "resident", Creator: m.ID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old := &hosted{sessionID: "old-s", cwd: dir}
	got, err := qaFileCall(t, ws, old, "member_update", map[string]any{"handoff": "Pages 1–14 written; 15 needs the scheduler page."})
	if err != nil {
		t.Fatal(err)
	}
	path := got.(map[string]any)["handoff"].(string)
	if path != filepath.Join(dir, ".wash", "local", "handoffs", "writer.md") {
		t.Fatalf("handoff at %s", path)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".wash", "local", ".gitignore")); !strings.Contains(string(b), "*") {
		t.Fatal(".wash/local does not keep itself out of git")
	}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"preview": true, "members": map[string]any{"writer2": map[string]any{"name": "Writer 2", "lifetime": "resident", "instructions": "Write pages.", "handoff_from": "missing"}}}); err == nil {
		t.Fatal("a handoff that does not exist was accepted")
	}
	brief := memberBrief(swarm.Member{ID: "new", Instructions: "Write pages.", Handoff: "Pages 1–14 written", LaunchSettings: &swarm.AgentProfile{Provider: "codex"}}, "")
	if !strings.Contains(brief, "## Handoff from the member you replace") || !strings.Contains(brief, "Pages 1–14") {
		t.Fatal(brief)
	}
}
