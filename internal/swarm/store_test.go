package swarm

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func fixture(t *testing.T) (*Store, *Workspace) {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "workspaces.json"))
	if e != nil {
		t.Fatal(e)
	}
	w, e := s.Setup("lead-session", "codex", "/project", "Project", "/project", nil)
	if e != nil {
		t.Fatal(e)
	}
	e = s.Mutate("lead-session", true, func(w *Workspace, m *Member) error {
		w.Members = append(w.Members, Member{ID: "worker", Name: "Worker", Session: "worker-session", Lifetime: "resident", State: "available", Creator: m.ID})
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return s, w
}
func TestInboxAcrossIdleAndRecovery(t *testing.T) {
	s, w := fixture(t)
	a, e := s.Assign("lead-session", "worker", "Build timers", "task-1")
	if e != nil {
		t.Fatal(e)
	}
	first, e := s.Next("worker-session")
	if e != nil || len(first) != 1 {
		t.Fatalf("dispatch: %v %v", first, e)
	}
	q, e := s.Send("worker-session", w.Lead, "question", "Which timer?", "", a.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	// Reply arrives before the recipient has yielded its active turn.
	answer, e := s.Send("lead-session", "worker", "answer", "Use the monotonic clock", q.ID, a.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.TurnEnded("worker-session", []string{first[0].ID}, false); e != nil {
		t.Fatal(e)
	}
	next, e := s.Next("worker-session")
	if e != nil || len(next) != 1 || next[0].ID != answer.ID {
		t.Fatalf("lost early reply: %v %v", next, e)
	}
	if e = s.Complete("worker-session", a.ID, "Timer tests pass", false); e != nil {
		t.Fatal(e)
	}
	v := s.View("worker-session")
	if GetMember(v, "worker").Retire {
		t.Fatal("resident retired")
	}
	// Process restart preserves the dispatched message as uncertain, and pauses.
	restored, e := Open(s.path)
	if e != nil {
		t.Fatal(e)
	}
	v = restored.View("worker-session")
	if v.State != "paused" {
		t.Fatal(v.State)
	}
	for _, m := range v.Messages {
		if m.ID == answer.ID && m.State != "uncertain" {
			t.Fatalf("delivery = %s", m.State)
		}
	}
	if next, e = restored.Next("worker-session"); e != nil || len(next) != 0 {
		t.Fatal("replayed uncertain work")
	}
}
func TestPersistenceFailureDoesNotAcknowledgeOrMutate(t *testing.T) {
	s, _ := fixture(t)
	before := s.Snapshot()
	s.write = func(string, []byte) error { return errors.New("disk full") }
	if _, e := s.Send("lead-session", "worker", "instruction", "Do work", "", "", ""); e == nil {
		t.Fatal("accepted message despite failed persistence")
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("failed transaction mutated visible state")
	}
}
func TestConcurrentRetryAndRecipientIsolation(t *testing.T) {
	s, w := fixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.Send("lead-session", "worker", "instruction", "Do work", "", "", "same-request"); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	v := s.View("lead-session")
	if len(v.Messages) != 1 {
		t.Fatalf("duplicate acceptance: %d", len(v.Messages))
	}
	if _, e := s.Send("lead-session", "worker", "instruction", "Different", "", "", "same-request"); e == nil {
		t.Fatal("conflicting retry accepted")
	}
	if _, e := s.Send("worker-session", "other-workspace-member", "answer", "secret", "", "", ""); e == nil {
		t.Fatal("cross-workspace recipient accepted")
	}
	if _, e := s.Next("worker-session"); e != nil {
		t.Fatal(e)
	}
	if s.View("worker-session").Messages[0].State != "dispatched" || GetMember(s.View("lead-session"), w.Lead).State != "available" {
		t.Fatal("delivery changed the sender's lifecycle or skipped dispatch")
	}
}
func TestStopRetainsMailAndEphemeralCompletionIsExplicit(t *testing.T) {
	s, _ := fixture(t)
	_ = s.Mutate("worker-session", false, func(_ *Workspace, m *Member) error { m.Lifetime = "ephemeral"; return nil })
	a, e := s.Assign("lead-session", "worker", "Review", "request")
	if e != nil {
		t.Fatal(e)
	}
	_ = s.TurnEnded("worker-session", nil, true)
	if next, _ := s.Next("worker-session"); len(next) != 0 {
		t.Fatal("paused member woke")
	}
	if messages := s.View("worker-session").Messages; len(messages) != 2 || messages[0].Assignment != a.ID || messages[0].State != "queued" || messages[1].Type != "lifecycle" {
		t.Fatal("Stop lost mail")
	}
	_ = s.Mutate("worker-session", false, func(_ *Workspace, m *Member) error { m.State = "available"; return nil })
	msg, _ := s.Next("worker-session")
	_ = s.TurnEnded("worker-session", []string{msg[0].ID}, false)
	if GetMember(s.View("worker-session"), "worker").Retire {
		t.Fatal("turn end retired worker")
	}
	if e = s.Complete("worker-session", a.ID, "No findings", false); e != nil {
		t.Fatal(e)
	}
	if !GetMember(s.View("worker-session"), "worker").Retire {
		t.Fatal("explicit completion did not retire ephemeral")
	}
}

func TestLeadFailurePausesSwarmAndACleanTurnDelivers(t *testing.T) {
	s, _ := fixture(t)
	msg, err := s.Send("lead-session", "worker", "instruction", "Work", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Next("worker-session"); err != nil {
		t.Fatal(err)
	}
	if err = s.TurnEnded("worker-session", []string{msg.ID}, false); err != nil {
		t.Fatal(err)
	}
	// Delivery is receipt: nothing further is asked of the member.
	if s.View("worker-session").Messages[0].State != "delivered" {
		t.Fatal("a clean turn did not deliver its message")
	}
	if err = s.TurnEnded("lead-session", nil, true); err != nil {
		t.Fatal(err)
	}
	if s.View("worker-session").State != "paused" {
		t.Fatal("orchestrator failure left swarm active")
	}
	if next, err := s.Next("worker-session"); err != nil || len(next) != 0 {
		t.Fatal("dispatch continued while paused")
	}
}

func TestDelegateEndingCancelsItsDecisionsAndRoutesChildResultToLead(t *testing.T) {
	s, w := fixture(t)
	if err := s.Mutate("lead-session", true, func(w *Workspace, _ *Member) error {
		GetMember(w, "worker").CanSpawn = true
		w.Members = append(w.Members, Member{ID: "child", Session: "child-session", State: "available", Lifetime: "ephemeral"})
		_, err := AddMessage(w, "worker", "human", "decision_request", "Need a decision", "", "", "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assignment, err := s.Assign("worker-session", "child", "Review", "request")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EndMember("lead-session", "worker", false); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete("child-session", assignment.ID, "Review complete", false); err != nil {
		t.Fatal(err)
	}
	state := s.View("lead-session")
	if state.Messages[0].State != "cancelled" {
		t.Fatal("ended member's decision remained pending")
	}
	result := state.Messages[len(state.Messages)-1]
	if result.Type != "result" || result.Recipient != w.Lead {
		t.Fatal(result)
	}
}

// A human stopping the lead's turn redirects their own conversation; it is
// not the orchestrator failure that pauses the team. A stopped member still
// pauses, as before.
func TestStoppingTheLeadDoesNotPauseTheTeam(t *testing.T) {
	s, _ := fixture(t)
	if err := s.TurnStopped("lead-session", nil); err != nil {
		t.Fatal(err)
	}
	if got := s.View("worker-session").State; got != "active" {
		t.Fatalf("workspace %q after the human stopped the lead, want active", got)
	}
	if _, err := s.Send("lead-session", "worker", "instruction", "Work", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if next, err := s.Next("worker-session"); err != nil || len(next) == 0 {
		t.Fatal("dispatch stopped after the lead was interrupted", err)
	}
	if err := s.TurnStopped("worker-session", nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range s.View("worker-session").Members {
		if m.ID == "worker" && m.State != "paused" {
			t.Fatalf("stopped member %q, want paused", m.State)
		}
	}
}

// The lead hears about a member ending only when it did not end it itself.
func TestEndingAMemberNotifiesTheLeadOnlyWhenAsked(t *testing.T) {
	s, w := fixture(t)
	count := func() int {
		n := 0
		for _, m := range s.View("lead-session").Messages {
			if m.Type == "lifecycle" && m.Recipient == w.Lead {
				n++
			}
		}
		return n
	}
	if err := s.EndMember("lead-session", "worker", false); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("lead-ended member sent %d notices", n)
	}
	if err := s.Mutate("lead-session", true, func(w *Workspace, _ *Member) error {
		w.Members = append(w.Members, Member{ID: "w2", Name: "W2", Session: "w2-s", State: "available", Lifetime: "resident"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.EndMember("lead-session", "w2", true); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("externally ended member sent %d notices, want 1", n)
	}
}

// A report is a summary: it is re-read on every later turn of whoever receives
// it, and the orchestrator's context is a workspace's largest cost.
func TestReportsToTheOrchestratorAreSummaries(t *testing.T) {
	s, w := fixture(t)
	long := strings.Repeat("x", ReportLimit+1)
	if _, err := s.Send("worker-session", w.Lead, "progress", long, "", "", ""); err == nil || !strings.Contains(err.Error(), "QA thread or a file") {
		t.Fatal("long member report to the orchestrator accepted", err)
	}
	if _, err := s.Send("worker-session", w.Lead, "progress", long[:ReportLimit], "", "", ""); err != nil {
		t.Fatal(err)
	}
	// Briefs flow the other way and stay long.
	if _, err := s.Send("lead-session", "worker", "instruction", long, "", "", ""); err != nil {
		t.Fatal("orchestrator brief capped", err)
	}
	a, err := s.Assign("lead-session", "worker", "Do it", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Complete("worker-session", a.ID, long, false); err == nil {
		t.Fatal("long result accepted")
	}
	if err = s.Complete("worker-session", a.ID, "Done; details in QA thread k5-timer.", false); err != nil {
		t.Fatal(err)
	}
}

// Observed in Redoubt: two implementers reported finished work as progress,
// set waiting, and the orchestrator slept on — both sides waiting. The last
// report before a member goes idle wakes the orchestrator; check-ins do not.
func TestAMembersLastReportBeforeWaitingWakesTheOrchestrator(t *testing.T) {
	s, w := fixture(t)
	for _, body := range []string{"step 1 done", "fix round done, staged"} {
		if _, err := s.Send("worker-session", w.Lead, "progress", body, "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if next, _ := s.Next("lead-session"); len(next) != 0 {
		t.Fatal("a check-in woke the orchestrator")
	}
	if err := s.Mutate("worker-session", false, func(w *Workspace, m *Member) error {
		m.Waiting = "Waiting for review"
		DeliverLastReport(w, m)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next, err := s.Next("lead-session")
	if err != nil || len(next) != 1 || next[0].Body != "fix round done, staged" {
		t.Fatalf("orchestrator woke with %+v, want the last report", next)
	}
	if first := s.View("lead-session").Messages[0]; first.State != "recorded" {
		t.Fatalf("an earlier check-in was delivered too: %s", first.State)
	}
	// The orchestrator going idle wakes nobody.
	if err := s.Mutate("lead-session", false, func(w *Workspace, m *Member) error { DeliverLastReport(w, m); return nil }); err != nil {
		t.Fatal(err)
	}
}
