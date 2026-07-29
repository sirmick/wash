// Agent notifications (docs/AGENT_TERM.md §5, M2).
//
// Two moments in an agent's turn are worth interrupting a human for, and
// no others:
//
//   - it wants something from you (a permission prompt, or it has gone
//     idle waiting) — a warn toast, because the work has stopped;
//   - a turn you were waiting on finished — an info toast, with how long
//     it took.
//
// Everything else the tab dot already says quietly. The rules below exist
// because a chatty desktop is one people turn off: transitions only (never
// a repeat of the same state), rate-limited per tab, and needs-input is
// allowed to jump the rate limit because it is the one that is actually
// blocking the human.
//
// The toast rides the existing notify path (sdk.Warn/Info → tray →
// history), so it carries this app's instance id and the shell can focus
// the terminal when it is clicked. No parallel channel.
package term

import (
	"fmt"
	"log"
	"path"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/sdk"
)

// agentToastGap is the minimum spacing between toasts for ONE tab. An
// agent that flips working→needs-input→working while a human is typing
// into it should not produce a column of toasts.
const agentToastGap = 5 * time.Second

// agentToast is a decided notification. Empty Title = nothing to say.
type agentToast struct {
	Title string
	Body  string
	Level string // "warn" | "info"
}

// toastFor decides whether the transition the record has just made is
// worth a toast, and applies the rate limit. It mutates the rate-limit
// bookkeeping on success, so call it exactly once per transition.
//
// prev is the state the tab left (r.prevState at the time of the call);
// v is the merged view it landed on.
func (r *agentRec) toastFor(v agentView, now time.Time) (agentToast, bool) {
	var t agentToast
	switch {
	case v.State == agentStateNeedsInput:
		// Reaching needs-input at all is the event; how it got there
		// doesn't matter (a fresh session can prompt before it works).
		t = agentToast{
			Title: agentDisplayName(v.Agent) + " needs your input",
			Body:  toastBody(r.oscCwd, needsInputWhy(v.Reason)),
			Level: "warn",
		}
	case v.State == agentStateDone && r.prevState == agentStateWorking:
		// Only a turn you could have been waiting on. done arriving from
		// running/idle is a session that never worked — silence.
		t = agentToast{
			Title: fmt.Sprintf("%s finished after %s", agentDisplayName(v.Agent), fmtDur(now.Sub(r.prevSince))),
			Body:  toastBody(r.oscCwd, ""),
			Level: "info",
		}
	default:
		return t, false
	}
	if !r.allowToast(t.Level, now) {
		return agentToast{}, false
	}
	r.lastToastAt, r.lastToastLevel = now, t.Level
	return t, true
}

// allowToast is the rate limit: one toast per tab per agentToastGap,
// except that a needs-input warn may always interrupt a preceding info
// ("needs-input always wins over done" — the human is blocked, and the
// toast that says so must not be swallowed by the toast that said the
// last turn finished). Two warns in a row are still spaced out.
func (r *agentRec) allowToast(level string, now time.Time) bool {
	if now.Sub(r.lastToastAt) >= agentToastGap {
		return true
	}
	return level == "warn" && r.lastToastLevel != "warn"
}

// needsInputWhy turns the OSC reason into the phrase a human reads.
func needsInputWhy(reason string) string {
	switch reason {
	case "permission":
		return "permission request"
	case "idle":
		return "waiting for you"
	case "":
		return ""
	default:
		return reason
	}
}

// toastBody names the tab so a toast from one of several agents is
// actionable: the cwd's basename (the repo, usually) plus the reason.
// The git branch joins this in M4, where agentd owns the git lookups.
func toastBody(cwd, why string) string {
	parts := make([]string, 0, 2)
	if base := path.Base(strings.TrimRight(cwd, "/")); cwd != "" && base != "." && base != "/" {
		parts = append(parts, base)
	}
	if why != "" {
		parts = append(parts, why)
	}
	return strings.Join(parts, " · ")
}

// agentDisplayName renders a slug for humans: "claude" → "Claude".
func agentDisplayName(slug string) string {
	if slug == "" {
		return "Agent"
	}
	return strings.ToUpper(slug[:1]) + slug[1:]
}

// fmtDur renders a turn length the way a toast wants it: seconds, then
// minutes (with seconds while they still matter), then hours.
func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	case d < time.Hour:
		m := int(d.Minutes())
		if s := int(d.Seconds()) % 60; s != 0 {
			return fmt.Sprintf("%dm %ds", m, s)
		}
		return fmt.Sprintf("%dm", m)
	default:
		h := int(d.Hours())
		if m := int(d.Minutes()) % 60; m != 0 {
			return fmt.Sprintf("%dh %dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
}

// notifyAgent sends a decided toast. sdk.Warn/Info are fire-and-forget on
// their own goroutine, which is what we want on the pty copy path.
func notifyAgent(c *sdk.Conn, id uint32, t agentToast) {
	log.Printf("term: agent-notify ch=%d level=%s title=%q body=%q", id, t.Level, t.Title, t.Body)
	if t.Level == "warn" {
		c.Warn(t.Title, t.Body)
		return
	}
	c.Info(t.Title, t.Body)
}
