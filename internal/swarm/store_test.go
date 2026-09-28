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
	w, e := s.Setup("lead-session", "codex", "/project", "Project", "/project")
	if e != nil {
		t.Fatal(e)
	}
	e = s.Mutate("lead-session", true, func(w *Workspace, m *Member) error {
		w.Plan = append(w.Plan, Node{ID: "K1", Title: "Timers", State: "todo"})
		w.Members = append(w.Members, Member{ID: "worker", Name: "Worker", Session: "worker-session", Lifetime: "resident", State: "available", Creator: m.ID, Node: "K1"})
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return s, w
}

// A member's turn carries everything queued in order, but at most one ask: a
// second instruction waits for the next turn, with whatever came after it.
// The orchestrator gets every ask at once.
func TestTurnCarriesAtMostOneAsk(t *testing.T) {
	s, w := fixture(t)
	if _, e := s.Send("lead-session", "worker", "answer", "Main moved; rebase first", "", "", ""); e != nil {
		t.Fatal(e)
	}
	a, e := s.Assign("lead-session", "worker", "", "", "Build timers", "task-1")
	if e != nil {
		t.Fatal(e)
	}
	// One active assignment per member, so the second ask is a plain
	// instruction.
	b, e := s.Send("lead-session", "worker", "instruction", "Also rebase onto main", "", "", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Send("lead-session", "worker", "answer", "Use the monotonic clock", "", "", ""); e != nil {
		t.Fatal(e)
	}
	first, e := s.Next("worker-session")
	if e != nil || len(first) != 2 || first[1].Assignment != a.ID {
		t.Fatalf("first turn = %+v %v", first, e)
	}
	if e = s.TurnEnded("worker-session", []string{first[0].ID, first[1].ID}, false); e != nil {
		t.Fatal(e)
	}
	second, e := s.Next("worker-session")
	if e != nil || len(second) != 2 || second[0].ID != b.ID || second[1].Type != "answer" {
		t.Fatalf("second turn = %+v %v", second, e)
	}
	for _, q := range []string{"Which clock?", "Rebase or merge?"} {
		if _, e = s.Send("worker-session", w.Lead, "question", q, "", "", ""); e != nil {
			t.Fatal(e)
		}
	}
	if lead, e := s.Next("lead-session"); e != nil || len(lead) != 2 {
		t.Fatalf("orchestrator turn = %+v %v", lead, e)
	}
}

// Ending a member cancels its assignment, and a cancelled assignment settles
// a waiting set: the rest of the round must not be held forever.
func TestWaitingSetSettlesWhenAMemberIsEnded(t *testing.T) {
	s, w := fixture(t)
	if err := s.Mutate("lead-session", true, func(w *Workspace, _ *Member) error {
		w.Members = append(w.Members, Member{ID: "other", Session: "other-session", State: "available", Lifetime: "resident", Node: "K1"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Assign("lead-session", "worker", "", "", "Review", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Assign("lead-session", "other", "", "", "Review", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Mutate("lead-session", false, func(_ *Workspace, m *Member) error {
		m.WaitingOn = []string{a.ID, b.ID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete("worker-session", a.ID, "OK", false); err != nil {
		t.Fatal(err)
	}
	if err = s.EndMember("lead-session", "other", false); err != nil {
		t.Fatal(err)
	}
	// The result, and the nudge that K1 is active with nobody on it.
	got, err := s.Next("lead-session")
	if err != nil || len(got) != 2 || got[0].Assignment != a.ID || got[1].Sender != "wash" || !strings.Contains(got[1].Body, "K1") {
		t.Fatalf("held after the set settled: %+v %v", got, err)
	}
	if lead := GetMember(s.View("lead-session"), w.Lead); len(lead.WaitingOn) != 0 {
		t.Fatalf("waiting set not cleared: %v", lead.WaitingOn)
	}
}

func TestInboxAcrossIdleAndRecovery(t *testing.T) {
	s, w := fixture(t)
	a, e := s.Assign("lead-session", "worker", "", "", "Build timers", "task-1")
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
// An abandoned turn's mail may never have reached the model: it is
// uncertain, and the member stays as it was.
func TestAbandonedTurnLeavesMailUncertain(t *testing.T) {
	s, _ := fixture(t)
	if _, e := s.Send("lead-session", "worker", "instruction", "do it", "", "", ""); e != nil {
		t.Fatal(e)
	}
	msg, _ := s.Next("worker-session")
	if e := s.TurnAbandoned("worker-session", []string{msg[0].ID}); e != nil {
		t.Fatal(e)
	}
	w := s.View("worker-session")
	if w.Messages[0].State != "uncertain" {
		t.Fatalf("message state = %s", w.Messages[0].State)
	}
	if m := GetMember(w, "worker"); m.State != "available" {
		t.Fatalf("member state = %s", m.State)
	}
	for _, v := range w.Messages {
		if v.Type == "lifecycle" {
			t.Fatal("abandoning a turn woke the orchestrator")
		}
	}
}

func TestStopRetainsMailAndEphemeralCompletionIsExplicit(t *testing.T) {
	s, _ := fixture(t)
	_ = s.Mutate("worker-session", false, func(_ *Workspace, m *Member) error { m.Lifetime = "ephemeral"; return nil })
	a, e := s.Assign("lead-session", "worker", "", "", "Review", "request")
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
		w.Members = append(w.Members, Member{ID: "child", Session: "child-session", State: "available", Lifetime: "ephemeral", Node: "K1"})
		_, err := AddMessage(w, "worker", "human", "decision_request", "Need a decision", "", "", "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assignment, err := s.Assign("worker-session", "child", "", "", "Review", "request")
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
	a, err := s.Assign("lead-session", "worker", "", "", "Do it", "")
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

// A cheap model that ends its turn without reporting is reminded once.
func TestAMemberThatForgetsToReportIsRemindedOnce(t *testing.T) {
	s, _ := fixture(t)
	a, err := s.Assign("lead-session", "worker", "", "", "Build timers", "")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.Next("worker-session")
	if err := s.TurnEnded("worker-session", []string{first[0].ID}, false); err != nil {
		t.Fatal(err)
	}
	next, _ := s.Next("worker-session")
	if len(next) != 1 || next[0].Sender != "wash" || next[0].Assignment != a.ID || !strings.Contains(next[0].Body, "member_update") {
		t.Fatalf("no reminder: %+v", next)
	}
	if err := s.TurnEnded("worker-session", []string{next[0].ID}, false); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Next("worker-session"); len(again) != 0 {
		t.Fatalf("reminded twice: %+v", again)
	}
}

// A note rides along with the next turn something else starts; alone it
// wakes nobody.
func TestANoteWaitsForTheNextTurn(t *testing.T) {
	s, w := fixture(t)
	if err := s.Mutate("lead-session", false, func(w *Workspace, _ *Member) error {
		_, err := AddMessage(w, "wash", w.Lead, "note", "Worker asked the owner", "", "", "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if next, _ := s.Next("lead-session"); len(next) != 0 {
		t.Fatalf("a note woke the orchestrator: %+v", next)
	}
	if _, err := s.Send("worker-session", w.Lead, "question", "Which timer?", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if next, _ := s.Next("lead-session"); len(next) != 2 || next[0].Type != "note" || next[1].Type != "question" {
		t.Fatalf("the note did not ride along: %+v", next)
	}
}

// An orchestrator idle in a plain wait is woken by a note: it kept to "do
// not poll", and nothing else would tell it. One holding a waiting set is
// not: the set promises one wake-up, when it resolves.
func TestANoteWakesAPlainWait(t *testing.T) {
	s, w := fixture(t)
	note := func() {
		t.Helper()
		if err := s.Mutate("lead-session", false, func(w *Workspace, _ *Member) error {
			_, err := AddMessage(w, "wash", w.Lead, "note", "Worker asked the owner", "", "", "")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(on ...string) {
		t.Helper()
		if err := s.Mutate("lead-session", false, func(w *Workspace, m *Member) error {
			m.Waiting, m.WaitingOn = "waiting for the worker's question", on
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	wait("some-assignment")
	note()
	if next, _ := s.Next("lead-session"); len(next) != 0 {
		t.Fatalf("a note broke a waiting set: %+v", next)
	}
	wait()
	if next, _ := s.Next("lead-session"); len(next) != 1 || next[0].Type != "note" || next[0].Recipient != w.Lead {
		t.Fatalf("a plain wait was not woken by the note: %+v", next)
	}
}
