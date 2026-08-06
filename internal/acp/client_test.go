package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAgent is the other end of the wire: a scanner over what the client
// wrote, and a writer the test scripts replies on. No process, no adapter
// — the framing is the only thing under test here.
type fakeAgent struct {
	in  *bufio.Scanner
	out io.WriteCloser
	t   *testing.T
}

func newPair(t *testing.T, h SessionHandler) (*Client, *fakeAgent) {
	t.Helper()
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()

	c := NewClient(toClientR, toAgentW, h)
	sc := bufio.NewScanner(toAgentR)
	sc.Buffer(make([]byte, 0, 4096), maxLine)

	t.Cleanup(func() {
		c.Close()
		toAgentW.Close()
		toClientW.Close()
	})
	return c, &fakeAgent{in: sc, out: toClientW, t: t}
}

// next reads one message the client sent.
func (f *fakeAgent) next() message {
	f.t.Helper()
	if !f.in.Scan() {
		f.t.Fatalf("client sent nothing: %v", f.in.Err())
	}
	var m message
	if err := json.Unmarshal(f.in.Bytes(), &m); err != nil {
		f.t.Fatalf("client sent a non-message: %q", f.in.Bytes())
	}
	return m
}

func (f *fakeAgent) send(raw string) {
	f.t.Helper()
	if _, err := io.WriteString(f.out, raw+"\n"); err != nil {
		f.t.Fatalf("send: %v", err)
	}
}

// reply answers a request the client made.
func (f *fakeAgent) reply(id json.Number, result string) {
	f.send(`{"jsonrpc":"2.0","id":` + id.String() + `,"result":` + result + `}`)
}

type recordingHandler struct {
	mu      sync.Mutex
	updates []SessionNotification
	perm    func(RequestPermissionRequest) RequestPermissionResponse
}

func (h *recordingHandler) RequestPermission(_ context.Context, req RequestPermissionRequest) (RequestPermissionResponse, error) {
	if h.perm != nil {
		return h.perm(req), nil
	}
	return Cancelled(), nil
}

func (h *recordingHandler) SessionUpdate(_ context.Context, n SessionNotification) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.updates = append(h.updates, n)
}

func (h *recordingHandler) seen() []SessionNotification {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]SessionNotification(nil), h.updates...)
}

// The handshake pins the version and records what the agent can do.
func TestInitializeNegotiates(t *testing.T) {
	c, agent := newPair(t, &recordingHandler{})

	done := make(chan error, 1)
	go func() {
		_, err := c.Initialize(context.Background(),
			ClientCapabilities{Fs: FsCapability{ReadTextFile: true}, Terminal: true},
			Implementation{Name: "wash"})
		done <- err
	}()

	m := agent.next()
	if m.Method != MethodInitialize {
		t.Fatalf("first call = %q, want %q", m.Method, MethodInitialize)
	}
	var req InitializeRequest
	if err := json.Unmarshal(m.Params, &req); err != nil {
		t.Fatalf("params: %v", err)
	}
	if req.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocolVersion = %d, want %d", req.ProtocolVersion, ProtocolVersion)
	}
	// Advertising terminal is what makes the agent hand us its shell.
	if !req.ClientCapabilities.Terminal {
		t.Error("terminal capability not advertised")
	}

	agent.reply(*m.ID, `{"protocolVersion":1,"agentCapabilities":{"loadSession":true},"agentInfo":{"name":"codex-acp"}}`)

	if err := <-done; err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if !c.Capabilities().LoadSession {
		t.Error("loadSession capability lost")
	}
	if c.AgentInfo().Name != "codex-acp" {
		t.Errorf("agent name = %q", c.AgentInfo().Name)
	}
}

// An agent that speaks a version we do not must fail here, loudly, rather
// than in some subtly-wrong session later.
func TestInitializeRejectsForeignVersion(t *testing.T) {
	c, agent := newPair(t, &recordingHandler{})

	done := make(chan error, 1)
	go func() {
		_, err := c.Initialize(context.Background(), ClientCapabilities{}, Implementation{Name: "wash"})
		done <- err
	}()

	m := agent.next()
	agent.reply(*m.ID, `{"protocolVersion":2,"agentInfo":{"name":"future"}}`)

	err := <-done
	if err == nil {
		t.Fatal("accepted a protocol version wash cannot speak")
	}
	if !strings.Contains(err.Error(), "v2") {
		t.Errorf("error should name the version it saw: %v", err)
	}
}

// The permission request is the reason this package exists: the agent
// asks, the host answers, the answer reaches the agent.
func TestRequestPermissionRoundTrip(t *testing.T) {
	h := &recordingHandler{perm: func(req RequestPermissionRequest) RequestPermissionResponse {
		if req.ToolCall.Kind != ToolKindExecute {
			t.Errorf("tool kind = %q, want %q", req.ToolCall.Kind, ToolKindExecute)
		}
		for _, o := range req.Options {
			if o.Kind == OptionAllowAlways {
				return Selected(o.OptionID)
			}
		}
		return Cancelled()
	}}
	_, agent := newPair(t, h)

	agent.send(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{` +
		`"sessionId":"s1","toolCall":{"toolCallId":"t1","title":"git push","kind":"execute","status":"pending"},` +
		`"options":[{"optionId":"o1","name":"Allow once","kind":"allow_once"},` +
		`{"optionId":"o2","name":"Always allow","kind":"allow_always"}]}}`)

	m := agent.next()
	if m.ID == nil || m.ID.String() != "7" {
		t.Fatalf("reply id = %v, want 7", m.ID)
	}
	var res RequestPermissionResponse
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.Outcome.Outcome != OutcomeSelected || res.Outcome.OptionID != "o2" {
		t.Errorf("outcome = %+v, want selected o2", res.Outcome)
	}
}

// A host that implements neither fs nor terminals must say so in the
// protocol's own words, so the agent does that work itself instead of
// waiting on a capability we never had.
func TestUnsupportedCapabilityIsMethodNotFound(t *testing.T) {
	_, agent := newPair(t, &recordingHandler{})

	agent.send(`{"jsonrpc":"2.0","id":3,"method":"terminal/create","params":{"sessionId":"s1","command":"ls"}}`)

	m := agent.next()
	if m.Error == nil {
		t.Fatal("terminal/create answered despite no Terminals implementation")
	}
	if m.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", m.Error.Code)
	}
}

// The spec says an agent MUST NOT write non-ACP to stdout. Adapters do it
// anyway — a Node warning, a banner. One bad line must not take the
// session with it.
func TestNoiseOnStdoutIsSkipped(t *testing.T) {
	h := &recordingHandler{}
	_, agent := newPair(t, h)

	agent.send(`(node:1234) ExperimentalWarning: something`)
	agent.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}}`)

	deadline := time.After(2 * time.Second)
	for {
		if got := h.seen(); len(got) == 1 {
			if got[0].Update.SessionUpdate != UpdateAgentMessageChunk {
				t.Fatalf("update kind = %q", got[0].Update.SessionUpdate)
			}
			if got[0].Update.Content.String() != "hello" {
				t.Fatalf("text = %q", got[0].Update.Content.String())
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("the message after the noise never arrived")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A permission question can block on a human for 30 seconds. The read loop
// must keep running underneath it, or one pending question would freeze
// every other message on the session.
func TestSlowHandlerDoesNotStallTheWire(t *testing.T) {
	release := make(chan struct{})
	h := &recordingHandler{perm: func(RequestPermissionRequest) RequestPermissionResponse {
		<-release
		return Cancelled()
	}}
	_, agent := newPair(t, h)

	agent.send(`{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"s1","options":[]}}`)
	agent.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"still talking"}}}}`)

	deadline := time.After(2 * time.Second)
	for len(h.seen()) == 0 {
		select {
		case <-deadline:
			t.Fatal("an update behind a blocked permission handler never arrived")
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(release)

	m := agent.next()
	if m.ID == nil || m.ID.String() != "1" {
		t.Fatalf("permission reply id = %v", m.ID)
	}
}

// An adapter that dies mid-call must unblock the caller. A hung Prompt is
// a session that never ends and a roster row that never clears.
func TestAdapterExitUnblocksPendingCall(t *testing.T) {
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	defer toAgentW.Close()
	go io.Copy(io.Discard, toAgentR)

	c := NewClient(toClientR, toAgentW, &recordingHandler{})

	done := make(chan error, 1)
	go func() {
		_, err := c.Prompt(context.Background(), "s1", Text("hi"))
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	toClientW.Close() // the adapter exits

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Prompt returned success after the adapter exited")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt hung after the adapter exited")
	}
}

// The same `content` key is a single block on an agent_message_chunk and an
// array on a tool_call. Decoding it as one shape dropped every tool_call
// notification against claude-agent-acp 0.64.2 — silently, because the only
// evidence was a log line.
func TestContentDecodesBothShapes(t *testing.T) {
	h := &recordingHandler{}
	_, agent := newPair(t, h)

	agent.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"one"}}}}`)
	agent.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","kind":"execute","status":"pending","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}}`)

	got := waitFor(t, h, 2)
	if got[0].Update.Content.String() != "one" {
		t.Errorf("single block = %q", got[0].Update.Content.String())
	}
	if got[1].Update.SessionUpdate != UpdateToolCall {
		t.Fatalf("second update = %q, want tool_call", got[1].Update.SessionUpdate)
	}
	if got[1].Update.Content.String() != "ab" {
		t.Errorf("array content = %q, want %q", got[1].Update.Content.String(), "ab")
	}
	if got[1].Update.ToolCall.Kind != ToolKindExecute {
		t.Errorf("tool kind lost: %+v", got[1].Update.ToolCall)
	}
}

// One field with an unexpected shape must cost that field, not the whole
// notification. Dropping the message is data loss in a transcript, and
// invisible — which is how the array-content bug survived until a real
// adapter was run.
func TestOneBadFieldDoesNotDropTheUpdate(t *testing.T) {
	h := &recordingHandler{}
	_, agent := newPair(t, h)

	agent.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"t9","kind":"execute","content":42}}}`)

	got := waitFor(t, h, 1)
	if got[0].Update.SessionUpdate != UpdateToolCall {
		t.Errorf("discriminator lost: %q", got[0].Update.SessionUpdate)
	}
	if got[0].Update.ToolCall.ToolCallID != "t9" {
		t.Errorf("sibling field lost: %+v", got[0].Update.ToolCall)
	}
	if len(got[0].Update.Raw) == 0 {
		t.Error("Raw not preserved — nothing left to diagnose with")
	}
}

func waitFor(t *testing.T, h *recordingHandler, n int) []SessionNotification {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if got := h.seen(); len(got) >= n {
			return got
		}
		select {
		case <-deadline:
			t.Fatalf("only %d of %d updates arrived", len(h.seen()), n)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Notifications are a stream and must arrive in the order they were sent.
//
// The first cut dispatched each on its own goroutine, so two session/update
// messages could race — and a streamed reply would then append its chunks
// out of order and come out scrambled. Caught by TestContentDecodesBothShapes
// failing intermittently, which is the only reason it was found at all.
func TestNotificationsStayInOrder(t *testing.T) {
	h := &recordingHandler{}
	_, agent := newPair(t, h)

	const n = 60
	for i := 0; i < n; i++ {
		agent.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + itoa(i) + ` "}}}}`)
	}

	got := waitFor(t, h, n)
	for i := 0; i < n; i++ {
		want := itoa(i) + " "
		if got[i].Update.Content.String() != want {
			t.Fatalf("update %d = %q, want %q — the stream was reordered", i, got[i].Update.Content.String(), want)
		}
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// usage_update and session_info_update carry the two facts a status bar
// wants — how much context is gone, and what the agent decided this
// session is about. Both were arriving and being dropped on the floor.
//
// Shapes observed 2026-08-05 against codex-acp 1.1.9, not guessed.
func TestUsageAndTitleDecode(t *testing.T) {
	h := &recordingHandler{}
	_, agent := newPair(t, h)

	agent.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":14689,"size":258400}}}`)
	agent.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"session_info_update","title":"Reply with the single word: ok"}}}`)

	got := waitFor(t, h, 2)
	if got[0].Update.Used != 14689 || got[0].Update.Size != 258400 {
		t.Errorf("usage = %d/%d, want 14689/258400", got[0].Update.Used, got[0].Update.Size)
	}
	if got[1].Update.Title != "Reply with the single word: ok" {
		t.Errorf("title = %q", got[1].Update.Title)
	}
}

// A tool_call's content is nested one level deeper than a message's: its
// items are ToolCallContent wrappers carrying the real block under
// `content`. Only unwrapping the message form silently lost every image a
// TOOL produced — a screenshot, a chart — which looks like a rendering
// fault rather than a decoding one.
func TestToolCallImagesAreUnwrapped(t *testing.T) {
	h := &recordingHandler{}
	_, agent := newPair(t, h)

	agent.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{` +
		`"sessionUpdate":"tool_call","toolCallId":"t1","kind":"execute","status":"completed",` +
		`"content":[{"type":"content","content":{"type":"image","mimeType":"image/png","data":"QUJD"}},` +
		`{"type":"content","content":{"type":"text","text":"done"}}]}}}`)

	got := waitFor(t, h, 1)
	imgs := got[0].Update.Content.Images()
	if len(imgs) != 1 {
		t.Fatalf("%d images unwrapped, want 1 — a tool's image was lost", len(imgs))
	}
	if imgs[0].Data != "QUJD" || imgs[0].MimeType != "image/png" {
		t.Errorf("image = %+v", imgs[0])
	}
	// The nested text still reads through, so a tool's output is not lost
	// either.
	if got[0].Update.Content.String() != "done" {
		t.Errorf("text = %q", got[0].Update.Content.String())
	}
}
