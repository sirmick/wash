package agentd

// The closing dossier: what one agent needs to take a milestone's tail
// over from the team, in one bounded Markdown document read from the
// workspace record — what is left and what blocks it, what is reported
// and owed acceptance, the open threads with their answers under their
// questions, the owner's decisions, the resolved threads one line each,
// where the done work's commits are, and the handoffs members wrote. The
// pieces exist (plan, QA, LinkAnswers, handoffs); this is the assembly a
// fold-back to a single agent needs, so the handover is a document and
// not a rebuild that loses every member's context (Redoubt, 2026-10-20).

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/swarm"
)

// dossierLimit bounds the document, like the QA view; the closer's
// context is the single point of failure.
const dossierLimit = 256 * 1024

// dossier writes the closing dossier for w. Sections past the limit are
// cut with a note saying where the rest is.
func dossier(w *swarm.Workspace) (string, bool) {
	var b strings.Builder
	name := qaNamesFor(w)
	truncated := false
	section := func(title string, body func(b *strings.Builder)) {
		if truncated {
			return
		}
		var s strings.Builder
		body(&s)
		if b.Len()+s.Len() > dossierLimit {
			truncated = true
			fmt.Fprintf(&b, "\n## %s\n\nCut for size; read it with workspace_get view=qa, view=state and the plan file.\n", title)
			return
		}
		fmt.Fprintf(&b, "\n## %s\n\n", title)
		b.WriteString(s.String())
	}
	fmt.Fprintf(&b, "# %s — closing dossier\n\nWritten %s from the workspace record (plan revision %d, workspace revision %d). Project root: %s.\n",
		w.Name, time.Now().UTC().Format(time.RFC3339), w.PlanRevision, w.Revision, w.Root)
	if w.Legend != "" {
		fmt.Fprintf(&b, "\nLegend: %s\n", w.Legend)
	}

	byID := map[string]*swarm.Node{}
	for i := range w.Plan {
		byID[w.Plan[i].ID] = &w.Plan[i]
	}
	top := func(id string) string {
		for guard := 0; guard <= len(w.Plan); guard++ {
			n := byID[id]
			if n == nil || n.Parent == "" || byID[n.Parent] == nil {
				return id
			}
			id = n.Parent
		}
		return id
	}
	unmet := func(n *swarm.Node) []string {
		var out []string
		for _, need := range n.Needs {
			if dep := byID[need]; dep != nil {
				if dep.State != "done" {
					out = append(out, fmt.Sprintf("%s (%s)", need, dep.State))
				}
			} else if q := swarm.QA(w, need); q != nil && q.State != "resolved" {
				out = append(out, fmt.Sprintf("%s (thread %s)", need, q.State))
			}
		}
		return out
	}
	membersOn := func(id string) []string {
		var out []string
		for _, m := range w.Members {
			if m.Node == id && m.State != "ended" {
				out = append(out, fmt.Sprintf("%s (%s)", m.Name, m.State))
			}
		}
		return out
	}

	section("Remaining work", func(s *strings.Builder) {
		// By milestone, in plan order; steps under their package.
		count := 0
		for _, ms := range w.Plan {
			if ms.Parent != "" || ms.State == "done" {
				continue
			}
			var inside []*swarm.Node
			for i := range w.Plan {
				n := &w.Plan[i]
				if n.ID != ms.ID && top(n.ID) == ms.ID && n.State != "done" {
					inside = append(inside, n)
				}
			}
			if ms.Template == "milestone" && len(inside) == 0 {
				fmt.Fprintf(s, "### %s · %s · %s — nothing planned inside it yet\n\n", ms.ID, ms.State, ms.Title)
				continue
			}
			fmt.Fprintf(s, "### %s · %s · %s\n\n", ms.ID, ms.State, ms.Title)
			if ms.Parent == "" && ms.Template != "milestone" {
				inside = append([]*swarm.Node{&ms}, inside...)
			}
			for _, n := range inside {
				count++
				indent := ""
				if n.Parent != "" && n.Parent != ms.ID {
					indent = "  "
				}
				fmt.Fprintf(s, "%s- **%s** · %s · %s", indent, n.ID, n.State, n.Title)
				if u := unmet(n); len(u) > 0 {
					fmt.Fprintf(s, " — waits on %s", strings.Join(u, ", "))
				} else if len(n.Needs) > 0 {
					s.WriteString(" — unblocked")
				}
				if on := membersOn(n.ID); len(on) > 0 {
					fmt.Fprintf(s, " — %s", strings.Join(on, ", "))
				}
				if open := swarm.OpenOn(w, n.ID); len(open) > 0 {
					fmt.Fprintf(s, " — %d open assignment(s)", len(open))
				}
				s.WriteString("\n")
				if n.Body != "" {
					fmt.Fprintf(s, "%s  %s\n", indent, firstLine(n.Body, 300))
				}
				for _, o := range n.Overrides {
					fmt.Fprintf(s, "%s  Override: %s\n", indent, firstLine(o, 200))
				}
			}
			s.WriteString("\n")
		}
		if count == 0 {
			s.WriteString("Nothing: every node is done.\n")
		}
	})

	section("Reported, awaiting acceptance", func(s *strings.Builder) {
		n := 0
		for i := range w.Plan {
			node := &w.Plan[i]
			if node.State != "reported" {
				continue
			}
			n++
			fmt.Fprintf(s, "- **%s** · %s", node.ID, node.Title)
			for _, a := range w.Assignments {
				if a.Node == node.ID && a.Result != "" {
					fmt.Fprintf(s, " — result: %s", firstLine(a.Result, 300))
					break
				}
			}
			s.WriteString("\n")
		}
		if n == 0 {
			s.WriteString("None.\n")
		}
	})

	section("Open threads", func(s *strings.Builder) {
		n := 0
		for _, q := range w.QA {
			if q.State == "resolved" {
				continue
			}
			n++
			fmt.Fprintf(s, "### %s · %s — %s\n\n%s · assigned to %s", q.Node, q.ID, q.Title, q.State, name(q.Assignee))
			if q.Blocking {
				s.WriteString(" · **blocks its node**")
			}
			s.WriteString("\n\n")
			events := swarm.LinkAnswers(w, q.ID, q.Events)
			// The last 12 events, each answer under what it answers; a
			// question answered later is kept with its answer.
			keep := map[string]bool{}
			for i := len(events) - 1; i >= 0 && len(keep) < 12; i-- {
				keep[events[i].ID] = true
				if events[i].Answers != "" {
					keep[events[i].Answers] = true
				}
			}
			omitted := 0
			for _, e := range events {
				if !keep[e.ID] {
					omitted++
					continue
				}
				indent := ""
				label := eventKind(e.Kind)
				if e.Answers != "" {
					indent = "  "
					if e.Inferred {
						label += " (probably to the above)"
					} else {
						label += " (to the above)"
					}
				}
				fmt.Fprintf(s, "%s- %s · %s: %s\n", indent, name(e.Author), label, firstLine(e.Body, 500))
			}
			if omitted > 0 {
				fmt.Fprintf(s, "- … %d earlier events: workspace_get view=qa thread_id=%s\n", omitted, q.ID)
			}
			s.WriteString("\n")
		}
		if n == 0 {
			s.WriteString("None.\n")
		}
	})

	section("Owner decisions", func(s *strings.Builder) {
		n := 0
		for _, m := range w.Messages {
			if m.Type != "decision_response" {
				continue
			}
			n++
			when := time.UnixMilli(m.Created).UTC().Format("2006-01-02")
			if m.Thread != "" {
				fmt.Fprintf(s, "- %s · thread %s: %s\n", when, m.Thread, firstLine(m.Body, 400))
			} else {
				fmt.Fprintf(s, "- %s: %s\n", when, firstLine(m.Body, 400))
			}
		}
		if n == 0 {
			s.WriteString("None recorded.\n")
		}
	})

	section("Resolved threads", func(s *strings.Builder) {
		n := 0
		for _, q := range w.QA {
			if q.State != "resolved" {
				continue
			}
			n++
			fmt.Fprintf(s, "- %s · %s — %s", q.Node, q.ID, q.Title)
			if q.Evidence != "" {
				fmt.Fprintf(s, " · %s", firstLine(q.Evidence, 160))
			}
			s.WriteString("\n")
		}
		if n == 0 {
			s.WriteString("None.\n")
		}
	})

	section("Done work and its commits", func(s *strings.Builder) {
		n := 0
		for i := range w.Plan {
			node := &w.Plan[i]
			if node.State != "done" || node.Template == "milestone" {
				continue
			}
			shas := commitHashes(node.Body)
			if len(shas) == 0 {
				continue
			}
			n++
			fmt.Fprintf(s, "- %s · %s: %s\n", node.ID, node.Title, strings.Join(shas, ", "))
		}
		if n == 0 {
			s.WriteString("No done node names a commit in its body.\n")
		}
	})

	section("Handoffs and members", func(s *strings.Builder) {
		members := append([]swarm.Member(nil), w.Members...)
		sort.SliceStable(members, func(i, j int) bool { return members[i].Node < members[j].Node })
		for _, m := range members {
			fmt.Fprintf(s, "- %s", m.Name)
			if m.Role != "" {
				fmt.Fprintf(s, " (%s)", m.Role)
			}
			fmt.Fprintf(s, " · %s", m.State)
			if m.Node != "" {
				fmt.Fprintf(s, " · on %s", m.Node)
			}
			if m.HandoffPath != "" {
				fmt.Fprintf(s, " · handoff: %s", m.HandoffPath)
			}
			if m.Status != "" {
				fmt.Fprintf(s, " · %s", firstLine(m.Status, 160))
			}
			s.WriteString("\n")
		}
	})
	return b.String(), truncated
}

// qaNamesFor names an author as the QA document does.
func qaNamesFor(w *swarm.Workspace) func(string) string {
	return func(id string) string {
		if id == "human" {
			return "Owner"
		}
		if m := swarm.GetMember(w, id); m != nil && m.Name != "" {
			return m.Name
		}
		if n := w.QAAuthors[id]; n != "" {
			return n
		}
		return id
	}
}

// eventKind is what an event kind is called, as the QA document says it.
func eventKind(kind string) string {
	if k, ok := map[string]string{"open": "Question", "reply": "Reply", "question": "Question", "answer": "Answer", "assign": "Assigned", "block": "Blocked", "resolve": "Resolved", "reopen": "Reopened", "decision_request": "Owner decision requested", "decision_response": "Owner decision", "progress": "Progress", "instruction": "Instruction"}[kind]; ok {
		return k
	}
	return kind
}

var hexWord = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)

// commitHashes finds the commit hashes a body names: 7–40 hex characters
// with a letter and a digit each, so a date is not one.
func commitHashes(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range hexWord.FindAllString(body, -1) {
		if seen[h] || !strings.ContainsAny(h, "abcdef") || !strings.ContainsAny(h, "0123456789") {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}
