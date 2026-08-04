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
			if got[0].Update.Content.Text != "hello" {
				t.Fatalf("text = %q", got[0].Update.Content.Text)
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
