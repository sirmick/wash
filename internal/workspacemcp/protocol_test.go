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
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"member_wait","arguments":{"reason":"instructions"}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"teardown_workspace","arguments":{}}}
`
	var out bytes.Buffer
	calls := 0
	err := Serve(strings.NewReader(in), &out, func(_ context.Context, c Call) (any, error) {
		calls++
		if c.Name == "teardown_workspace" {
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
	if !strings.Contains(lines[1], "setup_workspace") || !strings.Contains(lines[1], "plan_update_item") {
		t.Fatal("missing tools")
	}
	if !strings.Contains(lines[3], `"isError":true`) {
		t.Fatal("tool failure not visible")
	}
}
func TestNoInvocationBeforeInitializeOrForUnknownTool(t *testing.T) {
	var out bytes.Buffer
	called := false
	err := Serve(strings.NewReader("{\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"setup_workspace\"}}\n"), &out, func(context.Context, Call) (any, error) { called = true; return nil, nil })
	if err != nil || called {
		t.Fatal(err, called)
	}
	if !strings.Contains(out.String(), "Initialize first") {
		t.Fatal(out.String())
	}
}
