// Legacy auto-approve (docs/AGENT_TERM.md §6, last bullet).
//
// For agents with no hooks at all, the only way to answer a permission
// prompt is the one everybody hates: watch the terminal for a question
// and type `y`. Issue #19 asked for it, so it exists — and it is off by
// default, because it is spoofable by construction. Any process in the
// pty can print "(y/n)" and get a keystroke out of wash. That is not a
// bug in the implementation; it is what pattern-matching a terminal
// means, and it is why the policy socket (§6) is the recommended path.
//
// Four conditions must ALL hold before a byte is injected, which is what
// keeps the blast radius near zero:
//
//  1. `legacy_autoapprove: true` in the policy file — an explicit opt-in
//     the user had to type.
//  2. The policy itself is enabled (the same kill switch).
//  3. A coding agent is in the tab's FOREGROUND right now (tier T0). A
//     prompt printed by a shell, a build, or `cat` of a source file
//     containing "(y/n)" can never trigger it. This constraint is wash's
//     addition to the design, and it is the difference between "risky"
//     and "reckless".
//  4. The matched text is a question at the END of the output, not a
//     mention of one in passing.
//
// Every injection is logged and toasted. Nothing here is ever silent.
package term

import (
	"log"
	"strings"
	"sync"
	"time"
)

// autoApproveGap is the minimum spacing between injections for one tab.
// An agent that re-prompts in a loop gets one answer, not a keyboard
// mashing.
const autoApproveGap = 3 * time.Second

// autoApproveTail bounds how much trailing output we keep per tab while
// looking for a question. A prompt is one short line; this is generous.
const autoApproveTail = 512

// autoApprovePrompt is one recognized question shape. The name goes in
// the log line so an unexpected injection is traceable to a pattern.
type autoApprovePrompt struct {
	Name string
	// Suffix is matched against the trailing text with surrounding
	// whitespace trimmed, case-insensitively.
	Suffixes []string
	// Reply is what gets typed, including the newline.
	Reply string
}

// autoApprovePrompts is deliberately a short, closed list of the
// yes/no shapes CLI agents actually print. It is not user-extensible: a
// user-supplied regexp here would be a remote-code-execution-by-terminal
// footgun, and the policy socket exists for anything more nuanced.
//
// Claude Code is absent on purpose — its prompt is a numbered TUI menu,
// not a (y/n) line, and it has hooks, so it uses the policy path.
var autoApprovePrompts = []autoApprovePrompt{
	{Name: "y/n", Suffixes: []string{"(y/n)", "(y/n)?", "(y/n):", "[y/n]", "[y/n]:", "[y/n]?"}, Reply: "y\n"},
	{Name: "yes/no", Suffixes: []string{"(yes/no)", "(yes/no)?", "(yes/no):"}, Reply: "yes\n"},
	{Name: "aider", Suffixes: []string{"(y)es/(n)o", "(y)es/(n)o [yes]:", "(y)es/(n)o/(a)ll/(s)kip all [yes]:"}, Reply: "y\n"},
}

// matchAutoApprove reports which prompt shape the trailing terminal text
// ends with. Pure — the tests are the specification of what counts as a
// question, and every "no" case in them is a keystroke wash did not send.
func matchAutoApprove(tail string) (string, string, bool) {
	// Only the end of the output can be a live prompt: a question that
	// has already been answered (or scrolled past) has more text after it.
	t := strings.ToLower(strings.TrimRight(tail, " \t\r\n"))
	if t == "" {
		return "", "", false
	}
	for _, p := range autoApprovePrompts {
		for _, suffix := range p.Suffixes {
			if strings.HasSuffix(t, suffix) {
				return p.Name, p.Reply, true
			}
		}
	}
	return "", "", false
}

// autoApproveState is one tab's trailing-output window plus its rate
// limit. Guarded by its own mutex: it is written from the pty copy
// goroutine, which must never contend with the app's state lock.
type autoApproveState struct {
	mu       sync.Mutex
	tail     []byte
	lastAt   time.Time
	lastSent string
}

// feed appends output and reports a reply to inject, if the tail now ends
// in a recognized question and the rate limit allows it. Returns "" for
// "do nothing", which is the overwhelmingly common answer.
func (s *autoApproveState) feed(p []byte, now time.Time) (name, reply string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tail = append(s.tail, p...)
	if len(s.tail) > autoApproveTail {
		s.tail = s.tail[len(s.tail)-autoApproveTail:]
	}
	tail := stripANSI(string(s.tail))
	name, reply, ok := matchAutoApprove(tail)
	if !ok {
		return "", ""
	}
	if now.Sub(s.lastAt) < autoApproveGap {
		return "", ""
	}
	// Same question, unanswered? Only reply once — if the agent didn't
	// take our "y" the first time, hammering it won't help.
	if s.lastSent == tail {
		return "", ""
	}
	s.lastAt, s.lastSent = now, tail
	return name, reply
}

// stripANSI removes escape sequences so a coloured prompt still matches
// its plain-text shape. Only the two forms a prompt can carry (CSI …
// letter, and OSC … BEL/ST) — this is a display cleanup, not a parser.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case '[':
			i += 2
			for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
				i++
			}
		case ']':
			i += 2
			for i < len(s) && s[i] != 0x07 {
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
					i++
					break
				}
				i++
			}
		default:
			i++
		}
	}
	return b.String()
}

// autoApproveEnabled reports whether the opt-in is on right now. Both
// switches must be set: the feature flag AND the policy's own kill
// switch, so turning the policy off turns everything off.
func autoApproveEnabled(now time.Time) bool {
	p := policyStore.current(now)
	return p.Enabled && p.LegacyAutoApprove
}

// onTabOutput is the pty output tap for one tab: it decides whether this
// chunk completed a question worth answering, and if so types the answer.
//
// The foreground check is the load-bearing safety condition — an agent
// must actually be running in this tab, which is tier T0 and needs no
// hooks (the exact case this mode exists for).
func onTabOutput(id uint32, inject func([]byte) (int, error), warn func(title, body string), p []byte) {
	now := time.Now()
	if !autoApproveEnabled(now) {
		return
	}
	st.mu.Lock()
	rec := st.agents[id]
	state := st.autoApprove[id]
	if state == nil {
		state = &autoApproveState{}
		st.autoApprove[id] = state
	}
	agent := ""
	if rec != nil {
		agent = rec.pollAgent
	}
	st.mu.Unlock()
	if agent == "" {
		// No agent in the foreground — whatever printed that question, it
		// wasn't something wash is allowed to answer.
		return
	}
	name, reply := state.feed(p, now)
	if reply == "" {
		return
	}
	if _, err := inject([]byte(reply)); err != nil {
		log.Printf("term: agent-autoapprove ch=%d inject: %v", id, err)
		return
	}
	log.Printf("term: agent-autoapprove ch=%d agent=%s prompt=%s reply=%q", id, agent, name, strings.TrimSpace(reply))
	if warn != nil {
		warn("Auto-answered "+agent, "legacy auto-approve typed "+strings.TrimSpace(reply))
	}
}
