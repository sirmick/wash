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
	var result any
	err = json.Unmarshal([]byte(envelope.Result.Content[0].Text), &result)
	if envelope.Result.IsError {
		return nil, fmt.Errorf("MCP: %v", result)
	}
	return result, err
}
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
	if strings.HasPrefix(raw, "Wash inbox message from ") {
		_, data, _ := strings.Cut(raw, "\n")
		var msg struct {
			ID, Body, Type string
			Assignment     string `json:"assignment_id"`
			Thread         string `json:"thread_id"`
			Sender         string
		}
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			return err.Error(), true
		}
		if msg.Body == "ASK_PERMISSION" {
			return "", false
		}
		if _, err := workspaceCall("inbox_ack", map[string]any{"ids": []string{msg.ID}}); err != nil {
			return err.Error(), true
		}
		if msg.Assignment != "" && msg.Type == "instruction" && msg.Body == "WAIT_FOR_ANSWER" {
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
		_, err := workspaceCall("member_update", map[string]any{"waiting": map[string]any{"reason": "Waiting for inbox"}})
		if err != nil {
			return err.Error(), true
		}
		return "INBOX_HANDLED " + msg.ID, true
	}
	return "", false
}

// Workspace-only fixture options model the dependency between a model and its
// supported thinking levels. Other fixture scripts keep their existing wire.
var workspaceModel = "fast"
var workspaceThinking = "low"

func initialConfigOptions() []any {
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
