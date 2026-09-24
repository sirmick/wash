package workspacemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestProtocolDiscoveryAndToolErrors(t *testing.T) {
	in := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"member_update","arguments":{"waiting":{"reason":"instructions"}}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"workspace_end","arguments":{}}}
`
	var out bytes.Buffer
	calls := 0
	err := Serve(strings.NewReader(in), &out, Tools(), func(_ context.Context, c Call) (any, error) {
		calls++
		if c.Name == "workspace_end" {
			return nil, errors.New("not orchestrator")
		}
		return map[string]any{"waiting": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 || calls != 2 {
		t.Fatalf("responses=%d calls=%d", len(lines), calls)
	}
	for _, line := range lines {
		var v map[string]any
		if e := json.Unmarshal([]byte(line), &v); e != nil {
			t.Fatal(e)
		}
		if v["jsonrpc"] != "2.0" {
			t.Fatal(v)
		}
	}
	if !strings.Contains(lines[1], "workspace_configure") || !strings.Contains(lines[1], "member_update") {
		t.Fatal("missing tools")
	}
	if !strings.Contains(lines[3], `"isError":true`) {
		t.Fatal("tool failure not visible")
	}
}
func TestNoInvocationBeforeInitializeOrForUnknownTool(t *testing.T) {
	var out bytes.Buffer
	called := false
	err := Serve(strings.NewReader("{\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"workspace_configure\"}}\n"), &out, Tools(), func(context.Context, Call) (any, error) { called = true; return nil, nil })
	if err != nil || called {
		t.Fatal(err, called)
	}
	if !strings.Contains(out.String(), "Initialize first") {
		t.Fatal(out.String())
	}
}

func TestInitializationAndAboutShareOperatingInstructions(t *testing.T) {
	var out bytes.Buffer
	err := Serve(strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n"), &out, Tools(), func(context.Context, Call) (any, error) { t.Fatal("initialization invoked a tool"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			Instructions string `json:"instructions"`
			ServerInfo   struct {
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err = json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	about := About()
	if response.Result.Instructions != about["instructions"] || response.Result.ServerInfo.Version != about["api_version"] {
		t.Fatal("initialize/about drift")
	}
	if err := ValidateCall(Call{Name: "workspace_get", Arguments: json.RawMessage(`{"view":"about"}`)}); err != nil {
		t.Fatal(err)
	}
	names := about["tools"].([]string)
	if len(names) != len(Tools()) || about["wash_version"] == "" {
		t.Fatal(about)
	}
	if !about["capabilities"].(map[string]bool)["bulk_workspace_configuration"] {
		t.Fatal("missing bulk API capability")
	}
}

func TestRemovedToolsAreRejectedBeforeDispatch(t *testing.T) {
	removed := []string{"inbox_ack", "no_such_tool"}
	if len(Tools()) != 11 {
		t.Fatalf("want exactly eleven tools, got %d", len(Tools()))
	}
	for _, name := range removed {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCall(Call{Name: name, Arguments: json.RawMessage(`{}`)}); err == nil || !strings.Contains(err.Error(), "unknown workspace tool") {
				t.Fatalf("removed tool accepted: %v", err)
			}
			var in, out bytes.Buffer
			enc := json.NewEncoder(&in)
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": Call{Name: name, Arguments: json.RawMessage(`{}`)}})
			if err := Serve(&in, &out, Tools(), func(context.Context, Call) (any, error) { t.Fatal("removed tool dispatched"); return nil, nil }); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			var response struct {
				Error struct {
					Code    int
					Message string
				}
			}
			if len(lines) != 2 {
				t.Fatal(out.String())
			}
			if err := json.Unmarshal([]byte(lines[1]), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Code != -32602 || response.Error.Message != "Unknown tool" {
				t.Fatal(lines[1])
			}
		})
	}
}

func TestMemberToolsLeaveOutOrchestratorOperations(t *testing.T) {
	var in, out bytes.Buffer
	enc := json.NewEncoder(&in)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": Call{Name: "workspace_configure", Arguments: json.RawMessage(`{}`)}})
	if err := Serve(&in, &out, MemberTools(), func(context.Context, Call) (any, error) {
		t.Fatal("orchestrator tool dispatched for a member")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatal(out.String())
	}
	for name := range leadOnly {
		if strings.Contains(lines[1], `"`+name+`"`) {
			t.Fatalf("member lists %s", name)
		}
	}
	if !strings.Contains(lines[1], `"member_update"`) || len(MemberTools()) != len(Tools())-len(leadOnly) {
		t.Fatal(lines[1])
	}
	if !strings.Contains(lines[2], "Unknown tool") {
		t.Fatal(lines[2])
	}
}
