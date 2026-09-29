package agentd

import (
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/swarm"
)

// A resolution says what was decided. The notice used to carry the evidence
// alone, so a thread's creator was told the finding it already knew and had
// to ask again which option had been chosen.
func TestQAResolutionTellsTheThreadWhatWasDecided(t *testing.T) {
	w := &swarm.Workspace{
		ID: "w", Lead: "lead",
		Members: []swarm.Member{
			{ID: "lead", State: "available"},
			{ID: "writer", State: "available"},
		},
	}
	q := &swarm.QAThread{ID: "Q1", Node: "K5", Creator: "writer", Assignee: "lead", State: "open"}
	lead := swarm.GetMember(w, "lead")
	err := notifyQAUpdate(w, lead, q, swarm.QAUpdate{
		Action:   "resolve",
		ID:       "Q1",
		Body:     "Proceed with A: rebase onto main.",
		Evidence: "Both branches share the 3c84c00 parent.",
	})
	if err != nil {
		t.Fatal(err)
	}
	var notice string
	for _, m := range w.Messages {
		if m.Recipient == "writer" {
			notice = m.Body
		}
	}
	if !strings.Contains(notice, "Proceed with A") {
		t.Fatalf("the decision never reached the thread: %q", notice)
	}
	if !strings.Contains(notice, "3c84c00") {
		t.Fatalf("the evidence was dropped: %q", notice)
	}
}

// A resolution with evidence only still says something: evidence is required,
// body is not.
func TestQAResolutionWithoutABodyStillCarriesItsEvidence(t *testing.T) {
	w := &swarm.Workspace{
		ID: "w", Lead: "lead",
		Members: []swarm.Member{
			{ID: "lead", State: "available"},
			{ID: "writer", State: "available"},
		},
	}
	q := &swarm.QAThread{ID: "Q1", Node: "K5", Creator: "writer", Assignee: "lead", State: "open"}
	lead := swarm.GetMember(w, "lead")
	if err := notifyQAUpdate(w, lead, q, swarm.QAUpdate{Action: "resolve", ID: "Q1", Evidence: "Fixed in 47689f8."}); err != nil {
		t.Fatal(err)
	}
	var notice string
	for _, m := range w.Messages {
		if m.Recipient == "writer" {
			notice = m.Body
		}
	}
	if !strings.Contains(notice, "Fixed in 47689f8") {
		t.Fatalf("notice = %q", notice)
	}
}
