package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An ended workspace's record moves to its own file and the live state
// keeps a stub: the state is deep-copied on every read, and one long run's
// ended workspaces made it 8.8 MB, 45 ms a copy, a core of agentd always.
func TestArchiveMovesAnEndedWorkspaceOutOfTheState(t *testing.T) {
	s, w := fixture(t)
	if _, err := s.Send("lead-session", "worker", "instruction", "Build the timers.", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Archive(w.ID); err == nil || !strings.Contains(err.Error(), "only an ended") {
		t.Fatalf("an active workspace archived: %v", err)
	}
	if err := s.Mutate("lead-session", true, func(w *Workspace, _ *Member) error {
		w.State = "ended"
		w.Legend = strings.Repeat("x", 1000)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.path)
	if got := s.Unarchived(); len(got) != 1 || got[0] != w.ID {
		t.Fatalf("unarchived = %v", got)
	}
	if err := s.Archive(w.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Archive(w.ID); err != nil {
		t.Fatalf("archiving twice: %v", err)
	}
	if got := s.Unarchived(); len(got) != 0 {
		t.Fatalf("still unarchived: %v", got)
	}
	after, _ := os.ReadFile(s.path)
	if len(after) >= len(before) {
		t.Fatalf("state did not shrink: %d -> %d bytes", len(before), len(after))
	}
	st := s.Snapshot().Workspaces[0]
	want := filepath.Join(filepath.Dir(s.path), "workspaces-archive", w.ID+".json")
	if st.Archive != want || len(st.Messages) != 0 || len(st.Plan) != 0 || st.Legend != "" || st.QADir != "" {
		t.Fatalf("stub: %+v", st)
	}
	// Parentage and identities survive in the stub.
	if p := s.Parents(); p["worker-session"] != "lead-session" {
		t.Fatalf("parents after archive: %v", p)
	}
	full, err := LoadArchived(st.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Messages) == 0 || full.Messages[0].Body != "Build the timers." || len(full.Plan) != 1 || full.Legend == "" {
		t.Fatalf("archive lost the record: %+v", full)
	}
	if info, err := os.Stat(st.Archive); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive file mode: %v %v", info, err)
	}
	// The stub reloads like any state.
	again, err := Open(s.path)
	if err != nil || again.Snapshot().Workspaces[0].Archive != want {
		t.Fatalf("reopened: %v", err)
	}
}
