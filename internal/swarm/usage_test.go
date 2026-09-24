package swarm

import (
	"path/filepath"
	"testing"
)

func TestUsageSurvivesRetirementAndDoesNotInvalidateCoordinationRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Setup("lead", "codex", t.TempDir(), "Team", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Mutate("lead", true, func(w *Workspace, _ *Member) error {
		w.Members = append(w.Members, Member{ID: "worker", Session: "worker", State: "available"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.EndMember("lead", "worker", false); err != nil {
		t.Fatal(err)
	}
	before := s.View("lead").Revision
	if err = s.RecordUsage("worker", 14689, 258400); err != nil {
		t.Fatal(err)
	}
	if s.View("lead").Revision != before {
		t.Fatal("telemetry invalidated coordination revision")
	}
	if err = s.RecordUsage("worker", 0, 0); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := GetMember(restored.View("lead"), "worker")
	if m.Usage == nil || m.Usage.Used != 14689 || m.Usage.Size != 258400 {
		t.Fatal(m)
	}
	if GetMember(restored.View("lead"), w.Lead).Usage != nil {
		t.Fatal("invented unreported usage")
	}
}

func TestUsageDoesNotRewritePriorWorkspaceWhenLeadConversationIsReused(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	if _, err := s.Setup("lead", "codex", t.TempDir(), "First", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage("lead", 100, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("lead", true, func(w *Workspace, _ *Member) error { w.State = "ended"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Setup("lead", "codex", t.TempDir(), "Second", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage("lead", 200, 1000); err != nil {
		t.Fatal(err)
	}
	state := s.Snapshot()
	if state.Workspaces[0].Members[0].Usage.Used != 100 || state.Workspaces[1].Members[0].Usage.Used != 200 {
		t.Fatal("archived workspace telemetry changed")
	}
}
