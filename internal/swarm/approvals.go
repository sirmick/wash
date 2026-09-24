package swarm

import (
	"errors"

	"github.com/sirmick/wash/internal/agentpolicy"
)

// Workspace-scoped approvals.
//
// The global table (~/.config/wash/agents.json) scopes a shell rule to the
// directory the question came from, so that one project cannot buy a command
// in every other checkout. That is right for a single session and wrong for a
// fleet: every member works in its own worktree, so the same "always allow"
// has to be answered once per member, and again for each member launched
// later. Worse, worktrees are as often siblings of project_root as children
// of it, so widening the path scope would not reliably cover them either.
//
// These rules are scoped by MEMBERSHIP instead. They carry no Cwd, so
// agentpolicy's matcher — and its pattern semantics, and its tests — apply
// unchanged; what makes them narrow is that they are only ever consulted for
// a session that belongs to this workspace, and they die with it.

// maxApprovals bounds the table the way every other workspace list is bounded.
const maxApprovals = 200

// ApprovalPolicy wraps the workspace's rules as a policy the shared matcher
// can evaluate. Enabled is true because the table's existence IS the opt-in,
// and Default stays "ask" so an empty table decides nothing.
func ApprovalPolicy(w *Workspace) agentpolicy.Policy {
	if w == nil {
		return agentpolicy.Policy{}
	}
	return agentpolicy.Policy{Enabled: true, Default: agentpolicy.DecisionAsk, Rules: w.Approvals}
}

// AddApproval records one rule for the workspace with this ID. Keyed by
// workspace rather than by session: the human answers a question that a member
// asked, and that member's process may already be gone by the time the answer
// lands.
//
// Re-answering the same question is not an error — it is the ordinary result
// of a second member asking before the first answer was saved — so an existing
// identical rule succeeds silently, exactly as agentpolicy.Append does.
func (s *Store) AddApproval(workspaceID, match, decision string) error {
	if match == "" {
		return errors.New("approval requires a rule")
	}
	if decision != agentpolicy.DecisionAllow && decision != agentpolicy.DecisionDeny {
		return errors.New("approval decision must be allow or deny")
	}
	return s.change(func(st *State) error {
		for i := range st.Workspaces {
			w := &st.Workspaces[i]
			if w.ID != workspaceID {
				continue
			}
			if w.State == "ended" {
				return errors.New("workspace has ended")
			}
			for _, r := range w.Approvals {
				if r.Match == match && r.Decision == decision {
					return nil
				}
			}
			if len(w.Approvals) >= maxApprovals {
				return errors.New("workspace approval limit reached")
			}
			// Prepended, not appended: agentpolicy evaluates in order and
			// takes the first match, so the newest answer wins over an
			// older, broader one rather than being shadowed by it.
			w.Approvals = append([]agentpolicy.Rule{{Match: match, Decision: decision}}, w.Approvals...)
			w.Revision++
			return nil
		}
		return errors.New("unknown workspace")
	})
}

// ApprovalsFor returns the workspace this session belongs to, for callers that
// need both its ID (to record an answer against) and its rules (to decide
// one). Returns "" when the session is not a workspace member, which is the
// ordinary case for every agent conversation that never set one up.
func (s *Store) ApprovalsFor(session string) (id, name string, policy agentpolicy.Policy) {
	w := s.View(session)
	if w == nil {
		return "", "", agentpolicy.Policy{}
	}
	return w.ID, w.Name, ApprovalPolicy(w)
}
