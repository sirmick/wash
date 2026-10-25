package swarm

// LinkAnswers says which event each answer in a thread answers. Read from
// the record rather than stored, so every thread, archived or not, gets
// it: an answer's message replies to the question's message (reply_to).
// An event read back from its file has lost its message id and finds its
// message by thread, kind and time. An answer whose message replies to
// nothing is set to the latest earlier question still unanswered, and
// marked inferred, so the window can say "probably". The events are
// returned as a copy; the thread itself is unchanged.
func LinkAnswers(w *Workspace, thread string, events []QAEvent) []QAEvent {
	out := append([]QAEvent(nil), events...)
	byID := map[string]*Message{}
	var inThread []*Message
	for i := range w.Messages {
		m := &w.Messages[i]
		byID[m.ID] = m
		if m.Thread == thread {
			inThread = append(inThread, m)
		}
	}
	abs := func(d int64) int64 {
		if d < 0 {
			return -d
		}
		return d
	}
	messageOf := func(e *QAEvent) *Message {
		if e.Message != "" {
			return byID[e.Message]
		}
		var best *Message
		for _, m := range inThread {
			if m.Type != e.Kind || abs(m.Created-e.Created) > 2000 {
				continue
			}
			if best == nil || abs(m.Created-e.Created) < abs(best.Created-e.Created) {
				best = m
			}
		}
		return best
	}
	eventByMessage := map[string]string{}
	for i := range out {
		if m := messageOf(&out[i]); m != nil {
			eventByMessage[m.ID] = out[i].ID
		}
	}
	answered := map[string]bool{}
	for i := range out {
		e := &out[i]
		e.Answers, e.Inferred = "", false
		if e.Kind != "answer" {
			continue
		}
		if m := messageOf(e); m != nil && m.ReplyTo != "" {
			if target, ok := eventByMessage[m.ReplyTo]; ok && target != e.ID {
				e.Answers = target
				answered[target] = true
			}
		}
	}
	// Exact answers have claimed their questions; the rest take the latest
	// earlier question nobody has answered, from someone else.
	for i := range out {
		e := &out[i]
		if e.Kind != "answer" || e.Answers != "" {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			q := &out[j]
			if asks(q.Kind) && q.Author != e.Author && !answered[q.ID] {
				e.Answers, e.Inferred = q.ID, true
				answered[q.ID] = true
				break
			}
		}
	}
	return out
}

// asks says whether an event kind puts a question.
func asks(kind string) bool {
	return kind == "open" || kind == "question" || kind == "decision_request"
}
