package main

// These scripts exercise the actual injected stdio MCP executable. The fake
// never calls agentd's private HTTP API or writes its workspace store.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ownerAssignment and peerAssignment are the assignments this member is
// finishing once the owner, or a peer, answers.
var ownerAssignment, peerAssignment string

var workspaceBridge struct {
	Command string
	Args    []string
	Env     []struct{ Name, Value string }
}

func captureWorkspace(m map[string]any) {
	if os.Getenv("WASH_FAKE_WORKSPACE") != "1" {
		return
	}
	params, _ := m["params"].(map[string]any)
	if id, ok := params["sessionId"].(string); ok {
		sessionID = id
	}
	servers, _ := params["mcpServers"].([]any)
	for _, v := range servers {
		s, _ := v.(map[string]any)
		if s["name"] == "wash_workspace" {
			b, _ := json.Marshal(s)
			_ = json.Unmarshal(b, &workspaceBridge)
		}
	}
}
func workspaceCall(name string, args any) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, workspaceBridge.Command, workspaceBridge.Args...)
	cmd.Env = os.Environ()
	for _, v := range workspaceBridge.Env {
		cmd.Env = append(cmd.Env, v.Name+"="+v.Value)
	}
	var in bytes.Buffer
	enc := json.NewEncoder(&in)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fake-test", "version": "1"}}})
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	cmd.Stdin = &in
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	var envelope struct {
		Result struct {
			Content []struct{ Text string }
			IsError bool
		}
		Error any
	}
	for dec.Decode(&envelope) == nil {
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("RPC: %v", envelope.Error)
	}
	if len(envelope.Result.Content) == 0 {
		return nil, fmt.Errorf("no MCP response")
	}
	text := envelope.Result.Content[0].Text
	if envelope.Result.IsError {
		return nil, fmt.Errorf("MCP: %s", text)
	}
	// Passed on as written: decoded into any it would re-encode with its
	// keys sorted, and the order is part of what agents are shown.
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("MCP result is not JSON: %s", text)
	}
	return json.RawMessage(text), nil
}

// hangTurn is workspaceScript's answer for a turn that must never end.
const hangTurn = "\x00hang"

func workspaceScript(raw string) (string, bool) {
	if os.Getenv("WASH_FAKE_WORKSPACE") != "1" {
		return "", false
	}
	if strings.HasPrefix(raw, "workspace ") {
		fields := strings.SplitN(strings.TrimPrefix(raw, "workspace "), " ", 2)
		args := any(map[string]any{})
		if len(fields) > 1 {
			if err := json.Unmarshal([]byte(fields[1]), &args); err != nil {
				return err.Error(), true
			}
		}
		if fields[0] == "flash_message" {
			time.Sleep(300 * time.Millisecond)
		}
		result, err := workspaceCall(fields[0], args)
		if err != nil {
			return "WORKSPACE_ERROR " + err.Error(), true
		}
		b, _ := json.Marshal(result)
		return "WORKSPACE_RESULT " + string(b), true
	}
	if strings.HasPrefix(raw, "Wash inbox: ") {
		// One turn carries a batch: the JSON array after the first line.
		_, data, _ := strings.Cut(raw, "\n")
		var batch []struct {
			ID, Body, Type string
			Assignment     string `json:"assignment_id"`
			Thread         string `json:"thread_id"`
			Sender         string
			Node           string `json:"node"`
		}
		if err := json.Unmarshal([]byte(data), &batch); err != nil {
			return err.Error(), true
		}
		handled := []string{}
		for _, msg := range batch {
			if msg.Body == "ASK_PERMISSION" {
				return "", false
			}
			// An assignment that hangs, or wakes its member into a turn of
			// its own afterwards (runTurn).
			if msg.Assignment != "" && msg.Type == "instruction" && strings.Contains(msg.Body, "HANG_TURN") {
				return hangTurn, true
			}
			if msg.Assignment != "" && msg.Type == "instruction" && strings.Contains(msg.Body, "SELF_TURN") {
				selfTurnNext.Store(true)
			}
			// ASK_OWNER: ask the owner two structured questions and wait
			// (the call blocks this member); the answers arrive as the
			// next turn's decision_response, which completes the work.
			if msg.Assignment != "" && msg.Type == "instruction" && strings.Contains(msg.Body, "ASK_OWNER") {
				ownerAssignment = msg.Assignment
				_, err := workspaceCall("decision_request", map[string]any{"title": "Gamma", "questions": []any{
					map[string]any{"id": "word", "question": "Which word goes in gamma.txt?", "options": []any{map[string]any{"label": "gamma"}, map[string]any{"label": "GAMMA"}}, "recommended": "gamma"},
					map[string]any{"id": "note", "question": "Anything to add?"},
				}})
				if err != nil {
					return err.Error(), true
				}
				handled = append(handled, msg.ID)
				return "ASKED_OWNER " + strings.Join(handled, ","), true
			}
			if msg.Type == "decision_response" && ownerAssignment != "" {
				_, err := workspaceCall("assignment_update", map[string]any{"updates": []any{map[string]any{"action": "complete", "id": ownerAssignment, "body": "Owner answered: " + msg.Body}}})
				if err != nil {
					return err.Error(), true
				}
				ownerAssignment = ""
				handled = append(handled, msg.ID)
				continue
			}
			// FAIL_ON_PURPOSE: report the assignment failed.
			if msg.Assignment != "" && msg.Type == "instruction" && strings.Contains(msg.Body, "FAIL_ON_PURPOSE") {
				_, err := workspaceCall("assignment_update", map[string]any{"updates": []any{map[string]any{"action": "fail", "id": msg.Assignment, "body": "Failed on purpose"}}})
				if err != nil {
					return err.Error(), true
				}
				handled = append(handled, msg.ID)
				continue
			}
			// ASK_PEER <member> <thread>: open a QA thread on this member's
			// node with another member and wait; the answer completes it.
			if msg.Assignment != "" && msg.Type == "instruction" && strings.Contains(msg.Body, "ASK_PEER") {
				f := strings.Fields(msg.Body[strings.Index(msg.Body, "ASK_PEER"):])
				if len(f) >= 4 {
					peerAssignment = msg.Assignment
					_, err := workspaceCall("message_send", map[string]any{"recipient": f[1], "type": "question", "body": "Is alpha lower case?", "qa": map[string]any{"id": f[2], "action": "open", "node": f[3], "title": "Alpha case"}})
					if err != nil {
						return err.Error(), true
					}
					handled = append(handled, msg.ID)
					continue
				}
			}
			if msg.Type == "answer" && msg.Thread != "" && peerAssignment != "" {
				_, err := workspaceCall("assignment_update", map[string]any{"updates": []any{map[string]any{"action": "complete", "id": peerAssignment, "body": "Peer answered on " + msg.Thread}}})
				if err != nil {
					return err.Error(), true
				}
				peerAssignment = ""
				handled = append(handled, msg.ID)
				continue
			}
			// A member's first turn is its brief, with the task at the end.
			if msg.Assignment != "" && msg.Type == "instruction" && strings.HasSuffix(strings.TrimSpace(msg.Body), "WAIT_FOR_ANSWER") {
				_, err := workspaceCall("message_send", map[string]any{"recipient": msg.Sender, "type": "question", "body": "Which clock?", "assignment_id": msg.Assignment})
				if err != nil {
					return err.Error(), true
				}
				// The reply can arrive before this turn yields. It must remain in
				// the durable inbox, and the ephemeral assignment must stay alive.
				time.Sleep(300 * time.Millisecond)
			} else if msg.Assignment != "" && msg.Type == "answer" {
				_, err := workspaceCall("assignment_update", map[string]any{"updates": []any{map[string]any{"action": "complete", "id": msg.Assignment, "body": "Completed after an inbox reply"}}})
				if err != nil {
					return err.Error(), true
				}
			} else if msg.Assignment != "" && msg.Type == "instruction" {
				_, err := workspaceCall("assignment_update", map[string]any{"updates": []any{map[string]any{"action": "complete", "id": msg.Assignment, "body": "Fixture completed: " + msg.Body}}})
				if err != nil {
					return err.Error(), true
				}
			} else if msg.Type == "question" {
				_, err := workspaceCall("message_send", map[string]any{"recipient": msg.Sender, "type": "answer", "body": "Fixture answer", "thread_id": msg.Thread, "reply_to": msg.ID, "assignment_id": msg.Assignment})
				if err != nil {
					return err.Error(), true
				}
			}
			handled = append(handled, msg.ID)
		}
		_, err := workspaceCall("member_update", map[string]any{"waiting": map[string]any{"reason": "Waiting for inbox"}})
		if err != nil {
			return err.Error(), true
		}
		return "INBOX_HANDLED " + strings.Join(handled, ","), true
	}
	return "", false
}

// Workspace-only fixture options model the dependency between a model and its
// supported thinking levels. Other fixture scripts keep their existing wire.
var workspaceModel = "fast"
var workspaceThinking = "low"

func initialConfigOptions() []any {
	if isOpencode() {
		return opencodeConfigOptions()
	}
	if os.Getenv("WASH_FAKE_WORKSPACE") == "1" {
		return workspaceConfigOptions()
	}
	return []any{configState("model", "fast")["configOptions"].([]any)[0]}
}
func workspaceConfigOptions() []any {
	levels := []any{map[string]any{"value": "low", "name": "Low"}}
	if workspaceModel == "smart" {
		levels = append(levels, map[string]any{"value": "high", "name": "High"})
	}
	return []any{
		map[string]any{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": workspaceModel, "options": []any{map[string]any{"value": "fast", "name": "Fast"}, map[string]any{"value": "smart", "name": "Smart"}}},
		map[string]any{"id": "reasoning_effort", "name": "Thinking", "category": "thought_level", "type": "select", "currentValue": workspaceThinking, "options": levels},
	}
}
func workspaceSetConfig(id, value string) (any, error) {
	switch id {
	case "model":
		if value != "fast" && value != "smart" {
			return nil, fmt.Errorf("unsupported model")
		}
		workspaceModel = value
		workspaceThinking = "low"
	case "reasoning_effort":
		if value != "low" && !(value == "high" && workspaceModel == "smart") {
			return nil, fmt.Errorf("unsupported thinking for model")
		}
		workspaceThinking = value
	default:
		return nil, fmt.Errorf("unsupported setting")
	}
	return map[string]any{"configOptions": workspaceConfigOptions()}, nil
}
