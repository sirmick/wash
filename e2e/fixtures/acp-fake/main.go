// acp-fake — a deterministic ACP agent for the e2e suite.
//
// The real thing needs an API key, a network, money and patience, and it
// answers differently every time — none of which belongs in a test. This
// speaks the same wire instead, from a recorded script.
//
// The frames it replays were CAPTURED from real adapters (claude-agent-acp
// 0.64.2 and codex-acp 1.1.9) with the conformance test's tracer, not
// invented — so a shape that drifts upstream fails the conformance check
// first, and this fixture is updated from a fresh capture rather than
// from someone's memory of the spec.
//
// Installed on PATH under the name of the adapter it stands in for
// (`codex-acp`), so nothing in production has to know it exists.
//
// Behaviour is driven by the prompt text, which keeps the e2e readable:
//
//	"ask"    → requests permission, then reports what was answered
//	anything → a short markdown reply with a tool call
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var sessionID = "fake-session-1"

// A 1x1 transparent PNG. Small enough to inline, real enough that the
// browser decodes it — a broken <img> would still "be visible" to a
// naive assertion, so the test checks naturalWidth instead.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 8<<20)
	out := bufio.NewWriter(os.Stdout)

	for in.Scan() {
		var m map[string]any
		if err := json.Unmarshal(in.Bytes(), &m); err != nil {
			continue
		}
		method, _ := m["method"].(string)
		id, hasID := m["id"]

		// A response to something WE asked (the permission request). The
		// turn is blocked on it, exactly as a real agent's turn is.
		if hasID && method == "" {
			deliver(fmt.Sprint(id), m["result"])
			continue
		}

		switch method {
		case "initialize":
			reply(out, id, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"loadSession":        true,
					"promptCapabilities": map[string]any{"image": true, "embeddedContext": true},
				},
				"agentInfo":   map[string]any{"name": "acp-fake", "title": "Fake Agent", "version": "1.0.0"},
				"authMethods": []any{},
			})

		case "session/new":
			reply(out, id, map[string]any{"sessionId": sessionID})

		case "session/load":
			// A load MUST replay the conversation before it answers.
			// Reproducing that ordering is the point of covering it here.
			notify(out, chunk("Earlier in this session we discussed **resuming**."))
			reply(out, id, nil)

		case "session/prompt":
			go runTurn(out, m)

		case "session/cancel":
			cancelled.Store(true)

		default:
			if hasID {
				replyErr(out, id, -32601, "method not found: "+method)
			}
		}
	}
}

var (
	cancelled atomic.Bool
	reqSeq    atomic.Int64
)

func runTurn(out *bufio.Writer, m map[string]any) {
	cancelled.Store(false)
	text := promptText(m)
	id := m["id"]

	notify(out, update(map[string]any{
		"sessionUpdate": "usage_update", "used": 14689, "size": 258400,
	}))

	if strings.Contains(text, "ask") {
		// A permission request, with the option kinds a real adapter
		// sends — this is what the sidebar and the inline row render.
		rid := reqSeq.Add(1) + 1000
		request(out, rid, "session/request_permission", map[string]any{
			"sessionId": sessionID,
			"toolCall": map[string]any{
				"toolCallId": "call-1",
				"title":      "echo hello > /tmp/wash-e2e-fake",
				"kind":       "execute",
				"rawInput":   map[string]any{"command": "echo hello > /tmp/wash-e2e-fake"},
			},
			"options": []any{
				map[string]any{"optionId": "reject", "name": "Deny", "kind": "reject_once"},
				map[string]any{"optionId": "allow", "name": "Allow Once", "kind": "allow_once"},
				map[string]any{"optionId": "allow_always", "name": "Always Allow", "kind": "allow_always"},
			},
		})
		// Block until answered, like a real turn does. Reporting the
		// outcome back into the transcript is what lets an e2e assert
		// that clicking Allow actually reached the agent, rather than
		// just that a button disappeared.
		res := await(fmt.Sprint(rid))
		notify(out, chunk("Permission outcome: "+outcomeOf(res)+"."))
		reply(out, id, map[string]any{"stopReason": "end_turn"})
		return
	}

	// Streamed in pieces, deliberately: chunk boundaries mid-word are
	// where ordering bugs and lost whitespace hide.
	for _, part := range []string{"## Heading\n\nHello ", "from the ", "fake agent.\n\n- one\n- two\n"} {
		if cancelled.Load() {
			reply(out, id, map[string]any{"stopReason": "cancelled"})
			return
		}
		notify(out, chunk(part))
	}
	// A table and an image, so the renderer's two richest paths are
	// covered by something other than looking at them.
	notify(out, chunk("\n| Adapter | Kind |\n| --- | ---: |\n| codex | npm |\n| claude | npm |\n"))
	notify(out, update(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content": []any{map[string]any{
			"type": "image", "mimeType": "image/png", "data": onePixelPNG,
		}},
	}))
	notify(out, update(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "t-1", "kind": "read",
		"title": "README.md", "status": "completed",
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
	}))
	notify(out, update(map[string]any{"sessionUpdate": "session_info_update", "title": "Fake conversation"}))
	reply(out, id, map[string]any{"stopReason": "end_turn"})
}

// pending correlates our outbound requests with their answers.
var (
	pendingMu sync.Mutex
	pending   = map[string]chan any{}
)

func await(id string) any {
	ch := make(chan any, 1)
	pendingMu.Lock()
	pending[id] = ch
	pendingMu.Unlock()
	select {
	case v := <-ch:
		return v
	case <-time.After(60 * time.Second):
		return nil
	}
}

func deliver(id string, result any) {
	pendingMu.Lock()
	ch := pending[id]
	delete(pending, id)
	pendingMu.Unlock()
	if ch != nil {
		ch <- result
	}
}

// outcomeOf renders what the client chose, so it lands in the transcript
// where a test can see it.
func outcomeOf(res any) string {
	m, _ := res.(map[string]any)
	o, _ := m["outcome"].(map[string]any)
	kind, _ := o["outcome"].(string)
	if kind == "selected" {
		if id, ok := o["optionId"].(string); ok {
			return id
		}
	}
	if kind == "" {
		return "none"
	}
	return kind
}

func promptText(m map[string]any) string {
	params, _ := m["params"].(map[string]any)
	blocks, _ := params["prompt"].([]any)
	var sb strings.Builder
	for _, b := range blocks {
		if bm, ok := b.(map[string]any); ok {
			if s, ok := bm["text"].(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return strings.ToLower(sb.String())
}

func chunk(text string) map[string]any {
	return update(map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
	})
}

func update(u map[string]any) map[string]any {
	return map[string]any{"sessionId": sessionID, "update": u}
}

// One writer, one line per frame: the transport's whole framing rule.
var wmu = make(chan struct{}, 1)

func write(out *bufio.Writer, v any) {
	wmu <- struct{}{}
	defer func() { <-wmu }()
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(out, "%s\n", b)
	_ = out.Flush()
}

func reply(out *bufio.Writer, id any, result any) {
	write(out, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func replyErr(out *bufio.Writer, id any, code int, msg string) {
	write(out, map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg}})
}

func notify(out *bufio.Writer, params any) {
	write(out, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": params})
}

func request(out *bufio.Writer, id int64, method string, params any) {
	write(out, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}
