package swarm

import (
	"path/filepath"
	"testing"

	"github.com/sirmick/wash/internal/agentpolicy"
)

func approvalStore(t *testing.T) (*Store, *Workspace) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Setup("lead-session", "claude", "/tmp", "Project", "/tmp", nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, w
}

func TestApprovalCoversEveryMemberWhateverItsCwd(t *testing.T) {
	s, w := approvalStore(t)
	if err := s.AddApproval(w.ID, "Bash(go test*)", agentpolicy.DecisionAllow); err != nil {
		t.Fatal(err)
	}
	_, name, pol := s.ApprovalsFor("lead-session")
	if name != "Project" {
		t.Fatalf("workspace name = %q", name)
	}
	// The point of membership scoping: a sibling worktree is not under
	// project_root, so a path-scoped rule would have missed it.
	for _, cwd := range []string{"/tmp", "/tmp/branches/k5", "/data/project-worktree", ""} {
		res := agentpolicy.Evaluate(pol, agentpolicy.Request{
			ToolName:  "Bash",
			ToolInput: map[string]any{"command": "go test ./..."},
			Cwd:       cwd,
		})
		if res.Decision != agentpolicy.DecisionAllow {
			t.Fatalf("cwd %q: decision = %s, want allow", cwd, res.Decision)
		}
	}
	// It is still a rule, not a blanket: a different command still asks.
	res := agentpolicy.Evaluate(pol, agentpolicy.Request{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "rm -rf /"},
		Cwd:       "/tmp",
	})
	if res.Decision != agentpolicy.DecisionAsk {
		t.Fatalf("unrelated command decision = %s, want ask", res.Decision)
	}
}

func TestApprovalEmptyTableDecidesNothing(t *testing.T) {
	s, _ := approvalStore(t)
	_, _, pol := s.ApprovalsFor("lead-session")
	res := agentpolicy.Evaluate(pol, agentpolicy.Request{ToolName: "Read", Cwd: "/tmp"})
	if res.Decision != agentpolicy.DecisionAsk {
		t.Fatalf("empty table decision = %s, want ask", res.Decision)
	}
}

func TestApprovalRepeatIsNotAnError(t *testing.T) {
	// Two members asking the same question before either answer is saved is
	// the ordinary case, not a conflict.
	s, w := approvalStore(t)
	for i := 0; i < 3; i++ {
		if err := s.AddApproval(w.ID, "Read", agentpolicy.DecisionAllow); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if got := len(s.View("lead-session").Approvals); got != 1 {
		t.Fatalf("rules = %d, want 1", got)
	}
}

func TestApprovalNewestAnswerWins(t *testing.T) {
	s, w := approvalStore(t)
	if err := s.AddApproval(w.ID, "Bash(git*)", agentpolicy.DecisionAllow); err != nil {
		t.Fatal(err)
	}
	if err := s.AddApproval(w.ID, "Bash(git push*)", agentpolicy.DecisionDeny); err != nil {
		t.Fatal(err)
	}
	_, _, pol := s.ApprovalsFor("lead-session")
	res := agentpolicy.Evaluate(pol, agentpolicy.Request{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git push origin main"},
		Cwd:       "/tmp",
	})
	// agentpolicy takes the first match, so a later, narrower answer must
	// sit ahead of the broader one it refines.
	if res.Decision != agentpolicy.DecisionDeny {
		t.Fatalf("decision = %s (rule %q), want deny", res.Decision, res.Rule)
	}
}

func TestApprovalRejectsBadInput(t *testing.T) {
	s, w := approvalStore(t)
	if err := s.AddApproval(w.ID, "", agentpolicy.DecisionAllow); err == nil {
		t.Fatal("empty rule accepted")
	}
	if err := s.AddApproval(w.ID, "Read", agentpolicy.DecisionAsk); err == nil {
		t.Fatal("ask accepted as a stored decision")
	}
	if err := s.AddApproval("no-such-workspace", "Read", agentpolicy.DecisionAllow); err == nil {
		t.Fatal("unknown workspace accepted")
	}
}

func TestApprovalsForNonMemberSessionIsSilent(t *testing.T) {
	// Every ordinary agent conversation takes this path, so it must read as
	// "nothing to say" rather than as an error or a policy.
	s, _ := approvalStore(t)
	id, name, pol := s.ApprovalsFor("some-other-session")
	if id != "" || name != "" || pol.Enabled {
		t.Fatalf("non-member got id=%q name=%q enabled=%v", id, name, pol.Enabled)
	}
}
