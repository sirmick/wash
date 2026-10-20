package agentd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/swarm"
)

// Closing folds the team back: idle members are asked to hand off, busy
// ones are named to the lead, and new members launch only as replacements.
func TestClosingPhaseFoldsTheTeam(t *testing.T) {
	dir := t.TempDir()
	s, _ := swarm.Open(filepath.Join(dir, "state.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "lead", agent: "codex", cwd: dir}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"workspace": map[string]string{"name": "Fold"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := qaFileCall(t, ws, h, "plan_set", map[string]any{"nodes": map[string]any{"N1": map[string]any{"title": "Tail", "state": "active"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("lead", true, func(w *swarm.Workspace, lead *swarm.Member) error {
		w.Members = append(w.Members,
			swarm.Member{ID: "idle", Key: "idle", Name: "Idle reviewer", State: "available"},
			swarm.Member{ID: "busy", Key: "busy", Name: "Busy implementer", State: "available", Node: "N1"})
		w.Assignments = append(w.Assignments, swarm.Assignment{ID: "a1", Assigner: lead.ID, Member: "busy", Node: "N1", State: "active", Text: "finish the tail"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"phase": "closing"}); err != nil {
		t.Fatal(err)
	}
	w := s.View("lead")
	if w.Phase != "closing" {
		t.Fatalf("phase %q", w.Phase)
	}
	var toIdle, toBusy, toLead int
	for _, m := range w.Messages {
		switch {
		case m.Recipient == "idle" && m.Type == "instruction" && strings.Contains(m.Body, "closing"):
			toIdle++
		case m.Recipient == "busy":
			toBusy++
		case m.Recipient == w.Lead && m.Type == "lifecycle" && strings.Contains(m.Body, "Idle reviewer asked") && strings.Contains(m.Body, "Busy implementer still"):
			toLead++
		}
	}
	if toIdle != 1 || toBusy != 0 || toLead != 1 {
		t.Fatalf("messages: idle %d busy %d lead %d: %+v", toIdle, toBusy, toLead, w.Messages)
	}
	// Closing twice asks nobody twice.
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"phase": "closing"}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.View("lead").Messages); n != 2 {
		t.Fatalf("%d messages after a second closing", n)
	}
	_, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"preview": true, "members": map[string]any{"fresh": map[string]any{"name": "Fresh", "lifetime": "resident", "instructions": "help close", "role": "implementer"}}})
	if err == nil || !strings.Contains(err.Error(), "closing") {
		t.Fatalf("a new member while closing: %v", err)
	}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"phase": "sideways"}); err == nil {
		t.Fatal("an unknown phase was accepted")
	}
	if _, err := qaFileCall(t, ws, h, "workspace_configure", map[string]any{"phase": ""}); err != nil {
		t.Fatal(err)
	}
	if p := s.View("lead").Phase; p != "" {
		t.Fatalf("phase %q after reopening", p)
	}
}
