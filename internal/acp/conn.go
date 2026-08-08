// Package acp is a client for the Agent Client Protocol — JSON-RPC 2.0
// over an agent adapter's stdio (docs/AGENT_APP.md §5).
//
// wash is the *client*: it launches the adapter, drives the session, and
// answers the adapter's callbacks (permission requests, file reads,
// terminals). Both directions ride one connection, so this file is a small
// bidirectional JSON-RPC peer rather than a request/response client.
//
// Framing, per the ACP transport spec: messages are delimited by newlines
// and MUST NOT contain embedded newlines. The spec also says an agent MUST
// NOT write anything to stdout that is not a valid ACP message — which
// adapters violate in practice (a stray banner, a Node warning), so a line
// that does not parse is logged and skipped rather than killing the
// session. A protocol this young is not worth dying over.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
)

// maxLine bounds one framed message. Generous — a session/load replay can
// carry a whole conversation — but finite, so a wedged adapter emitting an
// unterminated line cannot exhaust memory.
const maxLine = 8 << 20 // 8 MiB

// ErrClosed is returned by calls made after the peer's read loop has ended
// (adapter exited, stdio closed).
var ErrClosed = errors.New("acp: connection closed")

// Handler answers the requests an agent makes of its client. Every method
// may be called concurrently. Returning an error becomes a JSON-RPC error
// response; the agent decides what to do with it.
//
// Implementations live in the app that hosts sessions (agentd); this
// package deliberately knows nothing about roster rows or ask queues.
type Handler interface {
	// Handle dispatches one inbound request by method name. Unknown
	// methods must return ErrMethodNotFound so the agent can degrade.
	Handle(ctx context.Context, method string, params json.RawMessage) (any, error)
	// Notify dispatches one inbound notification (no reply expected).
	// Errors are logged, never returned to the agent.
	Notify(ctx context.Context, method string, params json.RawMessage)
}

// ErrMethodNotFound maps onto JSON-RPC -32601, which is how a client tells
// an agent "I do not implement that capability".
var ErrMethodNotFound = errors.New("acp: method not found")

// rpcError is a JSON-RPC error object, both directions.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("acp: rpc %d: %s", e.Code, e.Message) }

// message is one framed JSON-RPC value. Request, response and notification
// share a frame; which one it is depends on which fields are present.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.Number    `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Conn is a bidirectional JSON-RPC peer over one pair of streams.
type Conn struct {
	w  io.Writer
	wm sync.Mutex // serializes writes; one message per line, never interleaved

	h Handler

	mu      sync.Mutex
	seq     int64
	pending map[string]chan *message
	closed  bool
	closeMu sync.Once
	done    chan struct{}
	err     error

	// notes serializes inbound notifications.
	//
	// Requests are answered on their own goroutines — a permission
	// question blocks on a human for 30s and must not stall the wire —
	// but NOTIFICATIONS ARE A STREAM AND MUST STAY IN ORDER. Dispatching
	// each on its own goroutine let two session/update messages race, so
	// a streamed reply could append its chunks out of order and come out
	// scrambled. One queue, one worker, arrival order preserved.
	notes chan noteMsg
}

type noteMsg struct {
	method string
	params json.RawMessage
}

// NewConn wires a peer to an already-started adapter's stdout/stdin. The
// caller owns the process; this owns the framing.
func NewConn(r io.Reader, w io.Writer, h Handler) *Conn {
	c := &Conn{
		w:       w,
		h:       h,
		pending: map[string]chan *message{},
		done:    make(chan struct{}),
		notes:   make(chan noteMsg, 256),
	}
	go c.readLoop(r)
	go c.noteLoop()
	return c
}

// Done closes when the peer stops reading — adapter exit, stdio EOF, or a
// framing error. Err reports why.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is the reason the connection ended, or nil while it is live.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Call sends a request and waits for its response. The context bounds the
// wait; a cancelled context leaves the request outstanding on the agent's
// side, which is the caller's business (session/cancel exists for that).
func (c *Conn) Call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.seq++
	id := json.Number(fmt.Sprint(c.seq))
	ch := make(chan *message, 1)
	c.pending[id.String()] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id.String())
		c.mu.Unlock()
	}()

	if err := c.write(&message{JSONRPC: "2.0", ID: &id, Method: method, Params: mustRaw(params)}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closedErr()
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if result == nil || len(m.Result) == 0 {
			return nil
		}
		return json.Unmarshal(m.Result, result)
	}
}

// Notify sends a notification: no id, no reply, no waiting.
func (c *Conn) Notify(method string, params any) error {
	return c.write(&message{JSONRPC: "2.0", Method: method, Params: mustRaw(params)})
}

// Close ends the peer. Outstanding calls fail with ErrClosed.
func (c *Conn) Close() { c.finish(ErrClosed) }

func (c *Conn) closedErr() error {
	if err := c.Err(); err != nil {
		return err
	}
	return ErrClosed
}

func (c *Conn) finish(err error) {
	c.closeMu.Do(func() {
		c.mu.Lock()
		c.closed = true
		if c.err == nil {
			c.err = err
		}
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *Conn) write(m *message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// The frame is the line. Marshal never emits a raw newline in a JSON
	// value, so appending one is the whole framing.
	b = append(b, '\n')
	c.wm.Lock()
	defer c.wm.Unlock()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	_, err = c.w.Write(b)
	return err
}

// noteLoop delivers notifications one at a time, in arrival order. A slow
// handler backs up this queue rather than reordering the stream; the read
// loop keeps running, so requests (and their own goroutines) are
// unaffected.
func (c *Conn) noteLoop() {
	for {
		select {
		case <-c.done:
			return
		case n := <-c.notes:
			c.h.Notify(context.Background(), n.method, n.params)
		}
	}
}

func (c *Conn) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			// Spec-violating noise on stdout. Skip it — an adapter that
			// prints a warning must not take the session with it.
			log.Printf("acp: skip non-message line len=%d: %v", len(line), err)
			continue
		}
		c.dispatch(&m)
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.finish(err)

	// Anything still waiting hears about it, rather than blocking on a
	// channel nobody will ever send to.
	c.mu.Lock()
	for id, ch := range c.pending {
		select {
		case ch <- &message{Error: &rpcError{Code: -32000, Message: "connection closed"}}:
		default:
		}
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

func (c *Conn) dispatch(m *message) {
	// A response: id set, no method.
	if m.ID != nil && m.Method == "" {
		c.mu.Lock()
		ch := c.pending[m.ID.String()]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
		return
	}
	// A notification: method, no id. Queued rather than spawned, so the
	// stream stays in the order it arrived.
	if m.ID == nil {
		if c.h == nil {
			return
		}
		select {
		case c.notes <- noteMsg{method: m.Method, params: m.Params}:
		case <-c.done:
		}
		return
	}
	// A request from the agent. Answered on its own goroutine so a slow
	// handler — a permission question waiting on a human for 30s — cannot
	// stall the read loop and with it every other message on the session.
	id := *m.ID
	method, params := m.Method, m.Params
	go func() {
		var res any
		var err error
		if c.h == nil {
			err = ErrMethodNotFound
		} else {
			res, err = c.h.Handle(context.Background(), method, params)
		}
		reply := &message{JSONRPC: "2.0", ID: &id}
		switch {
		case errors.Is(err, ErrMethodNotFound):
			reply.Error = &rpcError{Code: -32601, Message: "method not found: " + method}
		case err != nil:
			reply.Error = &rpcError{Code: -32000, Message: err.Error()}
		default:
			reply.Result = mustRaw(res)
		}
		if werr := c.write(reply); werr != nil && !errors.Is(werr, ErrClosed) {
			log.Printf("acp: reply method=%s: %v", method, werr)
		}
	}()
}

// mustRaw marshals params/results, falling back to JSON null. A value that
// cannot be marshalled is a programming error on our side, and null is a
// legal frame the agent can reject — better than dropping the message.
func mustRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("acp: marshal %T: %v", v, err)
		return json.RawMessage("null")
	}
	return b
}
