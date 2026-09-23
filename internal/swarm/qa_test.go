package swarm

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func qaStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "qa.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Setup("lead", "codex", t.TempDir(), "QA", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Mutate("lead", true, func(w *Workspace, _ *Member) error {
		w.Members = append(w.Members, Member{ID: "writer", Session: "writer-session", State: "available", Lifetime: "resident", Role: "implementer", Package: "K5"}, Member{ID: "reviewer", Session: "reviewer-session", State: "available", Lifetime: "resident", Role: "reviewer", Package: "K5"}, Member{ID: "other", Session: "other-session", State: "available", Lifetime: "resident", Role: "reviewer", Package: "R2"})
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
	if err := qaChange(s, "writer-session", QAUpdate{ID: "Q166", Action: "open", Package: "K5", Title: "Wakeup bound", Assignee: lead, Body: "Which bound?"}); err != nil {
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
		err := qaChange(store, "lead", QAUpdate{ID: "q", Action: "open", Package: "K5", Title: "Question", Assignee: lead, Body: "Body"})
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
func TestQAResolutionWaitsForHumanAndCannotForgeOwner(t *testing.T) {
	s, lead, _ := qaStore(t)
	if err := qaChange(s, "writer-session", QAUpdate{ID: "q", Action: "open", Package: "K5", Title: "Question", Assignee: lead, Body: "Question"}); err != nil {
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
