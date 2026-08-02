package agenthook

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTerminal stands in for a wash-term decision socket: it reads one
// request and replies with a canned answer, recording what it was asked.
func fakeTerminal(t *testing.T, reply string) (sockPath string, asked func() decideRequest) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	got := make(chan decideRequest, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				var req decideRequest
				_ = json.Unmarshal([]byte(strings.TrimSpace(line)), &req)
				select {
				case got <- req:
				default:
				}
				_, _ = conn.Write([]byte(reply + "\n"))
			}()
		}
	}()
	return path, func() decideRequest {
		select {
		case r := <-got:
			return r
		case <-time.After(2 * time.Second):
			t.Fatal("terminal was never asked")
			return decideRequest{}
		}
	}
}

const preToolUse = `{
  "hook_event_name": "PreToolUse",
  "session_id": "s-1",
  "cwd": "/home/mick/wash",
  "permission_mode": "default",
  "tool_name": "Bash",
  "tool_input": {"command": "git status --short"},
  "tool_use_id": "tu-1"
}`

func runDecideStr(t *testing.T, sock, payload string) string {
	t.Helper()
	var out bytes.Buffer
	args := []string{}
	if sock != "" {
		args = append(args, "--sock="+sock)
	}
	if rc := runDecide(args, strings.NewReader(payload), &out); rc != 0 {
		t.Fatalf("exit %d, want 0", rc)
	}
	return out.String()
}

// The whole point: an allow reaches Claude Code in the shape it expects,
// carrying the rule that produced it so the transcript records WHY.
func TestDecideAllow(t *testing.T) {
	sock, asked := fakeTerminal(t, `{"decision":"allow","rule":"Bash(git status:*)"}`)
	out := runDecideStr(t, sock, preToolUse)

	var parsed struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	h := parsed.HookSpecificOutput
	if h.HookEventName != "PreToolUse" || h.PermissionDecision != "allow" {
		t.Errorf("output = %+v", h)
	}
	if !strings.Contains(h.PermissionDecisionReason, "Bash(git status:*)") {
		t.Errorf("reason = %q, want the rule in it", h.PermissionDecisionReason)
	}

	// …and the terminal was asked the right question.
	req := asked()
	if req.ToolName != "Bash" || req.SessionID != "s-1" || req.Cwd != "/home/mick/wash" {
		t.Errorf("asked %+v", req)
	}
	if cmd, _ := req.ToolInput["command"].(string); cmd != "git status --short" {
		t.Errorf("tool_input.command = %q", cmd)
	}
	if req.PermissionMode != "default" {
		t.Errorf("permission_mode = %q", req.PermissionMode)
	}
}

func TestDecideDeny(t *testing.T) {
	sock, _ := fakeTerminal(t, `{"decision":"deny","rule":"Bash(rm *)"}`)
	out := runDecideStr(t, sock, preToolUse)
	if !strings.Contains(out, `"permissionDecision":"deny"`) {
		t.Errorf("output = %q, want a deny", out)
	}
}

// Every fail-open case prints NOTHING, leaving the agent's own prompt
// exactly as it would have been without wash. This is the single most
// important behaviour in the file: silence is the fallback, never allow.
func TestDecideFailsOpenSilently(t *testing.T) {
	live, _ := fakeTerminal(t, `{"decision":"ask","rule":"default"}`)
	askish, _ := fakeTerminal(t, `{"decision":"sure, why not"}`)
	garbage, _ := fakeTerminal(t, `not json`)
	empty, _ := fakeTerminal(t, ``)

	cases := []struct {
		name    string
		sock    string
		payload string
	}{
		{"policy says ask", live, preToolUse},
		{"unrecognized decision", askish, preToolUse},
		{"garbled answer", garbage, preToolUse},
		{"empty answer", empty, preToolUse},
		{"no socket path at all", "", preToolUse},
		{"socket path that doesn't exist", filepath.Join(t.TempDir(), "nope.sock"), preToolUse},
		{"payload isn't json", live, "definitely not json"},
		{"payload has no tool", live, `{"hook_event_name":"PreToolUse","session_id":"s"}`},
		{"empty payload", live, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := runDecideStr(t, c.sock, c.payload); out != "" {
				t.Errorf("printed %q, want nothing", out)
			}
		})
	}
}

// A terminal that accepts the connection and then says nothing must not
// hold the agent's turn open indefinitely.
func TestDecideTimesOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "silent.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Accept and stall. The helper's deadline must fire.
		time.Sleep(10 * time.Second)
		conn.Close()
	}()

	// Shorten the deadline for the test; the production value is sized
	// for a human answering at the desktop (§12).
	restore := decideTimeout
	decideTimeout = 700 * time.Millisecond
	defer func() { decideTimeout = restore }()

	start := time.Now()
	out := runDecideStr(t, path, preToolUse)
	elapsed := time.Since(start)
	if out != "" {
		t.Errorf("printed %q on timeout, want nothing", out)
	}
	if elapsed > decideTimeout+2*time.Second {
		t.Errorf("took %v, want to give up near %v", elapsed, decideTimeout)
	}
}

// $WASH_AGENT_SOCK is the production path (the term BE puts it in the
// pty's environment); --sock is the test/override seam.
func TestDecideUsesEnvSocket(t *testing.T) {
	sock, asked := fakeTerminal(t, `{"decision":"allow","rule":"Read"}`)
	t.Setenv("WASH_AGENT_SOCK", sock)
	var out bytes.Buffer
	if rc := runDecide(nil, strings.NewReader(preToolUse), &out); rc != 0 {
		t.Fatalf("exit %d", rc)
	}
	if !strings.Contains(out.String(), `"permissionDecision":"allow"`) {
		t.Errorf("output = %q", out.String())
	}
	if req := asked(); req.ToolName != "Bash" {
		t.Errorf("asked %+v", req)
	}
}

// The dispatcher wires `wash-agent-hook decide` to this path and, like
// every hook mode, must exit 0 whatever happens.
func TestRunDecideModeExitsZero(t *testing.T) {
	old := os.Stdin
	defer func() { os.Stdin = old }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	go func() {
		_, _ = w.WriteString(preToolUse)
		w.Close()
	}()
	if rc := Run([]string{"decide"}); rc != 0 {
		t.Errorf("exit %d, want 0", rc)
	}
}
