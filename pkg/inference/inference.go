// Package inference is the shared, one-shot inference contract used by wash
// apps. Provider configuration and execution live in com.wash.inference; this
// package deliberately exposes no credentials or provider-specific knobs.
package inference

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

const ServiceAppID = "com.wash.inference"

type Part struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Request struct {
	Purpose         string  `json:"purpose,omitempty"`
	Input           []Part  `json:"input"`
	Instructions    string  `json:"instructions,omitempty"`
	MaxOutputTokens int     `json:"max_output_tokens,omitempty"`
	Temperature     float64 `json:"temperature,omitempty"`
}

type Result struct {
	Text      string `json:"text"`
	Provider  string `json:"provider"`
	Model     string `json:"model,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Usage     Usage  `json:"usage,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

type Error struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

func (e Error) Error() string {
	if e.Msg == "" {
		return e.Code
	}
	return e.Code + ": " + e.Msg
}

type startWire struct {
	ID string `json:"id"`
	Request
}
type terminalWire struct {
	ID string `json:"id"`
	Result
	Code string `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`
}
type cancelWire struct {
	ID string `json:"id"`
}
type pendingResult struct {
	result Result
	err    error
}

// Client multiplexes asynchronous inference results over an existing sdk.Bus.
// Construct exactly once for a bus.
type Client struct {
	bus     *sdk.Bus
	mu      sync.Mutex
	pending map[string]chan pendingResult
}

func NewClient(bus *sdk.Bus) *Client {
	c := &Client{bus: bus, pending: make(map[string]chan pendingResult)}
	sdk.HandleFromVoid(bus, "inference.result", c.onResult)
	sdk.HandleFromVoid(bus, "inference.error", c.onError)
	// The acceptance acknowledgement is intentionally not exposed: Generate
	// waits for the terminal event, while the service remains cancellable.
	sdk.HandleFromVoid[terminalWire](bus, "inference.start_ok", func(*sdk.Conn, string, terminalWire, wire.Sender) error { return nil })
	sdk.HandleFromVoid[terminalWire](bus, "inference.cancel_ok", func(*sdk.Conn, string, terminalWire, wire.Sender) error { return nil })
	return c
}

func (c *Client) Generate(ctx context.Context, req Request) (Result, error) {
	id, err := requestID()
	if err != nil {
		return Result{}, err
	}
	ch := make(chan pendingResult, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	// EmitToBulk supplies the {kind:"inference.start", ...} envelope. Sending
	// startWire directly through Conn would omit kind, so the service bus would
	// (correctly) ignore the otherwise well-formed payload.
	if err := c.bus.EmitToBulk(wire.Recipient{AppID: ServiceAppID}, "inference.start", startWire{ID: id, Request: req}); err != nil {
		return Result{}, err
	}
	select {
	case got := <-ch:
		return got.result, got.err
	case <-ctx.Done():
		_ = c.bus.Conn().SendAppMsgTo(wire.Recipient{AppID: ServiceAppID}, map[string]any{"kind": "inference.cancel", "id": id})
		return Result{}, ctx.Err()
	}
}

func (c *Client) onResult(_ *sdk.Conn, _ string, msg terminalWire, from wire.Sender) error {
	if from.AppID != ServiceAppID {
		return errors.New("result from unexpected app")
	}
	c.deliver(msg.ID, pendingResult{result: msg.Result})
	return nil
}
func (c *Client) onError(_ *sdk.Conn, _ string, msg terminalWire, from wire.Sender) error {
	if from.AppID != ServiceAppID {
		return errors.New("error from unexpected app")
	}
	c.deliver(msg.ID, pendingResult{err: Error{Code: msg.Code, Msg: msg.Msg}})
	return nil
}
func (c *Client) deliver(id string, r pendingResult) {
	c.mu.Lock()
	ch := c.pending[id]
	c.mu.Unlock()
	if ch != nil {
		select {
		case ch <- r:
		default:
		}
	}
}
func requestID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "inf-" + hex.EncodeToString(b[:]), nil
}
