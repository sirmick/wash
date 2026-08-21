// The typed client surface: what agentd calls, and what it must implement
// to be called back (docs/AGENT_APP.md §5).
package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
)

// SessionHandler is the minimum an ACP host must implement. Two methods,
// deliberately: everything else an agent can ask for is an optional
// capability below, and a host that does not implement one simply does not
// advertise it — the agent then does that work itself.
type SessionHandler interface {
	// RequestPermission is the whole point. Returning Cancelled() is the
	// ACP spelling of the ask queue's `defer`: no answer from wash, the
	// agent falls back to whatever it would have done alone.
	RequestPermission(ctx context.Context, req RequestPermissionRequest) (RequestPermissionResponse, error)
	// SessionUpdate is the agent narrating: message chunks, tool calls,
	// status changes. Never blocks the wire — it runs on its own goroutine.
	SessionUpdate(ctx context.Context, n SessionNotification)
}

// FileSystem is optional: implement it and advertise ClientCapabilities.Fs
// to have the agent read and write through wash instead of directly.
type FileSystem interface {
	ReadTextFile(ctx context.Context, req ReadTextFileRequest) (ReadTextFileResponse, error)
	WriteTextFile(ctx context.Context, req WriteTextFileRequest) error
}

// Elicitations is optional: implement it to answer elicitation/create,
// the agent asking the human a structured question. A host that does not
// implement it answers -32601 and the agent asks in prose instead.
type Elicitations interface {
	Elicit(ctx context.Context, req ElicitRequest) (ElicitResponse, error)
}

// Terminals is optional: implement it and advertise
// ClientCapabilities.Terminal to have the agent's shell commands run in
// wash terminals (§8) rather than inside the adapter.
type Terminals interface {
	CreateTerminal(ctx context.Context, req CreateTerminalRequest) (CreateTerminalResponse, error)
	TerminalOutput(ctx context.Context, ref TerminalRef) (TerminalOutputResponse, error)
	WaitForExit(ctx context.Context, ref TerminalRef) (ExitStatus, error)
	KillTerminal(ctx context.Context, ref TerminalRef) error
	ReleaseTerminal(ctx context.Context, ref TerminalRef) error
}

// Client drives one adapter process.
type Client struct {
	conn *Conn
	init InitializeResponse
}

// NewClient wires a handler to an already-started adapter's streams. It
// does not initialize — call Initialize next, and check the negotiated
// version before opening sessions.
func NewClient(r io.Reader, w io.Writer, h SessionHandler) *Client {
	c := &Client{}
	c.conn = NewConn(r, w, &dispatch{h: h})
	return c
}

// Done closes when the adapter's stdio ends. Err says why.
func (c *Client) Done() <-chan struct{} { return c.conn.Done() }
func (c *Client) Err() error            { return c.conn.Err() }
func (c *Client) Close()                { c.conn.Close() }

// Capabilities is what the agent said it can do, valid after Initialize.
func (c *Client) Capabilities() AgentCapabilities { return c.init.AgentCapabilities }

// AgentInfo identifies the adapter, valid after Initialize.
func (c *Client) AgentInfo() Implementation { return c.init.AgentInfo }

// AuthMethods is non-empty when the agent needs Authenticate before
// sessions can be created.
func (c *Client) AuthMethods() []AuthMethod { return c.init.AuthMethods }

// Initialize performs the handshake and pins the protocol version.
//
// Negotiation, per the spec: we send the latest version we support, the
// agent answers with that or the latest *it* supports, and a client that
// cannot speak the answer closes the connection. wash speaks exactly one
// version, so anything else is a hard error here rather than a subtly
// wrong session later.
func (c *Client) Initialize(ctx context.Context, caps ClientCapabilities, info Implementation) (InitializeResponse, error) {
	req := InitializeRequest{ProtocolVersion: ProtocolVersion, ClientCapabilities: caps, ClientInfo: info}
	var res InitializeResponse
	if err := c.conn.Call(ctx, MethodInitialize, req, &res); err != nil {
		return res, err
	}
	if res.ProtocolVersion != ProtocolVersion {
		return res, fmt.Errorf("acp: agent speaks protocol v%d, wash speaks v%d", res.ProtocolVersion, ProtocolVersion)
	}
	c.init = res
	return res, nil
}

// Authenticate runs the named auth method. Only needed when Initialize
// reported auth methods.
func (c *Client) Authenticate(ctx context.Context, methodID string) error {
	return c.conn.Call(ctx, MethodAuthenticate, map[string]string{"methodId": methodID}, nil)
}

// NewSession opens a session rooted at an absolute cwd. The response
// carries what the agent will let you change later (modes, models), which
// is why the whole thing is returned rather than just the id.
func (c *Client) NewSession(ctx context.Context, cwd string, mcp []McpServer) (NewSessionResponse, error) {
	if mcp == nil {
		mcp = []McpServer{}
	}
	var res NewSessionResponse
	err := c.conn.Call(ctx, MethodSessionNew, NewSessionRequest{Cwd: cwd, McpServers: mcp}, &res)
	return res, err
}

// SetMode switches the session's approval/sandbox preset.
//
// This is ACP's own answer to "stop asking me": it changes what the AGENT
// requires approval for, rather than teaching wash to auto-answer on its
// behalf. The two are different in a way that matters — a mode change is
// visible to the agent and reversible from either side, where a blanket
// wash-side allow is invisible to it.
func (c *Client) SetMode(ctx context.Context, sessionID, modeID string) error {
	return c.conn.Call(ctx, MethodSessionSetMode, SetModeRequest{SessionID: sessionID, ModeID: modeID}, nil)
}

// SetConfigOption changes one of the agent's generic per-session
// settings. Returns the agent's full config state, which is authoritative
// — a set can change more than the one option (picking a model can reset
// the reasoning effort it supports).
func (c *Client) SetConfigOption(ctx context.Context, sessionID, configID, value string) (SetConfigOptionResponse, error) {
	var res SetConfigOptionResponse
	err := c.conn.Call(ctx, MethodSessionSetConfig,
		SetConfigOptionRequest{SessionID: sessionID, ConfigID: configID, Value: value}, &res)
	return res, err
}

// LoadSession resumes a previous session by id. The agent replays the
// whole conversation as session/update notifications *before* answering,
// so the handler sees the history arrive first — which is exactly what a
// transcript wants, and why this call can take a while.
func (c *Client) LoadSession(ctx context.Context, sessionID, cwd string, mcp []McpServer) (LoadSessionResponse, error) {
	var res LoadSessionResponse
	if !c.init.AgentCapabilities.LoadSession {
		return res, fmt.Errorf("acp: agent %q cannot load sessions", c.init.AgentInfo.Name)
	}
	if mcp == nil {
		mcp = []McpServer{}
	}
	// The response destination used to be nil, which threw away the
	// settings block a resumed session needs. An adapter that sends
	// nothing leaves res zero, so this is strictly additive.
	err := c.conn.Call(ctx, MethodSessionLoad, LoadSessionRequest{SessionID: sessionID, Cwd: cwd, McpServers: mcp}, &res)
	return res, err
}

// Prompt runs one turn and returns when the agent stops. Everything the
// agent does in between arrives on the handler.
func (c *Client) Prompt(ctx context.Context, sessionID string, blocks ...ContentBlock) (PromptResponse, error) {
	var res PromptResponse
	err := c.conn.Call(ctx, MethodSessionPrompt, PromptRequest{SessionID: sessionID, Prompt: blocks}, &res)
	return res, err
}

// Cancel aborts the running turn. It is a notification: the agent
// acknowledges by ending the turn with StopCancelled, not by replying.
func (c *Client) Cancel(sessionID string) error {
	return c.conn.Notify(MethodSessionCancel, CancelNotification{SessionID: sessionID})
}

// dispatch turns wire method names into handler calls. Optional
// capabilities are type assertions, so a host that does not implement them
// answers -32601 and the agent degrades on its own.
type dispatch struct{ h SessionHandler }

func (d *dispatch) Notify(ctx context.Context, method string, params json.RawMessage) {
	if method != MethodSessionUpdate {
		log.Printf("acp: ignoring notification method=%s", method)
		return
	}
	var n SessionNotification
	if err := json.Unmarshal(params, &n); err != nil {
		log.Printf("acp: session/update decode: %v", err)
		return
	}
	n.Update.Raw = params
	d.h.SessionUpdate(ctx, n)
}

func (d *dispatch) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case MethodRequestPermission:
		var req RequestPermissionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return d.h.RequestPermission(ctx, req)

	case MethodReadTextFile:
		fs, ok := d.h.(FileSystem)
		if !ok {
			return nil, ErrMethodNotFound
		}
		var req ReadTextFileRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return fs.ReadTextFile(ctx, req)

	case MethodWriteTextFile:
		fs, ok := d.h.(FileSystem)
		if !ok {
			return nil, ErrMethodNotFound
		}
		var req WriteTextFileRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, fs.WriteTextFile(ctx, req)

	case MethodElicitationCreate:
		e, ok := d.h.(Elicitations)
		if !ok {
			return nil, ErrMethodNotFound
		}
		var req ElicitRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return e.Elicit(ctx, req)

	case MethodTerminalCreate:
		t, ok := d.h.(Terminals)
		if !ok {
			return nil, ErrMethodNotFound
		}
		var req CreateTerminalRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return t.CreateTerminal(ctx, req)

	case MethodTerminalOutput, MethodTerminalWait, MethodTerminalKill, MethodTerminalRelease:
		t, ok := d.h.(Terminals)
		if !ok {
			return nil, ErrMethodNotFound
		}
		var ref TerminalRef
		if err := json.Unmarshal(params, &ref); err != nil {
			return nil, err
		}
		switch method {
		case MethodTerminalOutput:
			return t.TerminalOutput(ctx, ref)
		case MethodTerminalWait:
			return t.WaitForExit(ctx, ref)
		case MethodTerminalKill:
			return nil, t.KillTerminal(ctx, ref)
		default:
			return nil, t.ReleaseTerminal(ctx, ref)
		}
	}
	return nil, ErrMethodNotFound
}
