// OSC 7770 — the agent status channel (docs/AGENT_TERM.md §3).
//
// Coding agents (Claude Code and friends) report what they are doing by
// writing a private-use OSC sequence to their tty:
//
//	OSC 7770 ; v=1 ; ev=<event> [; k=v]… BEL
//	ev ∈ start | working | needs-input | done | end
//
// wash-term tees the pty output copy path through the scanner below —
// BEFORE the router's scrollback ring — so a replay/resync never re-fires
// events, and a remote agent (ssh / wash-remote) works unchanged because
// the parse happens where the pty lives.
//
// Two rules the implementation is built around:
//
//   - The sequence is NEVER stripped from the stream. xterm.js silently
//     ignores unknown OSC ids, and rewriting the byte stream would undo
//     the byte-exactness PTY_ROBUST established. The ring therefore keeps
//     old OSC events; harmless, because nothing parses on replay.
//   - Events are ADVISORY. Anything running in the pty can emit them —
//     they're just bytes. They may drive UI (dots, toasts, roster rows)
//     and must never drive input into a pty or a policy decision.
package pty

import (
	"bytes"
	"io"
	"strconv"
	"strings"
)

// SetAgentHandler installs fn as the receiver for OSC 7770 agent status
// events seen in this session's pty output. Until it is set, output is
// not scanned. Pass nil to stop scanning.
//
// fn runs on the pty→channel copy goroutine, so it must not block: send
// on a buffered channel or do bounded work. Events are ADVISORY — treat
// them as UI hints, never as authority to write into the pty or to make
// a policy decision.
func (s *Session) SetAgentHandler(fn func(AgentEvent)) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	s.agentFn = fn
	if fn != nil && s.agentScan == nil {
		s.agentScan = &agentOSCScanner{}
	}
}

// feedAgent tees one pty read into the scanner. The bytes are only read,
// never rewritten — the caller passes the same slice on to the channel.
func (s *Session) feedAgent(p []byte) {
	s.agentMu.Lock()
	fn := s.agentFn
	var evs []AgentEvent
	if fn != nil {
		s.agentScan.Feed(p, func(ev AgentEvent) { evs = append(evs, ev) })
	}
	s.agentMu.Unlock()
	// Dispatch outside the lock: a handler that calls back into the
	// session (or into SetAgentHandler) would otherwise deadlock.
	for _, ev := range evs {
		fn(ev)
	}
}

// agentTee wraps the pty side of the pty→channel copy so every byte is
// scanned on its way out — before it reaches the channel, and therefore
// before the router's scrollback ring.
type agentTee struct {
	r io.Reader
	s *Session
}

func (t *agentTee) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.s.feedAgent(p[:n])
	}
	return n, err
}

// AgentEvent is one decoded OSC 7770 report. Unknown keys are dropped by
// the parser (forward compat), so a newer agent can add fields without
// breaking an older wash.
type AgentEvent struct {
	// Version is the v= key ("1"); empty when the emitter omitted it.
	Version string
	// Event is the ev= key, always one of the known events — an
	// unrecognized ev never reaches a handler.
	Event string
	// Agent is the agent slug ("claude", "codex", …), when reported.
	Agent string
	// Session is the agent's own session id, for roster rows and
	// `--resume` later (M4).
	Session string
	// Reason qualifies needs-input: "permission" | "idle".
	Reason string
	// Cwd is the agent's working directory, when reported.
	Cwd string
	// Mode is the agent's permission mode, when reported.
	Mode string
	// TurnMS is a turn duration in milliseconds, when reported.
	TurnMS int64
}

// Agent event names. Anything else is ignored.
const (
	AgentEvStart      = "start"
	AgentEvWorking    = "working"
	AgentEvNeedsInput = "needs-input"
	AgentEvDone       = "done"
	AgentEvEnd        = "end"
)

var agentEvents = map[string]bool{
	AgentEvStart: true, AgentEvWorking: true, AgentEvNeedsInput: true,
	AgentEvDone: true, AgentEvEnd: true,
}

const (
	// agentOSCID is the private-use OSC id wash listens on.
	agentOSCID = "7770"
	// agentOSCMax bounds one accumulated sequence. A body longer than
	// this is dropped (not truncated-and-parsed) — a runaway or hostile
	// emitter must not be able to grow the accumulator.
	agentOSCMax = 1024
	// agentValueMax bounds one decoded value. Values land in logs and in
	// FE chrome, so they are also stripped of control characters.
	agentValueMax = 256
)

// scanner states.
const (
	scanText    = iota // outside any escape sequence
	scanEsc            // saw ESC, waiting for ']'
	scanBody           // inside an OSC body, accumulating
	scanBodyEsc        // inside an OSC body, saw ESC (maybe the ST terminator)
)

// agentOSCScanner is a resumable OSC 7770 extractor. Feed it whatever
// arrives from a pty read — sequences torn across chunk boundaries are
// carried in the accumulator, and everything that isn't ours is skipped
// without allocating.
//
// Not safe for concurrent use; the owning Session serializes access.
type agentOSCScanner struct {
	state int
	buf   []byte // accumulated body (everything after "ESC ]")
	// drop marks the current body as not-ours (id mismatch) or
	// over-long: bytes are discarded until the terminator.
	drop bool
}

// Feed scans p and calls emit once per complete, well-formed OSC 7770
// sequence. p is never modified — the caller passes the same bytes on to
// the channel.
func (s *agentOSCScanner) Feed(p []byte, emit func(AgentEvent)) {
	for i := 0; i < len(p); {
		switch s.state {
		case scanText:
			// Fast path: memchr to the next ESC. A terminal doing bulk
			// output (cat, build logs) costs one scan per read, not a
			// per-byte state machine.
			j := bytes.IndexByte(p[i:], 0x1b)
			if j < 0 {
				return
			}
			i += j + 1
			s.state = scanEsc
		case scanEsc:
			c := p[i]
			i++
			switch c {
			case ']':
				s.reset()
				s.state = scanBody
			case 0x1b:
				// ESC ESC — stay armed on the second one.
			default:
				s.state = scanText
			}
		case scanBody:
			c := p[i]
			i++
			switch {
			case c == 0x07: // BEL terminator
				s.finish(emit)
				s.state = scanText
			case c == 0x1b:
				s.state = scanBodyEsc
			case c < 0x20: // CAN/SUB/newline/… — an aborted sequence
				s.reset()
				s.state = scanText
			default:
				s.appendByte(c)
			}
		case scanBodyEsc:
			c := p[i]
			i++
			switch c {
			case '\\': // ST terminator
				s.finish(emit)
				s.state = scanText
			case 0x1b:
				// ESC ESC inside a body: still waiting on the '\'.
			default:
				// Not ST — the body was interrupted by a fresh escape
				// sequence. Abandon it and re-read this byte as the one
				// after ESC.
				s.reset()
				s.state = scanEsc
				i--
			}
		}
	}
}

// reset clears the accumulator between sequences. The backing array is
// kept so a busy terminal doesn't re-allocate on every OSC title change.
func (s *agentOSCScanner) reset() {
	s.buf = s.buf[:0]
	s.drop = false
}

// appendByte accumulates one body byte, rejecting early: as soon as the
// body diverges from our OSC id there is nothing to collect, so an OSC 0
// title or an OSC 52 clipboard blob costs no memory at all.
func (s *agentOSCScanner) appendByte(c byte) {
	if s.drop {
		return
	}
	if n := len(s.buf); n < len(agentOSCID) {
		if agentOSCID[n] != c {
			s.drop = true
			s.buf = s.buf[:0]
			return
		}
	}
	if len(s.buf) >= agentOSCMax {
		s.drop = true
		s.buf = s.buf[:0]
		return
	}
	s.buf = append(s.buf, c)
}

// finish parses a terminated body and emits it when it is a valid
// OSC 7770 event. Always leaves the accumulator empty.
func (s *agentOSCScanner) finish(emit func(AgentEvent)) {
	defer s.reset()
	if s.drop || len(s.buf) == 0 {
		return
	}
	if ev, ok := parseAgentOSC(string(s.buf)); ok && emit != nil {
		emit(ev)
	}
}

// parseAgentOSC decodes an OSC body ("7770;v=1;ev=working;agent=claude").
// Pure, so the whole grammar is unit-testable without a pty.
//
// Tolerances (a hook helper is a shell one-liner on someone else's box):
// spaces around separators and around k/v are trimmed, values are
// %-decoded, unknown keys are ignored, and a repeated key takes its last
// value. A value that must contain ';', '%' or a space has to %-encode it.
func parseAgentOSC(body string) (AgentEvent, bool) {
	var ev AgentEvent
	if !strings.HasPrefix(body, agentOSCID) {
		return ev, false
	}
	rest := strings.TrimLeft(body[len(agentOSCID):], " ")
	if !strings.HasPrefix(rest, ";") {
		// "77700;…" or a bare "7770" — not us.
		return ev, false
	}
	for _, part := range strings.Split(rest[1:], ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = sanitizeAgentValue(pctDecode(strings.TrimSpace(v)))
		switch k {
		case "v":
			ev.Version = v
		case "ev":
			ev.Event = v
		case "agent":
			ev.Agent = v
		case "session":
			ev.Session = v
		case "reason":
			ev.Reason = v
		case "cwd":
			ev.Cwd = v
		case "mode":
			ev.Mode = v
		case "turn_ms":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
				ev.TurnMS = n
			}
		}
		// Unknown keys: ignored on purpose (forward compat).
	}
	if !agentEvents[ev.Event] {
		return ev, false
	}
	return ev, true
}

// pctDecode expands %XX escapes. A malformed escape ("%", "%z1") is left
// as written rather than dropped — the value is display text, and losing
// a character silently is worse than showing a stray '%'.
func pctDecode(s string) string {
	if !strings.ContainsRune(s, '%') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+2 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		hi, ok1 := unhex(s[i+1])
		lo, ok2 := unhex(s[i+2])
		if !ok1 || !ok2 {
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	return b.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// sanitizeAgentValue caps a decoded value and strips control characters.
// %0A would otherwise let a pty-resident process forge a log line or
// smuggle an escape sequence back into the FE through a tab label.
func sanitizeAgentValue(s string) string {
	if len(s) > agentValueMax {
		s = s[:agentValueMax]
	}
	if strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// ---- T0 detection: match the foreground program against an agent table ----

// agentComms maps a foreground program name (/proc/<pid>/comm) to the
// agent slug wash reports. This is tier T0 (docs/AGENT_TERM.md §2): it
// needs no install and covers agents we never write hooks for, at the
// cost of knowing only "an agent is running here".
//
// comm is capped at 15 chars by the kernel, so keep entries short.
var agentComms = map[string]string{
	"claude":       "claude",
	"codex":        "codex",
	"gemini":       "gemini",
	"aider":        "aider",
	"amp":          "amp",
	"opencode":     "opencode",
	"goose":        "goose",
	"crush":        "crush",
	"cursor-agent": "cursor",
}

// runtimeComms are interpreters that show up as the foreground comm when
// an agent ships as a script rather than a native binary (`node
// .../cli.js`). For these — and only these — the agent name is looked up
// in argv instead.
var runtimeComms = map[string]bool{
	"node": true, "bun": true, "deno": true, "npx": true,
	"python": true, "python3": true, "uv": true, "uvx": true,
}

// agentArgvScan bounds how far into argv the runtime fallback looks. The
// agent entry point is argv[1] in every real invocation; a few more
// covers `node --flag .../claude`.
const agentArgvScan = 6

// matchAgent classifies a foreground program as an agent CLI, returning
// its slug or "" for anything else (a plain shell, vi, a build). Pure so
// the table is testable without live processes.
func matchAgent(comm string, argv []string) string {
	if slug, ok := agentComms[comm]; ok {
		return slug
	}
	if !runtimeComms[comm] {
		return ""
	}
	for i, a := range argv {
		if i == 0 || i > agentArgvScan {
			continue
		}
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		if slug, ok := agentComms[agentScriptName(a)]; ok {
			return slug
		}
	}
	return ""
}

// agentScriptName reduces an argv entry to the name to match: basename,
// minus a script extension ("/usr/lib/node_modules/claude/cli.js" stays
// "cli", "~/.local/bin/claude.js" becomes "claude").
func agentScriptName(arg string) string {
	if i := strings.LastIndexByte(arg, '/'); i >= 0 {
		arg = arg[i+1:]
	}
	for _, ext := range []string{".js", ".mjs", ".cjs", ".py"} {
		if strings.HasSuffix(arg, ext) {
			return arg[:len(arg)-len(ext)]
		}
	}
	return arg
}
