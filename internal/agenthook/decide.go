// `wash-agent-hook decide` — the PreToolUse policy callback
// (docs/AGENT_TERM.md §4, §6).
//
// The agent hands us a tool call on stdin; we ask the terminal that owns
// this pty ($WASH_AGENT_SOCK) what its policy says, and print the answer
// in the shape Claude Code expects.
//
// The rule this whole path is built around: **fail open to asking the
// human, never to allowing.** No socket, no policy, a timeout, a garbled
// answer, an answer we don't recognize — every one of those prints
// nothing at all, which leaves the agent's own permission prompt exactly
// as it would have been without wash. The only way anything is
// auto-allowed is an enabled policy explicitly saying so.
//
// (The design doc called that fallback `permissionDecision:"defer"`.
// Claude Code 2.1 accepts "defer" in print mode only and logs "defer is
// print-mode only; ignoring" in an interactive session — which is the
// case that matters here — so silence is the portable spelling of the
// same intent. Printing "ask" instead would be wrong in a subtler way: it
// would override the user's own allowlist and make wash's presence more
// annoying than its absence.)
package agenthook

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// decideTimeout bounds the whole exchange. The terminal answers from its
// rule table in microseconds, but since M6 (§12) it may also put the
// question to the human at the desktop — so this is sized to outlast that
// (agentd holds an ask 30s, the terminal 35s, we wait 45s), while still
// being far inside Claude Code's own 600s hook budget. A terminal that
// died closes the socket and we return immediately; only a HUNG one costs
// the full wait.
//
// A var, not a const, so tests can shorten it — a unit test should not
// have to burn 45 seconds to prove a deadline works.
var decideTimeout = 45 * time.Second

// maxDecideReply caps the answer we'll read. It is a two-field object.
const maxDecideReply = 64 << 10

// decideRequest is what we ask the terminal. Mirrors the fields Claude
// Code puts in a PreToolUse payload (verified against 2.1: session_id,
// cwd, permission_mode come from the common hook envelope; tool_name and
// tool_input are PreToolUse's own).
type decideRequest struct {
	SessionID      string         `json:"session_id"`
	ToolName       string         `json:"tool_name"`
	ToolInput      map[string]any `json:"tool_input"`
	Cwd            string         `json:"cwd"`
	PermissionMode string         `json:"permission_mode"`
}

// decideReply is the terminal's answer.
type decideReply struct {
	Decision string `json:"decision"`
	Rule     string `json:"rule,omitempty"`
}

// runDecide implements `wash-agent-hook decide`. Always exits 0: a hook
// that fails loudly shows up as noise inside the agent's UI, and every
// failure here already resolves to "let the human decide".
func runDecide(args []string, stdin io.Reader, stdout io.Writer) int {
	sockPath := os.Getenv("WASH_AGENT_SOCK")
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--sock="); ok && v != "" {
			sockPath = v
		}
	}
	payload, err := io.ReadAll(io.LimitReader(stdin, maxPayload))
	if err != nil {
		return 0
	}
	var req decideRequest
	if err := json.Unmarshal(payload, &req); err != nil || req.ToolName == "" {
		return 0
	}
	if sockPath == "" {
		// Not running under a wash terminal (or policy never installed).
		return 0
	}
	reply, err := askTerminal(sockPath, req)
	if err != nil {
		return 0
	}
	switch reply.Decision {
	case "allow", "deny":
		writeDecision(stdout, reply)
	}
	// "ask" — and anything unrecognized — says nothing, leaving the
	// agent's own flow untouched.
	return 0
}

// askTerminal does one request/response over the unix socket.
func askTerminal(sockPath string, req decideRequest) (decideReply, error) {
	var reply decideReply
	conn, err := net.DialTimeout("unix", sockPath, decideTimeout)
	if err != nil {
		return reply, err
	}
	defer conn.Close()
	deadline := time.Now().Add(decideTimeout)
	_ = conn.SetDeadline(deadline)

	body, err := json.Marshal(req)
	if err != nil {
		return reply, err
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return reply, err
	}
	// Half-close so the far side sees EOF even if it reads to the end.
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	data, err := io.ReadAll(io.LimitReader(conn, maxDecideReply))
	if err != nil {
		return reply, err
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &reply); err != nil {
		return reply, err
	}
	return reply, nil
}

// writeDecision prints Claude Code's PreToolUse hook output. The reason
// names the rule so the transcript records WHY a call was allowed or
// blocked, not just that it was.
func writeDecision(w io.Writer, reply decideReply) {
	reason := "wash policy"
	if reply.Rule != "" {
		reason = "wash policy: " + reply.Rule
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       reply.Decision,
			"permissionDecisionReason": reason,
		},
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	fmt.Fprintln(w, string(data))
}
