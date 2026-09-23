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
			result = map[string]any{"protocolVersion": p.Version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": ServerName, "version": APIVersion}, "instructions": Instructions}
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
