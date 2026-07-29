// Package agenthook implements wash's agent hook helper and the installer
// that wires it into a coding agent's config (docs/AGENT_TERM.md §4).
//
// Two entry points share this package:
//
//   - wash-agent-hook — the FE-less multicall helper an agent runs on each
//     hook event. `status` mode reads the hook's JSON payload on stdin and
//     writes an OSC 7770 sequence to /dev/tty, which lands in whatever pty
//     the agent is running in. That's the whole coupling: no sockets, no
//     wash SDK, no environment contract — so it works identically for a
//     local terminal, an ssh session, and a wash-remote tab.
//   - wash agent-hooks install|remove|status — the CLI that merges (and
//     un-merges) those hook entries into ~/.claude/settings.json. The
//     Settings → Agents panel will drive the same functions.
//
// `decide` mode (the PreToolUse policy callback) is M3 and deliberately
// absent here.
package agenthook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Agent slugs wash knows how to install hooks for. Claude Code is the
// only one in M1; the others are T0-detected only until each earns its
// own adapter (§4).
const AgentClaude = "claude"

// maxPayload bounds the hook JSON we read. A UserPromptSubmit payload
// carries the whole prompt, so this is generous — but it is a bound.
const maxPayload = 1 << 20

// maxValue caps one OSC value. The parser drops any sequence over 1 KiB
// whole, so a deep cwd must never be able to push us past it.
const maxValue = 200

// Run is the wash-agent-hook entry point. It never fails loudly: a hook
// that exits non-zero or writes to stderr shows up as noise inside the
// agent's UI, and this helper's job is to be invisible.
func Run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "status":
		return runStatus(args[1:], os.Stdin)
	case "decide":
		// M3. Print the fail-open answer so an early adopter who wires it
		// up gets Claude's own prompt rather than a broken turn.
		fmt.Println(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"defer"}}`)
		return 0
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "wash-agent-hook: unknown mode %q\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `wash-agent-hook — report coding-agent status to the terminal wash is showing

Usage (from an agent's hook config; see `+"`wash agent-hooks install`"+`):
  wash-agent-hook status [--agent=claude] [--tty=/dev/tty]
      Read the hook event JSON on stdin and write one OSC 7770 status
      sequence to the controlling terminal. Always exits 0.

  wash-agent-hook decide     PreToolUse policy callback (wash M3; currently
                             always defers to the agent's own prompt).`)
}

// runStatus maps one hook payload to an OSC 7770 sequence and writes it
// to the controlling terminal.
func runStatus(args []string, stdin io.Reader) int {
	agent := AgentClaude
	ttyPath := "/dev/tty"
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--agent="):
			if v := strings.TrimPrefix(a, "--agent="); v != "" {
				agent = v
			}
		case strings.HasPrefix(a, "--tty="):
			// Test seam (and an escape hatch for an agent whose hooks run
			// detached from the tty): write the sequence somewhere else.
			if v := strings.TrimPrefix(a, "--tty="); v != "" {
				ttyPath = v
			}
		}
	}
	payload, err := io.ReadAll(io.LimitReader(stdin, maxPayload))
	if err != nil {
		return 0
	}
	seq, ok := StatusSequence(payload, agent)
	if !ok {
		// A hook event with no wash meaning (or unparseable JSON). Not an
		// error: agents add events, and old helpers must stay quiet.
		return 0
	}
	f, err := os.OpenFile(ttyPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		// No controlling terminal (a detached/CI agent). Nothing to
		// report to; still a success from the agent's point of view.
		return 0
	}
	defer f.Close()
	_, _ = f.WriteString(seq)
	return 0
}

// hookPayload is the subset of an agent hook's JSON that wash reads.
// Everything is optional — a missing field just means a thinner event.
type hookPayload struct {
	Event          string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	PermissionMode string `json:"permission_mode"`
	// Notification only: the type is what the hook matcher selects on
	// ("permission_prompt" / "idle_prompt"); message is the human text,
	// used as a fallback when an older agent sends no type.
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
}

// StatusSequence maps one hook payload to the OSC 7770 sequence wash
// should emit, or ok=false when the event carries no status meaning.
// Pure: the whole mapping is unit-testable without a tty.
//
// The Claude Code matrix (docs/AGENT_TERM.md §4):
//
//	SessionStart      → ev=start
//	UserPromptSubmit  → ev=working
//	Notification      → ev=needs-input, reason=permission|idle
//	Stop              → ev=done
//	SessionEnd        → ev=end
func StatusSequence(payload []byte, agent string) (string, bool) {
	var p hookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", false
	}
	var ev, reason string
	switch p.Event {
	case "SessionStart":
		ev = "start"
	case "UserPromptSubmit":
		ev = "working"
	case "Notification":
		ev, reason = "needs-input", notificationReason(p)
	case "Stop":
		ev = "done"
	case "SessionEnd":
		ev = "end"
	default:
		return "", false
	}
	var b strings.Builder
	b.WriteString("\x1b]7770;v=1;ev=")
	b.WriteString(ev)
	if agent != "" {
		b.WriteString(";agent=" + pctEncode(agent))
	}
	if reason != "" {
		b.WriteString(";reason=" + reason)
	}
	if p.SessionID != "" {
		b.WriteString(";session=" + pctEncode(p.SessionID))
	}
	if p.Cwd != "" {
		b.WriteString(";cwd=" + pctEncode(p.Cwd))
	}
	if p.PermissionMode != "" {
		b.WriteString(";mode=" + pctEncode(p.PermissionMode))
	}
	b.WriteString("\x07")
	return b.String(), true
}

// notificationReason classifies a Notification into the reason wash shows
// beside a needs-input dot. notification_type is authoritative; the
// message text is a fallback for agents that don't send one.
func notificationReason(p hookPayload) string {
	switch p.NotificationType {
	case "permission_prompt":
		return "permission"
	case "idle_prompt":
		return "idle"
	case "":
		// fall through to the message sniff
	default:
		return "" // an unknown type is still "needs input", just unlabelled
	}
	m := strings.ToLower(p.Message)
	switch {
	case strings.Contains(m, "permission"):
		return "permission"
	case strings.Contains(m, "waiting for your input"), strings.Contains(m, "idle"):
		return "idle"
	}
	return ""
}

// pctEncode escapes a value for the OSC grammar: ';' and '=' are field
// separators, '%' is the escape itself, and anything non-printable would
// be stripped by the parser anyway. Values are also length-capped so one
// long cwd can't push the sequence past the parser's 1 KiB limit.
func pctEncode(s string) string {
	if len(s) > maxValue {
		s = s[:maxValue]
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case c == '-', c == '.', c == '_', c == '~', c == '/', c == ':', c == '@', c == '+':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
