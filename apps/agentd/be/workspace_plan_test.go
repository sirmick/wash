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
