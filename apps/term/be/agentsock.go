// The agent decision socket (docs/AGENT_TERM.md §6, M3).
//
// Each pty gets its own unix socket, exported into the shell as
// $WASH_AGENT_SOCK. An agent's PreToolUse hook runs `wash-agent-hook
// decide`, which connects, asks one question, reads one answer, and exits.
// One request/response per connection; no state, no session, no protocol
// negotiation.
//
// The trust story, which is the whole reason this is a separate channel
// from the OSC one (§3):
//
//   - The socket ANSWERS; it never initiates. Nothing here can write to a
//     pty, spawn anything, or change state. The only observable effect of
//     asking is a log line.
//   - The socket is 0600 and lives in the per-user runtime dir, so the
//     answer is only reachable by the uid that owns the terminal.
//   - A hostile process inside the pty can ask it things. All it learns is
//     the policy verdict for a tool call it made up — which is a verdict
//     about a call it could have made anyway.
//   - The spoofable OSC status channel has NO path into this decision. A
//     forged "needs-input" cannot become an "allow".
package term

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/sirmick/wash/internal/pty"
	"github.com/sirmick/wash/internal/sdk"
)

// decideDeadline bounds one request/response exchange. The helper runs
// inside the agent's turn, so a wedged answer must fail fast (the helper
// falls back to the agent's own prompt).
const decideDeadline = 3 * time.Second

// maxDecideRequest caps one request body. tool_input can carry a whole
// file's content on a Write; we only read what we need to decide.
const maxDecideRequest = 1 << 20

// tabSeq numbers sockets within this process, so the path can be built
// BEFORE the pty exists (the shell needs $WASH_AGENT_SOCK in its
// environment at exec time, and the channel id is only known after).
var tabSeq atomic.Uint64

// agentSock is one tab's listener.
type agentSock struct {
	path string
	ln   net.Listener
}

// newAgentSock creates the listener for a tab that is about to be
// spawned. Returns nil (and logs) if the socket can't be created — the
// terminal still opens, just without policy answers, which is the same
// state as "no policy installed".
func newAgentSock() *agentSock {
	dir := agentSockDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("term: agent-socket mkdir dir=%s: %v", dir, err)
		return nil
	}
	path := filepath.Join(dir, fmt.Sprintf("agent-%d-%d.sock", os.Getpid(), tabSeq.Add(1)))
	// A stale socket from a crashed predecessor would make Listen fail;
	// the pid+seq name makes a live collision impossible, so removing is
	// safe.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		log.Printf("term: agent-socket listen path=%s: %v", path, err)
		return nil
	}
	// 0600: only this uid may ask. Set after bind — the socket is created
	// with the process umask, which may be looser.
	if err := os.Chmod(path, 0o600); err != nil {
		log.Printf("term: agent-socket chmod path=%s: %v", path, err)
	}
	return &agentSock{path: path, ln: ln}
}

// sockDeps is what a decision needs from its tab: which channel it is
// (for the audit line) and how to tell the user something was blocked.
// Passing these in rather than an *sdk.Conn keeps the socket testable
// with no router in sight.
type sockDeps struct {
	chanID func() uint32
	warn   func(title, body string)
	// conn is the app connection, used to ask the desktop (§12). Nil in
	// tests that only exercise the table.
	conn *sdk.Conn
}

// serve accepts decide requests until the listener closes. One goroutine
// per connection, each with its own deadline.
func (a *agentSock) serve(deps sockDeps) {
	for {
		conn, err := a.ln.Accept()
		if err != nil {
			// Listener closed on tab teardown — the normal exit.
			return
		}
		go a.handle(deps, conn)
	}
}

func (a *agentSock) handle(deps sockDeps, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(decideDeadline))

	var req decideRequest
	dec := json.NewDecoder(io.LimitReader(conn, maxDecideRequest))
	if err := dec.Decode(&req); err != nil {
		// Unreadable question → the fail-open answer, so a broken client
		// still lands the user in the agent's own prompt.
		writeDecision(conn, decideResponse{Decision: DecisionAsk, Rule: "bad request"})
		if !errors.Is(err, io.EOF) {
			log.Printf("term: agent-decide decode: %v", err)
		}
		return
	}
	policy := policyStore.current(time.Now())
	resp := evaluate(policy, req)
	// No answer from the table? Ask the human where they already are
	// (§12). Only when the policy is on and the desktop ask is enabled;
	// otherwise this is M3 exactly. The connection deadline has to grow
	// to cover the wait — it was sized for a table lookup.
	if resp.Decision == DecisionAsk && policy.AskDesktopOrDefault() {
		_ = conn.SetDeadline(time.Now().Add(askWait + 5*time.Second))
		if d := askDesktop(deps.conn, deps.chanID(), req, toolSubject(req.ToolName, req.ToolInput)); d != DecisionDefer {
			resp = decideResponse{Decision: d, Rule: "you (desktop)"}
		}
	}
	writeDecision(conn, resp)

	// Audit: every non-ask decision is a thing wash did on the user's
	// behalf, and must be reconstructable from the log alone.
	if resp.Decision != DecisionAsk {
		log.Printf("term: agent-decide ch=%d session=%s tool=%s decision=%s rule=%q cwd=%s",
			deps.chanID(), req.SessionID, req.ToolName, resp.Decision, resp.Rule, req.Cwd)
	}
	// A denial is the case where the agent is about to look stuck for a
	// reason the user can't see. Say so.
	if resp.Decision == DecisionDeny && deps.warn != nil {
		deps.warn(
			fmt.Sprintf("Blocked %s", req.ToolName),
			fmt.Sprintf("wash policy · %s", resp.Rule),
		)
	}
}

func writeDecision(conn net.Conn, resp decideResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(data, '\n'))
}

// close tears the listener down and removes the socket file.
func (a *agentSock) close() {
	if a == nil {
		return
	}
	_ = a.ln.Close()
	_ = os.Remove(a.path)
}

// agentSockDir is the per-user runtime directory sockets live in,
// following the same preference order as the rest of wash's runtime
// state: $XDG_RUNTIME_DIR/wash, else /tmp/wash-<uid>. Never a shared
// world-writable path.
func agentSockDir() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "wash")
	}
	return fmt.Sprintf("/tmp/wash-%d", os.Getuid())
}

// withAgentSock returns an env transform that layers WASH_AGENT_SOCK on
// top of the standard wash terminal environment. sock may be nil (no
// listener), in which case the variable is simply absent and the helper
// defers every decision — the off state.
func withAgentSock(sock *agentSock) func([]string) []string {
	return func(env []string) []string {
		env = pty.WithWashEnv(env)
		if sock == nil {
			return env
		}
		return append(env, "WASH_AGENT_SOCK="+sock.path)
	}
}
