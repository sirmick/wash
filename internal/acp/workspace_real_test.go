package acp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Opt-in, bounded provider probe. Run with WASH_ACP_ADAPTER and
// WASH_WORKSPACE_BINARY pointing at an isolated Wash build. The only tool
// allowed by the probe is a read-only swarm_status; no real workspace starts.
func TestWorkspaceMCPAgainstRealAdapter(t *testing.T) {
	adapter, bin := os.Getenv("WASH_ACP_ADAPTER"), os.Getenv("WASH_WORKSPACE_BINARY")
	if adapter == "" || bin == "" {
		t.Skip("set WASH_ACP_ADAPTER and WASH_WORKSPACE_BINARY for the real-provider MCP probe")
	}
	dir := t.TempDir()
	socket := filepath.Join(dir, "probe.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer conformance-only" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var call struct{ Name string }
		if json.NewDecoder(r.Body).Decode(&call) != nil || call.Name != "swarm_status" {
			http.Error(w, "read-only probe", 400)
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"probe": "wash-workspace-ok"}})
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	f := strings.Fields(adapter)
	cmd := exec.Command(f[0], f[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	client := NewClient(stdout, stdin, &conformanceHandler{t: t})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	init, err := client.Initialize(ctx, ClientCapabilities{}, Implementation{Name: "wash-workspace-probe", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	mcp := []McpServer{{Name: "wash_workspace", Command: bin, Args: []string{"--workspace-mcp"}, Env: []EnvVar{{Name: "WASH_WORKSPACE_SOCKET", Value: socket}, {Name: "WASH_WORKSPACE_TOKEN", Value: "conformance-only"}}}}
	session, err := client.NewSession(ctx, dir, mcp)
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	prompt := []ContentBlock{{Type: "text", Text: "Call the wash_workspace swarm_status tool exactly once. Then report its probe value. Do not use other tools."}}
	if _, err = client.Prompt(ctx, session.SessionID, prompt...); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one MCP call, got %d", calls.Load())
	}
	if !init.AgentCapabilities.LoadSession {
		t.Fatal("provider does not support session/load")
	}
	if _, err = client.LoadSession(ctx, session.SessionID, dir, mcp); err != nil {
		t.Fatalf("session/load: %v", err)
	}
	if _, err = client.Prompt(ctx, session.SessionID, prompt...); err != nil {
		t.Fatalf("resumed prompt: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("MCP not reinjected after load: calls=%d", calls.Load())
	}
}
