package agentd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
)

// Claude Code's AskUserQuestion as claude-agent-acp 0.81.2 sends it: each
// question a oneOf (or, multi-select, an array of anyOf) keyed question_<n>
// with its header as title and its text as description, and a
// question_<n>_custom text field beside it.
const askUserQuestionForm = `{"type":"object","properties":{
 "question_0":{"type":"string","title":"Clock","description":"Which clock source?","oneOf":[{"const":"Monotonic","title":"Monotonic","description":"Never goes back"},{"const":"Wall","title":"Wall"}]},
 "question_0_custom":{"type":"string","title":"Other","description":"Type your own answer, or add a note to the option you chose above (optional)."},
 "question_1":{"type":"array","title":"Targets","description":"Which targets?","items":{"anyOf":[{"const":"rv32","title":"rv32"},{"const":"x86","title":"x86"}]}},
 "question_1_custom":{"type":"string","title":"Other","description":"Type your own answer to add to your selection above (optional)."}}}`

func TestAskUserQuestionFormBecomesAQuestionSetAndBack(t *testing.T) {
	set, fields, err := elicitationQuestions(acp.ElicitRequest{Mode: "form", Message: "Please answer the following questions.", Schema: json.RawMessage(askUserQuestionForm)})
	if err != nil {
		t.Fatal(err)
	}
	if set.Title != "Please answer the following questions." || len(set.Questions) != 2 {
		t.Fatalf("set %+v", set)
	}
	clock, targets := set.Questions[0], set.Questions[1]
	if clock.Question != "Which clock source?" || clock.Header != "Clock" || clock.Multi || clock.NoText || len(clock.Options) != 2 || clock.Options[0].Description != "Never goes back" {
		t.Fatalf("clock %+v", clock)
	}
	if !targets.Multi || targets.NoText || len(targets.Options) != 2 {
		t.Fatalf("targets %+v", targets)
	}
	answers := map[string]swarm.QuestionAnswer{clock.ID: {Selected: []string{"Monotonic"}, Text: "CLOCK_MONOTONIC_RAW"}, targets.ID: {Selected: []string{"rv32", "x86"}}}
	if err := swarm.ValidateAnswers(set, answers); err != nil {
		t.Fatal(err)
	}
	content, err := elicitationContent(fields, answers)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(content, &got)
	if got["question_0"] != "Monotonic" || got["question_0_custom"] != "CLOCK_MONOTONIC_RAW" || len(got["question_1"].([]any)) != 2 || got["question_1_custom"] != nil {
		t.Fatalf("content %s", content)
	}
}

// With one question the message carries its text, not a title above it.
func TestASingleQuestionFormAsksTheMessage(t *testing.T) {
	form := `{"type":"object","properties":{"question_0":{"type":"string","title":"Go?","oneOf":[{"const":"Yes","title":"Yes"},{"const":"No","title":"No"}]},"question_0_custom":{"type":"string","title":"Other"}}}`
	set, _, err := elicitationQuestions(acp.ElicitRequest{Message: "Ship the timer today?", Schema: json.RawMessage(form)})
	if err != nil {
		t.Fatal(err)
	}
	if set.Title != "" || len(set.Questions) != 1 || set.Questions[0].Question != "Ship the timer today?" || set.Questions[0].Header != "Go?" {
		t.Fatalf("set %+v", set)
	}
}

// Any MCP-style form: booleans are yes/no, plain strings and numbers text.
func TestGenericFormsMapFieldByField(t *testing.T) {
	form := `{"type":"object","properties":{"confirm":{"type":"boolean","description":"Delete the branch?"},"count":{"type":"integer","description":"How many?"},"name":{"type":"string","description":"Name it"},"size":{"type":"string","enum":["S","M"]}}}`
	set, fields, err := elicitationQuestions(acp.ElicitRequest{Message: "Details", Schema: json.RawMessage(form)})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Questions) != 4 || set.Questions[0].Options[0].Label != "Yes" || len(set.Questions[1].Options) != 0 || !set.Questions[3].NoText {
		t.Fatalf("set %+v", set)
	}
	content, err := elicitationContent(fields, map[string]swarm.QuestionAnswer{"q1": {Selected: []string{"No"}}, "q2": {Text: "3"}, "q3": {Text: "timer"}, "q4": {Selected: []string{"M"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != `{"confirm":false,"count":3,"name":"timer","size":"M"}` {
		t.Fatalf("content %s", content)
	}
	if _, err := elicitationContent(fields, map[string]swarm.QuestionAnswer{"q2": {Text: "three"}}); err == nil {
		t.Fatal("a word accepted as a number")
	}
}

// The asker waits, with no timeout, until the human answers; a stopped turn
// cancels the question instead.
func TestAQuestionWaitsForTheHumanAndGoesWithTheTurn(t *testing.T) {
	withState(t, 1)
	h := &hosted{key: "acp:q", agent: "claude"}
	set := swarm.QuestionSet{Questions: []swarm.Question{{ID: "q1", Question: "Which?", Options: []swarm.Option{{Label: "A"}, {Label: "B"}}}}}
	done := make(chan questionReply, 1)
	go func() { r, _ := h.askQuestions(context.Background(), "elicitation", set); done <- r }()
	id := waitQuestion(t, "acp:q")
	if err := answerQuestion(agentproto.AgentQuestionAnswer{ID: id, Action: "accept", Answers: map[string]swarm.QuestionAnswer{"q1": {Selected: []string{"C"}}}}); err == nil {
		t.Fatal("an answer that is not an option was taken")
	}
	if err := answerQuestion(agentproto.AgentQuestionAnswer{ID: id, Action: "accept", Answers: map[string]swarm.QuestionAnswer{"q1": {Selected: []string{"B"}}}}); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.cancelled || r.declined || r.answers["q1"].Selected[0] != "B" {
		t.Fatalf("reply %+v", r)
	}
	if len(questionsFor("acp:q", "")) != 0 {
		t.Fatal("an answered question is still shown")
	}
	go func() { r, _ := h.askQuestions(context.Background(), "elicitation", set); done <- r }()
	waitQuestion(t, "acp:q")
	cancelQuestionsFor("acp:q", ReasonTurnCancelled)
	if r := <-done; !r.cancelled {
		t.Fatalf("a stopped turn's question was not cancelled: %+v", r)
	}
}

func waitQuestion(t *testing.T, key string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if q := questionsFor(key, ""); len(q) == 1 {
			return q[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the question never showed")
	return ""
}

// A background command outlives its turn; the member shows as waiting on
// it, not idle, until it finishes.
func TestBackgroundWorkShowsUntilItEnds(t *testing.T) {
	withState(t, 1)
	h := &hosted{key: "acp:bg", agent: "claude"}
	h.sessionReady.Store(true)
	// As acp's dispatch hands it over: the whole notification.
	send := func(update string) {
		h.trackAsyncTask(json.RawMessage(`{"sessionId":"s","update":` + update + `}`))
	}
	send(`{"sessionUpdate":"async_task_spawned","asyncTaskId":"t1","name":"Background task","description":"sleep 30 && echo ok","taskType":"local_bash"}`)
	send(`{"sessionUpdate":"async_task_spawned","asyncTaskId":"t2","description":"go test ./..."}`)
	m := swarm.Member{State: "available"}
	if a, d := workspaceMemberActivity(m, h, false); a != "background" || !strings.HasPrefix(d, "sleep 30") || !strings.Contains(d, "+1 more") {
		t.Fatalf("activity %s %q", a, d)
	}
	send(`{"sessionUpdate":"async_task_state_update","asyncTaskId":"t1","state":"completed"}`)
	send(`{"sessionUpdate":"async_task_state_update","asyncTaskId":"t2","state":"running"}`)
	if a, d := workspaceMemberActivity(m, h, false); a != "background" || d != "go test ./..." {
		t.Fatalf("activity %s %q", a, d)
	}
	send(`{"sessionUpdate":"async_task_state_update","asyncTaskId":"t2","state":"stopped"}`)
	if a, _ := workspaceMemberActivity(m, h, false); a != "idle" {
		t.Fatalf("activity %s after the work ended", a)
	}
}
