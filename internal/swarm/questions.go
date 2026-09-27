package swarm

import (
	"errors"
	"fmt"
	"strings"
)

// A question set is what an agent asks the owner: several questions at once,
// each with options to pick (one, or several) and room for the owner's own
// words, any of them skippable. It is the shape of a workspace
// decision_request and of an adapter's form elicitation (Claude Code's
// AskUserQuestion), so one panel answers both. A list of questions in prose
// was what the owner found hard to answer.

// QuestionSet is one ask of the owner.
type QuestionSet struct {
	Title     string     `json:"title,omitempty"`
	Questions []Question `json:"questions"`
}

// Question is one question in a set.
type Question struct {
	ID string `json:"id"`
	// Header is a short label for the question (a chip, a tab).
	Header   string   `json:"header,omitempty"`
	Question string   `json:"question"`
	Options  []Option `json:"options,omitempty"`
	// Multi lets the owner pick several options.
	Multi bool `json:"multi,omitempty"`
	// Recommended is the label of the option the asker recommends.
	Recommended string `json:"recommended,omitempty"`
	// NoText says only the options are answers (a form field with no free
	// text); a decision_request question always takes the owner's words.
	NoText bool `json:"no_text,omitempty"`
}

// Option is one choice.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionAnswer is the owner's answer to one question: the options picked,
// their own words, or neither (skipped).
type QuestionAnswer struct {
	Selected []string `json:"selected,omitempty"`
	Text     string   `json:"text,omitempty"`
}

// ValidateQuestions checks a question set an agent sent, and gives each
// question an id if it had none.
func ValidateQuestions(set *QuestionSet) error {
	if len(set.Title) > 200 {
		return errors.New("title is at most 200 bytes")
	}
	if len(set.Questions) == 0 || len(set.Questions) > 10 {
		return errors.New("ask 1–10 questions")
	}
	seen := map[string]bool{}
	for i := range set.Questions {
		q := &set.Questions[i]
		if q.ID == "" {
			q.ID = fmt.Sprintf("q%d", i+1)
		}
		switch {
		case !ValidProfileName(q.ID) || seen[q.ID]:
			return fmt.Errorf("question %d: id must be unique letters, digits, - or _", i+1)
		case !ValidText(q.Question, 1000):
			return fmt.Errorf("question %s: the question is 1–1000 bytes", q.ID)
		case len(q.Header) > 40:
			return fmt.Errorf("question %s: header is at most 40 bytes", q.ID)
		case len(q.Options) > 12:
			return fmt.Errorf("question %s: at most 12 options", q.ID)
		}
		seen[q.ID] = true
		labels := map[string]bool{}
		for _, o := range q.Options {
			if !ValidText(o.Label, 200) || len(o.Description) > 500 || labels[o.Label] {
				return fmt.Errorf("question %s: option labels are unique, 1–200 bytes, descriptions at most 500", q.ID)
			}
			labels[o.Label] = true
		}
		if q.Recommended != "" && !labels[q.Recommended] {
			return fmt.Errorf("question %s: recommended %q is not one of its options", q.ID, q.Recommended)
		}
		if q.NoText && len(q.Options) == 0 {
			return fmt.Errorf("question %s: a question with no options takes text", q.ID)
		}
	}
	return nil
}

// ValidateAnswers checks the owner's answers against the set: picks are
// among the options, one unless the question is multi.
func ValidateAnswers(set QuestionSet, answers map[string]QuestionAnswer) error {
	byID := map[string]Question{}
	for _, q := range set.Questions {
		byID[q.ID] = q
	}
	for id, a := range answers {
		q, ok := byID[id]
		if !ok {
			return fmt.Errorf("no question %q", id)
		}
		if len(a.Selected) > 1 && !q.Multi {
			return fmt.Errorf("question %s takes one choice", id)
		}
		for _, s := range a.Selected {
			found := false
			for _, o := range q.Options {
				found = found || o.Label == s
			}
			if !found {
				return fmt.Errorf("question %s has no option %q", id, s)
			}
		}
		if a.Text != "" && q.NoText {
			return fmt.Errorf("question %s takes a choice, not text", id)
		}
		if len(a.Text) > 8000 {
			return fmt.Errorf("answer to %s is at most 8000 bytes", id)
		}
	}
	return nil
}

// QuestionsMarkdown renders a question set for a transcript or a QA thread.
func QuestionsMarkdown(set QuestionSet) string {
	var b strings.Builder
	if set.Title != "" {
		b.WriteString(set.Title + "\n\n")
	}
	for i, q := range set.Questions {
		fmt.Fprintf(&b, "%d. %s\n", i+1, q.Question)
		for _, o := range q.Options {
			mark := ""
			if o.Label == q.Recommended {
				mark = " (recommended)"
			}
			if o.Description != "" {
				fmt.Fprintf(&b, "   - %s%s: %s\n", o.Label, mark, o.Description)
			} else {
				fmt.Fprintf(&b, "   - %s%s\n", o.Label, mark)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// AnswersMarkdown renders the owner's answers, question by question, word
// for word: what the asker reads and what the QA thread keeps.
func AnswersMarkdown(set QuestionSet, answers map[string]QuestionAnswer) string {
	var b strings.Builder
	for i, q := range set.Questions {
		a := answers[q.ID]
		fmt.Fprintf(&b, "%d. %s\n   → ", i+1, q.Question)
		switch {
		case len(a.Selected) == 0 && strings.TrimSpace(a.Text) == "":
			b.WriteString("(skipped)")
		case len(a.Selected) == 0:
			b.WriteString(strings.TrimSpace(a.Text))
		default:
			b.WriteString(strings.Join(a.Selected, ", "))
			if t := strings.TrimSpace(a.Text); t != "" {
				b.WriteString(" — " + t)
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// AnswerDecision records the owner's answers to a pending decision_request
// (or that they declined), delivers them to the asker as its next message,
// and puts them in the decision's QA thread word for word.
func (s *Store) AnswerDecision(id string, answers map[string]QuestionAnswer, declined bool) error {
	return s.change(func(st *State) error {
		for i := range st.Workspaces {
			w := &st.Workspaces[i]
			if w.State == "ended" {
				continue
			}
			for j := range w.Messages {
				q := &w.Messages[j]
				if q.ID != id || q.Type != "decision_request" {
					continue
				}
				if q.State != "recorded" {
					return errors.New("decision no longer pending")
				}
				set := QuestionSet{}
				if q.Questions != nil {
					set = *q.Questions
				}
				if err := ValidateAnswers(set, answers); err != nil {
					return err
				}
				body := "The owner declined to answer."
				if !declined {
					body = "The owner answered:\n\n" + AnswersMarkdown(set, answers)
				}
				q.State = "answered"
				reply, err := AddMessage(w, "human", q.Sender, "decision_response", body, q.ID, "", "")
				if err != nil {
					return err
				}
				if !declined {
					reply.Answers = answers
				}
				if q.Thread != "" {
					return LinkQA(w, q.Thread, reply)
				}
				return nil
			}
		}
		return errors.New("decision no longer pending")
	})
}
