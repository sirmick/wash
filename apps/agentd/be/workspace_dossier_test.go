package agentd

import (
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/swarm"
)

func TestDossierSections(t *testing.T) {
	w := &swarm.Workspace{
		Name: "Redoubt", Root: "/p", PlanRevision: 7, Revision: 9, Legend: "🔴 blocked on the owner",
		Plan: []swarm.Node{
			{ID: "M1", Title: "Sessions", Template: "milestone", State: "done"},
			{ID: "K1", Title: "Kernel bit", Parent: "M1", State: "done", Body: "Built in cf445aa0c, merged 20261007 as c25fdbaa6."},
			{ID: "M2", Title: "Shell", Template: "milestone", State: "active", Needs: []string{"M1"}},
			{ID: "S1", Title: "Host shell", Parent: "M2", State: "reported"},
			{ID: "S2", Title: "Redoubt shell", Parent: "M2", State: "active", Needs: []string{"S1", "Q1"}},
			{ID: "S2a", Title: "step one", Parent: "S2", State: "todo"},
			{ID: "S3", Title: "Several harts", Parent: "M2", State: "todo", Needs: []string{"K1"}, Overrides: []string{"started early: owner's word"}},
			{ID: "M3", Title: "Agents", Template: "milestone", State: "todo", Needs: []string{"M2"}},
		},
		Members: []swarm.Member{
			{ID: "lead", Name: "Orchestrator", State: "available"},
			{ID: "impl", Name: "S2 implementer", Role: "implementer", Node: "S2", State: "available", HandoffPath: ".wash/local/S2-handoff.md", Status: "rebasing"},
		},
		Assignments: []swarm.Assignment{{ID: "a1", Node: "S1", Member: "impl", State: "done", Result: "Built on the host; bench green."}},
		QA: []swarm.QAThread{
			{ID: "Q1", Node: "S2", Title: "Which shell first", State: "open", Assignee: "lead", Blocking: true, Events: []swarm.QAEvent{
				{ID: "e1", Author: "impl", Kind: "open", Body: "Host or Redoubt first?", Message: "m1", Created: 1000},
				{ID: "e2", Author: "lead", Kind: "answer", Body: "Host first.", Message: "m2", Created: 2000},
			}},
			{ID: "Q0", Node: "K1", Title: "Ring size", State: "resolved", Assignee: "lead", Evidence: "16384 pages, both widths"},
		},
		Messages: []swarm.Message{
			{ID: "m1", Thread: "Q1", Type: "question", Sender: "impl", Created: 1000},
			{ID: "m2", Thread: "Q1", Type: "answer", Sender: "lead", ReplyTo: "m1", Created: 2000},
			{ID: "d1", Thread: "Q0", Type: "decision_response", Sender: "human", Body: "One ring size for both widths.", Created: 3000},
		},
	}
	text, truncated := dossier(w)
	if truncated {
		t.Fatal("truncated")
	}
	for _, want := range []string{
		"# Redoubt — closing dossier",
		"plan revision 7",
		"## Remaining work",
		"### M2 · active · Shell",
		"- **S1** · reported · Host shell",
		"- **S2** · active · Redoubt shell — waits on S1 (reported), Q1 (thread open) — S2 implementer (available)",
		"  - **S2a** · todo · step one",
		"- **S3** · todo · Several harts — unblocked",
		"  Override: started early: owner's word",
		"### M3 · todo · Agents — nothing planned inside it yet",
		"## Reported, awaiting acceptance",
		"- **S1** · Host shell — result: Built on the host; bench green.",
		"## Open threads",
		"### S2 · Q1 — Which shell first",
		"open · assigned to Orchestrator · **blocks its node**",
		"- S2 implementer · Question: Host or Redoubt first?",
		"  - Orchestrator · Answer (to the above): Host first.",
		"## Owner decisions",
		"thread Q0: One ring size for both widths.",
		"## Resolved threads",
		"- K1 · Q0 — Ring size · 16384 pages, both widths",
		"## Done work and its commits",
		"- K1 · Kernel bit: cf445aa0c, c25fdbaa6",
		"## Handoffs and members",
		"- S2 implementer (implementer) · available · on S2 · handoff: .wash/local/S2-handoff.md · rebasing",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "20261007") {
		t.Error("a date is not a commit")
	}
	if strings.Contains(text, "### M1") {
		t.Error("a done milestone is not remaining work")
	}
}
