package swarm

import "testing"

func TestLinkAnswers(t *testing.T) {
	w := &Workspace{Messages: []Message{
		{ID: "mq1", Thread: "T", Type: "question", Sender: "impl", Created: 1000},
		{ID: "ma1", Thread: "T", Type: "answer", Sender: "arch", ReplyTo: "mq1", Created: 5000},
		{ID: "mq2", Thread: "T", Type: "question", Sender: "impl", Created: 9000},
		{ID: "ma2", Thread: "T", Type: "answer", Sender: "arch", Created: 12000},
		{ID: "ma3", Thread: "T", Type: "answer", Sender: "arch", ReplyTo: "mq1", Created: 15000},
		{ID: "other", Thread: "U", Type: "answer", Sender: "arch", Created: 12100},
	}}
	events := []QAEvent{
		{ID: "e0", Kind: "open", Author: "impl", Created: 500},
		{ID: "e1", Kind: "question", Author: "impl", Message: "mq1", Created: 1001},
		// Read back from a file: no message id, found by kind and time.
		{ID: "e2", Kind: "answer", Author: "arch", Created: 5002},
		{ID: "e3", Kind: "question", Author: "impl", Message: "mq2", Created: 9001},
		// Its message replies to nothing: the latest unanswered question is e3.
		{ID: "e4", Kind: "answer", Author: "arch", Message: "ma2", Created: 12001},
		// A second exact answer to e1, after e3 was asked.
		{ID: "e5", Kind: "answer", Author: "arch", Message: "ma3", Created: 15001},
		// No open question since e3 was claimed: falls back to the thread's opener, as a guess.
		{ID: "e6", Kind: "answer", Author: "arch", Created: 20000},
	}
	out := LinkAnswers(w, "T", events)
	type link struct {
		answers  string
		inferred bool
	}
	want := map[string]link{"e2": {"e1", false}, "e4": {"e3", true}, "e5": {"e1", false}, "e6": {"e0", true}}
	for _, e := range out {
		exp, isAnswer := want[e.ID]
		if !isAnswer {
			if e.Answers != "" {
				t.Errorf("%s: a %s is not an answer, got answers %q", e.ID, e.Kind, e.Answers)
			}
			continue
		}
		if e.Answers != exp.answers || e.Inferred != exp.inferred {
			t.Errorf("%s: answers %q inferred %v, want %q %v", e.ID, e.Answers, e.Inferred, exp.answers, exp.inferred)
		}
	}
	if events[2].Answers != "" {
		t.Error("the input was changed")
	}
}
