package swarm

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func qaStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "qa.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Setup("lead", "codex", t.TempDir(), "QA", "")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Mutate("lead", true, func(w *Workspace, _ *Member) error {
		w.Plan = append(w.Plan, Node{ID: "K5", Title: "Timer", State: "todo"}, Node{ID: "R2", Title: "Review", State: "todo"})
		w.Members = append(w.Members, Member{ID: "writer", Session: "writer-session", State: "available", Lifetime: "resident", Role: "implementer", Node: "K5"}, Member{ID: "reviewer", Session: "reviewer-session", State: "available", Lifetime: "resident", Role: "reviewer", Node: "K5"}, Member{ID: "other", Session: "other-session", State: "available", Lifetime: "resident", Role: "reviewer", Node: "R2"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, w.Lead, s.path
}
func qaChange(s *Store, session string, u QAUpdate) error {
	return s.Mutate(session, false, func(w *Workspace, m *Member) error { _, err := UpdateQA(w, m, u); return err })
}
func TestQAConcurrentRepliesRevisionGuardsAndRecovery(t *testing.T) {
	s, lead, path := qaStore(t)
	if err := qaChange(s, "writer-session", QAUpdate{ID: "Q166", Action: "open", Node: "K5", Title: "Wakeup bound", Assignee: lead, Body: "Which bound?"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := qaChange(s, "writer-session", QAUpdate{ID: "Q166", Action: "reply", Body: fmt.Sprintf("Evidence %d", i)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	q := s.View("lead").QA[0]
	if len(q.Events) != 25 || q.Revision != 25 {
		t.Fatal(q)
	}
	stale := int64(1)
	before := s.Snapshot()
	if err := qaChange(s, "lead", QAUpdate{ID: q.ID, Action: "resolve", Expected: &stale, Evidence: "Tests pass"}); err == nil {
		t.Fatal("accepted stale resolution")
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("failed transition mutated history")
	}
	rev := q.Revision
	for _, session := range []string{"writer-session", "other-session"} {
		if err := qaChange(s, session, QAUpdate{ID: q.ID, Action: "resolve", Expected: &rev, Evidence: "Tests pass"}); err == nil {
			t.Fatal("unauthorized resolution", session)
		}
	}
	if err := qaChange(s, "reviewer-session", QAUpdate{ID: q.ID, Action: "resolve", Expected: &rev, Evidence: "timer regression passes; reviewed patch"}); err != nil {
		t.Fatal(err)
	}
	archived := s.View("lead").QA[0]
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(archived, reopened.View("lead").QA[0]) {
		t.Fatal("QA lost across backend recovery")
	}
	rev = archived.Revision
	if err := qaChange(reopened, "writer-session", QAUpdate{ID: q.ID, Action: "reopen", Expected: &rev, Body: "New failing input"}); err != nil {
		t.Fatal(err)
	}
	if reopened.View("lead").QA[0].State != "open" {
		t.Fatal("not reopened")
	}
}
func TestTransactionRollsBackAndDeduplicatesAcrossRecovery(t *testing.T) {
	s, lead, path := qaStore(t)
	raw := []byte(`{"request_id":"open"}`)
	invoke := func(store *Store) (any, error) {
		err := qaChange(store, "lead", QAUpdate{ID: "q", Action: "open", Node: "K5", Title: "Question", Assignee: lead, Body: "Body"})
		return map[string]bool{"ok": true}, err
	}
	if _, err := s.Transaction("lead", "report", "open", raw, false, invoke); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	if _, err := s.Transaction("lead", "report", "open", raw, false, invoke); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("retry mutated state")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Transaction("lead", "report", "open", raw, false, invoke); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transaction("lead", "report", "open", []byte(`{"different":true}`), false, invoke); err == nil {
		t.Fatal("accepted conflicting retry")
	}
	write := s.write
	s.write = func(string, []byte) error { return errors.New("disk full") }
	_, err = s.Transaction("lead", "report", "failed", []byte(`{}`), false, func(staged *Store) (any, error) {
		return nil, qaChange(staged, "lead", QAUpdate{ID: "q", Action: "reply", Body: "must roll back"})
	})
	s.write = write
	if err == nil || !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("persistence failure leaked state")
	}
	_, err = s.Transaction("lead", "report", "preview", []byte(`{}`), true, func(staged *Store) (any, error) {
		return nil, qaChange(staged, "lead", QAUpdate{ID: "q", Action: "reply", Body: "preview only"})
	})
	if err != nil || !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("preview mutated state")
	}
}

// A receipt belongs to its workspace: a new workspace on the same session
// that reuses a request_id runs the call, and the ended one's receipts go.
func TestReceiptsDoNotOutliveTheirWorkspace(t *testing.T) {
	s, _, _ := qaStore(t)
	calls := 0
	invoke := func(*Store) (any, error) { calls++; return map[string]int{"call": calls}, nil }
	raw := []byte(`{"request_id":"setup-1"}`)
	if _, err := s.Transaction("lead", "workspace_configure", "setup-1", raw, false, invoke); err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("lead", true, func(w *Workspace, _ *Member) error { w.State = "ended"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Setup("lead", "codex", "/project", "Next", "/project"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transaction("lead", "workspace_configure", "setup-1", raw, false, invoke); err != nil || calls != 2 {
		t.Fatalf("replayed the ended workspace's result: calls=%d %v", calls, err)
	}
	if n := len(s.Snapshot().Receipts); n != 1 {
		t.Fatalf("%d receipts, want only the live workspace's", n)
	}
}

func TestQAResolutionWaitsForHumanAndCannotForgeOwner(t *testing.T) {
	s, lead, _ := qaStore(t)
	if err := qaChange(s, "writer-session", QAUpdate{ID: "q", Action: "open", Node: "K5", Title: "Question", Assignee: lead, Body: "Question"}); err != nil {
		t.Fatal(err)
	}
	var questionID string
	if err := s.Mutate("lead", false, func(w *Workspace, m *Member) error {
		msg, err := AddMessage(w, m.ID, "human", "decision_request", "Choose bound", "", "", "")
		if err != nil {
			return err
		}
		questionID = msg.ID
		return LinkQA(w, "q", msg)
	}); err != nil {
		t.Fatal(err)
	}
	rev := s.View("lead").QA[0].Revision
	if err := qaChange(s, "lead", QAUpdate{ID: "q", Action: "resolve", Expected: &rev, Evidence: "claimed approval"}); err == nil {
		t.Fatal("resolved pending human choice")
	}
	if err := s.Mutate("lead", false, func(w *Workspace, m *Member) error {
		for i := range w.Messages {
			if w.Messages[i].ID == questionID {
				w.Messages[i].State = "answered"
			}
		}
		msg, err := AddMessage(w, "human", m.ID, "decision_response", "Use bound A", questionID, "", "")
		if err != nil {
			return err
		}
		return LinkQA(w, "q", msg)
	}); err != nil {
		t.Fatal(err)
	}
	q := s.View("lead").QA[0]
	if q.State == "resolved" || q.Events[len(q.Events)-1].Author != "human" {
		t.Fatal(q)
	}
	b, _ := json.Marshal(q)
	if len(b) == 0 {
		t.Fatal("missing QA JSON")
	}
}

// Ending a member withdraws its pending decision, and the QA thread it was
// asked on stops waiting for the owner.
func TestEndedMembersDecisionLeavesTheThreadOpen(t *testing.T) {
	s, _, _ := qaStore(t)
	if err := qaChange(s, "lead", QAUpdate{ID: "q", Action: "open", Node: "K5", Title: "Clock", Assignee: "writer", Body: "Which clock?"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("writer-session", false, func(w *Workspace, m *Member) error {
		msg, err := AddMessage(w, m.ID, "human", "decision_request", "Monotonic or wall?", "", "", "")
		if err != nil {
			return err
		}
		return LinkQA(w, "q", msg)
	}); err != nil {
		t.Fatal(err)
	}
	if q := QA(s.View("lead"), "q"); q.State != "awaiting-owner" {
		t.Fatalf("state %s", q.State)
	}
	if err := s.EndMember("lead", "writer", false); err != nil {
		t.Fatal(err)
	}
	if q := QA(s.View("lead"), "q"); q.State != "open" {
		t.Fatalf("thread still %s after its asker ended", q.State)
	}
}

// Found in the tally shakedown (pass 5): a reopened thread stayed with the
// member it was last assigned to, who may have ended, where the guide says
// it returns to the orchestrator. Its events name members, not ids, and an
// authority refusal says whose thread it is.
func TestReopenedQuestionReturnsToTheOrchestrator(t *testing.T) {
	s, lead, _ := qaStore(t)
	if err := s.Mutate("lead", true, func(w *Workspace, _ *Member) error {
		GetMember(w, "writer").Name = "K5 implementer"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := qaChange(s, "reviewer-session", QAUpdate{ID: "q", Action: "open", Node: "K5", Title: "Bound", Assignee: lead, Body: "Which bound?"}); err != nil {
		t.Fatal(err)
	}
	rev := s.View("lead").QA[0].Revision
	if err := qaChange(s, "lead", QAUpdate{ID: "q", Action: "assign", Expected: &rev, Assignee: "writer", Body: "Yours"}); err != nil {
		t.Fatal(err)
	}
	q := s.View("lead").QA[0]
	if body := q.Events[len(q.Events)-1].Body; body != "Yours\nNext responder: K5 implementer" {
		t.Fatalf("assign event = %q", body)
	}
	rev = q.Revision
	err := qaChange(s, "other-session", QAUpdate{ID: "q", Action: "block", Expected: &rev, Body: "no"})
	if err == nil || !strings.Contains(err.Error(), "reviewer on node K5") {
		t.Fatalf("authority error = %v", err)
	}
	if err := qaChange(s, "reviewer-session", QAUpdate{ID: "q", Action: "resolve", Expected: &rev, Evidence: "checked"}); err != nil {
		t.Fatal(err)
	}
	rev = s.View("lead").QA[0].Revision
	if err := qaChange(s, "reviewer-session", QAUpdate{ID: "q", Action: "reopen", Expected: &rev, Body: "regressed"}); err != nil {
		t.Fatal(err)
	}
	if q := s.View("lead").QA[0]; q.State != "open" || q.Assignee != lead {
		t.Fatalf("reopened thread: state %s assignee %s, want open and the orchestrator", q.State, q.Assignee)
	}
}

// The orchestrator trying to complete an assignment it gave out, to record
// acceptance, was told only "belongs to another member".
func TestCompletingSomeoneElsesAssignmentSaysWhose(t *testing.T) {
	s, _, _ := qaStore(t)
	if err := s.Mutate("lead", true, func(w *Workspace, _ *Member) error {
		GetMember(w, "writer").Name = "K5 implementer"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Assign("lead", "writer", "", "", "Fix it", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete("writer-session", a.ID, "done", false); err != nil {
		t.Fatal(err)
	}
	err = s.Complete("lead", a.ID, "accepted", false)
	if err == nil || !strings.Contains(err.Error(), "K5 implementer's") || !strings.Contains(err.Error(), "completed") || !strings.Contains(err.Error(), "needs no call") {
		t.Fatalf("err = %v", err)
	}
}

// A reply on a thread reaches the member who opened it, awake: DOC1's
// members blocked on a thread slept through rulings sent to someone else.
func TestNotifyQAWakesCreatorAndAssigneeOnly(t *testing.T) {
	s, lead, _ := qaStore(t)
	if err := qaChange(s, "writer-session", QAUpdate{ID: "Q1", Action: "open", Node: "K5", Title: "Bound", Assignee: "reviewer", Body: "Which bound?"}); err != nil {
		t.Fatal(err)
	}
	err := s.Mutate("lead", false, func(w *Workspace, m *Member) error {
		return NotifyQA(w, QA(w, "Q1"), m.ID, "Use 64")
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, msg := range s.View("lead").Messages {
		if msg.Thread == "Q1" && msg.Type == "answer" {
			got[msg.Recipient] = msg.State
		}
	}
	if len(got) != 2 || got["writer"] != "queued" || got["reviewer"] != "queued" || got[lead] != "" {
		t.Fatalf("notified %v", got)
	}
}

func TestQAFileRoundTripsWithShortenedPaths(t *testing.T) {
	s, _, _ := qaStore(t)
	if err := qaChange(s, "writer-session", QAUpdate{ID: "Q2", Action: "open", Node: "K5", Title: "Path", Assignee: "reviewer", Body: "See /home/u/proj/a.go and /home/u/notes"}); err != nil {
		t.Fatal(err)
	}
	w := s.View("lead")
	b, err := EncodeQAFile(QAFileFor(w, "Q2"), "/home/u/proj", "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "/home/u") || !strings.Contains(string(b), "./a.go") || !strings.Contains(string(b), "~/notes") {
		t.Fatal(string(b))
	}
	f, err := DecodeQAFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if f.Thread.ID != "Q2" || f.Thread.Events[0].Body != "See ./a.go and ~/notes" || f.Authors["writer"] == "" {
		t.Fatalf("%+v", f)
	}
	if _, err := DecodeQAFile(append([]byte(nil), b[:len(b)-10]...)); err == nil {
		t.Fatal("a truncated checkpoint decoded")
	}
	if _, err := DecodeQAFile([]byte("# notes\n")); err == nil || IsQAFile([]byte("# notes\n")) {
		t.Fatal("someone's Markdown read as a thread")
	}
}
