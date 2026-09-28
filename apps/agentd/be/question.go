package agentd

// Question sets for the human: an adapter's form elicitation (Claude Code
// turns its AskUserQuestion tool into one), and a workspace member's
// decision_request. Both are shown in one panel above the asking session's
// composer and answered with agent_question_answer. Unlike a permission ask
// a question never times out: the asker waits for the human, and the
// question goes only when it is answered, declined, its turn is stopped or
// its session ends.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// questionReply is how a waiting asker hears the human: answers, or nil
// for a decline; cancelled when the question went without an answer.
type questionReply struct {
	answers   map[string]swarm.QuestionAnswer
	declined  bool
	cancelled bool
}

type pendingQuestion struct {
	agentproto.PendingQuestion
	asked time.Time
	reply chan questionReply
}

var (
	questionsMu  sync.Mutex
	questions    = map[string]*pendingQuestion{}
	questionSeq  uint64
	lastQuestion []agentproto.PendingQuestion
)

// publishQuestions puts every waiting question in the roster: the pending
// elicitations and the workspaces' pending decisions. Called when either
// changes, and on the workspace loop; publishes only when something a
// subscriber sees moved (ages aside, which are recomputed per push).
func publishQuestions() {
	now := time.Now()
	questionsMu.Lock()
	out := make([]agentproto.PendingQuestion, 0, len(questions))
	for _, p := range questions {
		q := p.PendingQuestion
		q.AgeMS = now.Sub(p.asked).Milliseconds()
		out = append(out, q)
	}
	questionsMu.Unlock()
	if workspaces != nil {
		out = append(out, workspaces.decisionQuestions(now)...)
	}
	slices.SortFunc(out, func(a, b agentproto.PendingQuestion) int { return int(b.AgeMS - a.AgeMS) })
	if svc == nil {
		// No state service (a unit test's workspace alone): nothing to push.
		questionsMu.Lock()
		lastQuestion = out
		questionsMu.Unlock()
		return
	}
	mutateStateIf(func(s *agentproto.State) bool {
		questionsMu.Lock()
		defer questionsMu.Unlock()
		if sameQuestions(lastQuestion, out) {
			return false
		}
		lastQuestion = out
		s.Questions = out
		return true
	})
}

func sameQuestions(a, b []agentproto.PendingQuestion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		x.AgeMS, y.AgeMS = 0, 0
		if !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

// askQuestions shows set to the human for session h and waits for the
// answer. ok is false when the question went unanswered (stopped, session
// ended, adapter gone).
func (h *hosted) askQuestions(ctx context.Context, source string, set swarm.QuestionSet) (questionReply, bool) {
	defer h.awaitingHuman("question")()
	questionsMu.Lock()
	questionSeq++
	p := &pendingQuestion{
		PendingQuestion: agentproto.PendingQuestion{ID: "question-" + itoa(questionSeq), RowKey: h.key, Agent: h.agent, Source: source, Set: set},
		asked:           time.Now(),
		reply:           make(chan questionReply, 1),
	}
	if _, name, _ := workspaceApprovalPolicy(h.sessionID); name != "" {
		p.WorkspaceName = name
	}
	questions[p.ID] = p
	questionsMu.Unlock()
	publishQuestions()
	notifyQuestion(p.PendingQuestion)
	log.Printf("agentd: question id=%s row=%s source=%s questions=%d", p.ID, h.key, source, len(set.Questions))
	select {
	case r := <-p.reply:
		return r, !r.cancelled
	case <-ctx.Done():
		cancelQuestionsFor(h.key, ReasonAgentExited)
		return questionReply{cancelled: true}, false
	}
}

// cancelQuestionsFor drops a session's waiting questions: its turn was
// stopped or it ended. The askers hear cancelled.
func cancelQuestionsFor(rowKey, why string) int {
	questionsMu.Lock()
	var dropped []*pendingQuestion
	for id, p := range questions {
		if p.RowKey == rowKey {
			delete(questions, id)
			dropped = append(dropped, p)
		}
	}
	questionsMu.Unlock()
	if len(dropped) == 0 {
		return 0
	}
	for _, p := range dropped {
		log.Printf("agentd: question cancelled id=%s row=%s reason=%q", p.ID, p.RowKey, why)
		p.reply <- questionReply{cancelled: true}
	}
	publishQuestions()
	return len(dropped)
}

// notifyQuestion raises the desktop notification that something waits for
// the human; clicking it focuses the asking session.
func notifyQuestion(q agentproto.PendingQuestion) {
	if notifyConn == nil {
		return
	}
	title := q.Set.Title
	if title == "" && len(q.Set.Questions) > 0 {
		title = q.Set.Questions[0].Question
	}
	who := q.Agent
	if q.WorkspaceName != "" {
		who = q.WorkspaceName
	}
	desktop(notifyConn, agentproto.Notify{Key: q.RowKey, Title: who + " needs you", Body: firstLine(title, 160), Level: wire.NotifyLevelInfo})
}

// notifyConn is the connection desktop notifications go out on, set once
// the service is up.
var notifyConn *sdk.Conn

// answerQuestion delivers the human's answer to a waiting question: an
// elicitation's asker, or a workspace decision.
func answerQuestion(req agentproto.AgentQuestionAnswer) error {
	if req.Action != "accept" && req.Action != "decline" {
		return errors.New("action must be accept or decline")
	}
	questionsMu.Lock()
	p := questions[req.ID]
	if p != nil {
		if err := swarm.ValidateAnswers(p.Set, req.Answers); err != nil {
			questionsMu.Unlock()
			return err
		}
		delete(questions, req.ID)
	}
	questionsMu.Unlock()
	if p != nil {
		p.reply <- questionReply{answers: req.Answers, declined: req.Action == "decline"}
		log.Printf("agentd: question answered id=%s action=%s", req.ID, req.Action)
		publishQuestions()
		return nil
	}
	if workspaces == nil {
		return errors.New("question no longer pending")
	}
	err := workspaces.store.AnswerDecision(req.ID, req.Answers, req.Action == "decline")
	if err == nil {
		workspaces.syncQADocuments()
		workspaces.signal()
		publishQuestions()
	}
	return err
}

func registerQuestionHandlers(bus *sdk.Bus, c *sdk.Conn) {
	notifyConn = c
	sdk.HandleFromVoid(bus, "agent_question_answer", func(_ *sdk.Conn, _ string, req agentproto.AgentQuestionAnswer, _ wire.Sender) error {
		if err := answerQuestion(req); err != nil {
			log.Printf("agentd: question answer id=%s: %v", req.ID, err)
		}
		return nil
	})
}

// decisionQuestions are the workspaces' pending decisions as questions,
// placed on the asking member's session row.
func (ws *workspaceService) decisionQuestions(now time.Time) []agentproto.PendingQuestion {
	var out []agentproto.PendingQuestion
	for _, w := range ws.store.Snapshot().Workspaces {
		if w.State == "ended" {
			continue
		}
		for _, msg := range w.Messages {
			if msg.Type != "decision_request" || msg.State != "recorded" || msg.Questions == nil {
				continue
			}
			q := agentproto.PendingQuestion{ID: msg.ID, Source: "decision", Set: *msg.Questions, WorkspaceName: w.Name, MemberID: msg.Sender, AgeMS: now.UnixMilli() - msg.Created}
			if m := swarm.GetMember(&w, msg.Sender); m != nil {
				if h := hostedBySession(m.Session); h != nil {
					q.RowKey, q.Agent = h.key, h.agent
				}
			}
			out = append(out, q)
		}
	}
	return out
}

// ---- form elicitation ----

// elicitField is how one form property maps onto a question: which
// question it feeds, and whether it is that question's choice or its text.
type elicitField struct {
	key      string
	question string
	kind     string // choice | multi | text | bool | number
	textFor  string // set on a "<key>_custom" text field: the question it adds words to
	values   map[string]any
}

// elicitationQuestions turns a form elicitation's schema into a question
// set. Properties keep their order. A string with enum or oneOf is a single
// choice, an array of them a multiple choice, a boolean yes or no, a plain
// string or number free text; a "<key>_custom" string beside a choice is
// that question's free text, the shape claude-agent-acp gives AskUserQuestion.
func elicitationQuestions(req acp.ElicitRequest) (swarm.QuestionSet, []elicitField, error) {
	set := swarm.QuestionSet{Title: strings.TrimSpace(req.Message)}
	var schema struct {
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	if err := json.Unmarshal(req.Schema, &schema); err != nil {
		return set, nil, fmt.Errorf("unreadable form schema: %w", err)
	}
	keys, props, err := orderedObject(schema.Properties)
	if err != nil {
		return set, nil, err
	}
	type prop struct {
		Type        string `json:"type"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Enum        []any  `json:"enum"`
		OneOf       []struct {
			Const       any    `json:"const"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"oneOf"`
		Items struct {
			Enum  []any `json:"enum"`
			AnyOf []struct {
				Const       any    `json:"const"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"anyOf"`
		} `json:"items"`
	}
	var fields []elicitField
	index := map[string]int{}
	parsed := map[string]prop{}
	isCustom := func(key string, p prop) bool {
		base := strings.TrimSuffix(key, "_custom")
		_, choice := parsed[base]
		return base != key && choice && p.Type == "string" && len(p.Enum) == 0 && len(p.OneOf) == 0
	}
	questionsIn := 0
	for _, key := range keys {
		var p prop
		if err := json.Unmarshal(props[key], &p); err != nil {
			return set, nil, fmt.Errorf("form field %s: %w", key, err)
		}
		if !isCustom(key, p) {
			questionsIn++
		}
		parsed[key] = p
	}
	for _, key := range keys {
		p := parsed[key]
		// A field's title is its short header; its description is the
		// question. With one question the message carries it instead.
		text := p.Description
		if text == "" && questionsIn == 1 && set.Title != "" {
			text, set.Title = set.Title, ""
		}
		if text == "" {
			text = p.Title
		}
		if text == "" {
			text = key
		}
		base := strings.TrimSuffix(key, "_custom")
		if isCustom(key, p) {
			if i, ok := index[base]; ok {
				fields = append(fields, elicitField{key: key, question: set.Questions[i].ID, kind: "text", textFor: set.Questions[i].ID})
				continue
			}
		}
		q := swarm.Question{ID: fmt.Sprintf("q%d", len(set.Questions)+1), Question: text}
		if p.Title != "" && p.Title != text && len(p.Title) <= 40 {
			q.Header = p.Title
		}
		f := elicitField{key: key, question: q.ID, values: map[string]any{}}
		option := func(value any, title, desc string) {
			label := title
			if label == "" {
				label = fmt.Sprint(value)
			}
			q.Options = append(q.Options, swarm.Option{Label: label, Description: desc})
			f.values[label] = value
		}
		switch {
		case p.Type == "array":
			f.kind, q.Multi = "multi", true
			for _, v := range p.Items.Enum {
				option(v, "", "")
			}
			for _, o := range p.Items.AnyOf {
				option(o.Const, o.Title, o.Description)
			}
			q.NoText = true
		case len(p.Enum) > 0 || len(p.OneOf) > 0:
			f.kind = "choice"
			for _, v := range p.Enum {
				option(v, "", "")
			}
			for _, o := range p.OneOf {
				option(o.Const, o.Title, o.Description)
			}
			q.NoText = true
		case p.Type == "boolean":
			f.kind = "bool"
			option(true, "Yes", "")
			option(false, "No", "")
			q.NoText = true
		case p.Type == "number" || p.Type == "integer":
			f.kind = "number"
		default:
			f.kind = "text"
		}
		index[key] = len(set.Questions)
		set.Questions = append(set.Questions, q)
		fields = append(fields, f)
	}
	// A choice with a custom field beside it takes words too.
	for _, f := range fields {
		if f.textFor != "" {
			for i := range set.Questions {
				if set.Questions[i].ID == f.textFor {
					set.Questions[i].NoText = false
				}
			}
		}
	}
	if len(set.Questions) == 0 {
		return set, nil, errors.New("the form has no fields")
	}
	return set, fields, swarm.ValidateQuestions(&set)
}

// elicitationContent writes the human's answers back as the form's content.
func elicitationContent(fields []elicitField, answers map[string]swarm.QuestionAnswer) (json.RawMessage, error) {
	content := map[string]any{}
	for _, f := range fields {
		a, ok := answers[f.question]
		if !ok {
			continue
		}
		switch f.kind {
		case "text":
			if t := strings.TrimSpace(a.Text); t != "" {
				content[f.key] = t
			}
		case "number":
			if t := strings.TrimSpace(a.Text); t != "" {
				n, err := strconv.ParseFloat(t, 64)
				if err != nil {
					return nil, fmt.Errorf("%q is not a number", t)
				}
				content[f.key] = n
			}
		case "choice", "bool":
			if len(a.Selected) == 1 {
				content[f.key] = f.values[a.Selected[0]]
			}
		case "multi":
			if len(a.Selected) > 0 {
				picked := []any{}
				for _, s := range a.Selected {
					picked = append(picked, f.values[s])
				}
				content[f.key] = picked
			}
		}
	}
	return json.Marshal(content)
}

// orderedObject decodes a JSON object's keys in the order they appear.
func orderedObject(raw json.RawMessage) ([]string, map[string]json.RawMessage, error) {
	values := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, values, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errors.New("form properties must be an object")
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		values[key] = v
	}
	return keys, values, nil
}

// questionsFor is a session's waiting questions (a workspace member's by its
// member id too, for a decision asked while it was offline).
func questionsFor(rowKey, member string) []agentproto.PendingQuestion {
	var out []agentproto.PendingQuestion
	for _, q := range lastPublishedQuestions() {
		if rowKey != "" && q.RowKey == rowKey || member != "" && q.MemberID == member {
			out = append(out, q)
		}
	}
	return out
}

// workspaceQuestions is every question waiting from w's members, each
// naming the member that asked.
func workspaceQuestions(w *swarm.Workspace) []agentproto.PendingQuestion {
	members := map[string]string{}
	for _, m := range w.Members {
		if h := hostedBySession(m.Session); h != nil {
			members[h.key] = m.ID
		}
	}
	var out []agentproto.PendingQuestion
	for _, q := range lastPublishedQuestions() {
		if q.MemberID == "" {
			q.MemberID = members[q.RowKey]
		}
		if q.MemberID != "" && swarm.GetMember(w, q.MemberID) != nil {
			out = append(out, q)
		}
	}
	return out
}

func lastPublishedQuestions() []agentproto.PendingQuestion {
	questionsMu.Lock()
	defer questionsMu.Unlock()
	return slices.Clone(lastQuestion)
}
