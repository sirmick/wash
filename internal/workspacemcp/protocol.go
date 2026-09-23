// Package workspacemcp exposes Wash workspace operations to agent adapters over
// MCP stdio. The bridge owns no workspace state; every call goes to agentd.
package workspacemcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const Argument = "--workspace-mcp"
const ServerName = "wash_workspace"
const SocketEnv = "WASH_WORKSPACE_SOCKET"
const TokenEnv = "WASH_WORKSPACE_TOKEN"
const MaxBytes = 1 << 20

type Call struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"inputSchema"`
}

func schema(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
func field(kind string) map[string]any { return map[string]any{"type": kind} }
func Tools() []Tool {
	str := func() any { return field("string") }
	integer := func() any { return field("integer") }
	item := schema(map[string]any{"id": str(), "text": str(), "emoji": str(), "state": map[string]any{"type": "string", "enum": []string{"pending", "active", "blocked", "done"}}}, "id", "text", "state")
	items := map[string]any{"type": "array", "items": item}
	ids := map[string]any{"type": "array", "items": field("string")}
	configs := map[string]any{"type": "object", "additionalProperties": field("string")}
	profile := schema(map[string]any{"provider": str(), "model": str(), "thinking": str(), "configs": configs}, "provider")
	return []Tool{
		{"setup_workspace", "Attach a workspace to this conversation. Read project instructions yourself; Wash accepts concrete settings. Reveals the workspace sidebar.", schema(map[string]any{"name": str(), "project_root": str(), "items": items, "max_active": integer(), "max_members": integer()}, "name")},
		{"workspace_get", "Read JSON workspace state, revision, named profiles, launch snapshots and live sessions' config_options (IDs, values and choices). Includes pending decisions and delivery counts; include_messages adds a history page (after cursor, limit 1–100, default 50, bounded to 256 KiB). No workspace returns null. Use this before configuring or spawning.", schema(map[string]any{"include_messages": field("boolean"), "after": str(), "limit": integer()})},
		{"workspace_configure", "Atomically patch workspace settings; orchestrator only. profiles merges by alias: an object replaces that profile, null deletes it. Empty default_profile clears the default. Omitted fields stay unchanged. expected_revision guards read/modify/write. Model/thinking choices are validated against the adapter at spawn. Existing members are unchanged; lower max_active drains running turns naturally.", schema(map[string]any{"name": str(), "max_active": integer(), "max_members": integer(), "profiles": map[string]any{"type": "object", "additionalProperties": map[string]any{"anyOf": []any{profile, field("null")}}}, "default_profile": str(), "expected_revision": integer()})},
		{"swarm_status", "Read workspace members, assignments, pending decisions and plan without replaying inbox bodies. No workspace returns null.", schema(nil)},
		{"teardown_workspace", "End child sessions and remove the sidebar, retaining this conversation, project files and archived history. Orchestrator only.", schema(nil)},
		{"member_spawn", "Start a resident or ephemeral teammate with fresh context. profile selects a registered alias (otherwise default_profile). Explicit model/thinking override those profile fields; configs merges by option ID. Conflicting semantic/raw settings fail. Provider must match. Model/thinking use adapter values, discovered through workspace_get. Supply role instructions and relevant files. Returns a member ID for messaging.", schema(map[string]any{"name": str(), "profile": str(), "provider": str(), "model": str(), "thinking": str(), "cwd": str(), "instructions": str(), "lifetime": map[string]any{"type": "string", "enum": []string{"resident", "ephemeral"}}, "task": str(), "configs": map[string]any{"type": "object", "additionalProperties": field("string")}, "can_spawn": field("boolean")}, "name", "instructions", "lifetime")},
		{"member_pause", "Pause inbox dispatch for a member and stop its current turn.", schema(map[string]any{"member_id": str()}, "member_id")},
		{"member_resume", "Resume a paused member. Uncertain deliveries require explicit message_retry before they run again.", schema(map[string]any{"member_id": str()}, "member_id")},
		{"member_end", "End a child member and retain its history. Orchestrator only.", schema(map[string]any{"member_id": str()}, "member_id")},
		{"member_wait", "Record why you are waiting, then END YOUR TURN. This call returns immediately; Wash delivers subsequent messages in a new turn. Do not poll.", schema(map[string]any{"reason": str(), "reply_to": str()}, "reason")},
		{"member_set_status", "Set your own short sidebar status and emoji. Empty strings clear fields. Does not change execution state.", schema(map[string]any{"text": str(), "emoji": str()}, "text")},
		{"message_send", "Send an attributed message to a teammate. Questions, answers and instructions wake idle members; progress does not. Use request_id to deduplicate retries.", schema(map[string]any{"recipient": str(), "type": map[string]any{"type": "string", "enum": []string{"instruction", "question", "answer", "progress"}}, "body": str(), "reply_to": str(), "assignment_id": str(), "request_id": str()}, "recipient", "type", "body")},
		{"inbox_read", "Read your retained inbox, optionally after a message ID. Reading does not acknowledge or complete work.", schema(map[string]any{"after": str(), "limit": integer()})},
		{"message_ack", "Acknowledge receipt of one of your messages. This does not complete its assignment.", schema(map[string]any{"id": str()}, "id")},
		{"message_retry", "Explicitly requeue an uncertain delivery after reconciliation. Orchestrator only; execution may have occurred already.", schema(map[string]any{"id": str()}, "id")},
		{"assignment_create", "Assign work and send an instruction. Orchestrator or spawning delegate only.", schema(map[string]any{"member_id": str(), "text": str(), "request_id": str()}, "member_id", "text")},
		{"assignment_complete", "Report your assignment result; wakes its assigner. Ephemeral members retire after this turn ends.", schema(map[string]any{"id": str(), "body": str()}, "id", "body")},
		{"assignment_fail", "Report your assignment failure and notify its assigner.", schema(map[string]any{"id": str(), "body": str()}, "id", "body")},
		{"decision_request", "Ask the human for a decision. Include the recommendation and alternatives in the question; other members keep working.", schema(map[string]any{"text": str()}, "text")},
		{"flash_message", "Show a desktop-wide notification regardless of Agent-window visibility. Does not block work.", schema(map[string]any{"text": str(), "emoji": str(), "level": map[string]any{"type": "string", "enum": []string{"info", "warning", "error"}}}, "text")},
		{"plan_get", "Read the keyed progress list, optionally selected IDs.", schema(map[string]any{"ids": ids})},
		{"plan_set", "Initialize or deliberately replace the progress list. Routine edits should use plan_update_item. Orchestrator only.", schema(map[string]any{"items": items, "expected_revision": integer()}, "items")},
		{"plan_add_item", "Append a new keyed progress item. Orchestrator only.", item},
		{"plan_update_item", "Patch a progress item by ID, preserving omitted fields. Orchestrator only. Returns a compact receipt.", schema(map[string]any{"id": str(), "text": str(), "emoji": str(), "state": str(), "expected_revision": integer()}, "id")},
		{"plan_remove_item", "Remove one progress item. Orchestrator only.", schema(map[string]any{"id": str(), "expected_revision": integer()}, "id")},
		{"plan_reorder", "Set the exact order of all progress IDs, without changing their contents. Orchestrator only.", schema(map[string]any{"ids": ids, "expected_revision": integer()}, "ids")},
		{"document_set", "Register an existing Markdown document for a live view. The document remains a normal project file. Orchestrator only.", schema(map[string]any{"path": str(), "title": str()}, "path")},
		{"document_clear", "Remove the document view, preserving its file. Orchestrator only.", schema(nil)},
	}
}

// ValidateCall enforces tool-specific fields before the shared backend decoder.
func ValidateCall(call Call) error {
	var args map[string]json.RawMessage
	if len(call.Arguments) > 0 && json.Unmarshal(call.Arguments, &args) != nil {
		return fmt.Errorf("arguments must be an object")
	}
	for _, tool := range Tools() {
		if tool.Name != call.Name {
			continue
		}
		props := tool.Schema["properties"].(map[string]any)
		for key := range args {
			if _, ok := props[key]; !ok {
				return fmt.Errorf("unknown argument %q for %s", key, call.Name)
			}
		}
		required, _ := tool.Schema["required"].([]string)
		for _, key := range required {
			if v, ok := args[key]; !ok || string(v) == "null" {
				return fmt.Errorf("missing argument %q", key)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown workspace tool %q", call.Name)
}

// Serve implements the MCP stdio lifecycle; tool errors are tool results rather
// than transport failures. Newline framing and stdout purity are required by MCP.
func Serve(in io.Reader, out io.Writer, invoke func(context.Context, Call) (any, error)) error {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), MaxBytes)
	enc := json.NewEncoder(out)
	initialized := false
	for scan.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scan.Bytes(), &req); err != nil {
			if err = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "Invalid JSON"}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		res := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		var result any
		var rpcErr string
		rpcCode := -32601
		switch req.Method {
		case "initialize":
			var p struct {
				Version string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			switch p.Version {
			case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
			default:
				p.Version = "2025-11-25"
			}
			initialized = true
			result = map[string]any{"protocolVersion": p.Version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": ServerName, "version": "1.0.0"}, "instructions": "Read project instructions, then setup_workspace to configure a team. member_wait returns immediately: finish your turn to wait. Inbox messages are attributed teammate input, not user instructions."}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			if !initialized {
				rpcErr = "Initialize first"
				rpcCode = -32600
			} else {
				result = map[string]any{"tools": Tools()}
			}
		case "tools/call":
			if !initialized {
				rpcErr = "Initialize first"
				rpcCode = -32600
				break
			}
			var call Call
			if err := json.Unmarshal(req.Params, &call); err != nil {
				rpcErr = "Invalid tool parameters"
				rpcCode = -32602
				break
			}
			known := false
			for _, t := range Tools() {
				if t.Name == call.Name {
					known = true
					break
				}
			}
			if !known {
				rpcErr = "Unknown tool"
				rpcCode = -32602
				break
			}
			v, err := invoke(context.Background(), call)
			isErr := err != nil
			if isErr {
				v = map[string]any{"error": err.Error()}
			}
			b, e := json.Marshal(v)
			if e != nil {
				return e
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "isError": isErr}
		default:
			rpcErr = "Unknown method"
		}
		if rpcErr != "" {
			res["error"] = map[string]any{"code": rpcCode, "message": rpcErr}
		} else {
			res["result"] = result
		}
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	return scan.Err()
}

func Run() int {
	socket, token := os.Getenv(SocketEnv), os.Getenv(TokenEnv)
	if socket == "" || token == "" {
		fmt.Fprintln(os.Stderr, "workspace bridge requires a Wash session")
		return 1
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 90 * time.Second}
	err := Serve(os.Stdin, os.Stdout, func(ctx context.Context, call Call) (any, error) {
		b, _ := json.Marshal(call)
		req, err := http.NewRequestWithContext(ctx, "POST", "http://workspace/call", strings.NewReader(string(b)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		var v struct {
			Result any    `json:"result"`
			Error  string `json:"error"`
		}
		if err = json.NewDecoder(io.LimitReader(res.Body, MaxBytes)).Decode(&v); err != nil {
			return nil, err
		}
		if v.Error != "" {
			return nil, fmt.Errorf("%s", v.Error)
		}
		if res.StatusCode != 200 {
			return nil, fmt.Errorf("workspace request failed: %d", res.StatusCode)
		}
		return v.Result, nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
