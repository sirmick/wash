// Unified diffs for the transcript (docs/Review-findings.md P2 → agent:
// "diffs the agent made are not viewable").
//
// An ACP `diff` content block carries the whole file before and after.
// Shipping both to every window is the wrong shape — a 4,000-line file
// with a one-line change would cost 8,000 lines on the wire and render as
// nothing readable — so agentd turns the pair into a unified diff here,
// once, and the transcript event carries the text a person expects to
// see: hunks, context, +/- lines.
//
// The algorithm is a plain LCS over lines. Myers would be smaller in the
// worst case and is more code; the inputs are source files an agent just
// edited, and the quadratic table is capped so a pathological pair costs
// a coarse diff rather than the service.
package agentd

import (
	"fmt"
	"strings"
)

// diffContext is how many unchanged lines frame each hunk — the number
// every diff tool defaults to, and the one people can read.
const diffContext = 3

// maxDiffCells bounds the LCS table (old lines × new lines). Past it the
// diff degrades to "everything removed, everything added", which is
// still a correct diff, just not a minimal one.
const maxDiffCells = 4_000_000

// maxDiffBytes bounds the rendered text stored on an event. A transcript
// is pushed to every watcher and held in memory; a diff past this is
// truncated with a note rather than dropped.
const maxDiffBytes = 256 << 10

// unifiedDiff renders old → new as a unified diff headed by path. A nil
// old is a created file, a nil new a deleted one. Returns "" when the
// two are identical.
func unifiedDiff(path string, oldText, newText *string) string {
	var a, b []string
	if oldText != nil {
		a = splitDiffLines(*oldText)
	}
	if newText != nil {
		b = splitDiffLines(*newText)
	}
	ops := diffLines(a, b)
	changed := false
	for _, op := range ops {
		if op.kind != ' ' {
			changed = true
			break
		}
	}
	if !changed {
		return ""
	}

	var sb strings.Builder
	from, to := "a/"+strings.TrimPrefix(path, "/"), "b/"+strings.TrimPrefix(path, "/")
	if oldText == nil {
		from = "/dev/null"
	}
	if newText == nil {
		to = "/dev/null"
	}
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", from, to)

	// Group ops into hunks: a run of changes plus diffContext lines of
	// context either side, merged when two runs are within 2*context.
	i := 0
	for i < len(ops) {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := i - diffContext
		if start < 0 {
			start = 0
		}
		end := i
		for end < len(ops) {
			// Extend through this change run, then look ahead for the
			// next one within reach.
			for end < len(ops) && ops[end].kind != ' ' {
				end++
			}
			next := end
			for next < len(ops) && ops[next].kind == ' ' && next-end < 2*diffContext {
				next++
			}
			if next < len(ops) && ops[next].kind != ' ' {
				end = next
				continue
			}
			break
		}
		stop := end + diffContext
		if stop > len(ops) {
			stop = len(ops)
		}
		// Line numbers: count what precedes `start` on each side.
		oldStart, newStart := 1, 1
		for _, op := range ops[:start] {
			if op.kind != '+' {
				oldStart++
			}
			if op.kind != '-' {
				newStart++
			}
		}
		oldN, newN := 0, 0
		for _, op := range ops[start:stop] {
			if op.kind != '+' {
				oldN++
			}
			if op.kind != '-' {
				newN++
			}
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", hunkRange(oldStart, oldN), hunkRange(newStart, newN))
		for _, op := range ops[start:stop] {
			sb.WriteByte(byte(op.kind))
			sb.WriteString(op.text)
			sb.WriteByte('\n')
			if sb.Len() > maxDiffBytes {
				sb.WriteString("… (diff truncated)\n")
				return sb.String()
			}
		}
		i = stop
	}
	return sb.String()
}

func hunkRange(start, n int) string {
	if n == 1 {
		return fmt.Sprint(start)
	}
	if n == 0 {
		// An empty side is conventionally written as the line BEFORE.
		return fmt.Sprintf("%d,0", start-1)
	}
	return fmt.Sprintf("%d,%d", start, n)
}

// splitDiffLines splits on newlines without inventing a trailing empty
// line for a file that ends in one.
func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type diffOp struct {
	kind rune // ' ', '-', '+'
	text string
}

// diffLines is the LCS line diff: an edit script of keep/remove/add.
func diffLines(a, b []string) []diffOp {
	// Trim the common prefix and suffix first: an agent edit is usually a
	// few lines in the middle of a file, and this keeps the table small.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]

	out := make([]diffOp, 0, len(a)+len(b))
	for _, l := range a[:pre] {
		out = append(out, diffOp{' ', l})
	}
	if len(ma)*len(mb) > maxDiffCells {
		for _, l := range ma {
			out = append(out, diffOp{'-', l})
		}
		for _, l := range mb {
			out = append(out, diffOp{'+', l})
		}
	} else {
		out = append(out, lcsOps(ma, mb)...)
	}
	for _, l := range a[len(a)-suf:] {
		out = append(out, diffOp{' ', l})
	}
	return out
}

// lcsOps builds the edit script from a longest-common-subsequence table.
func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		out := make([]diffOp, 0, n+m)
		for _, l := range a {
			out = append(out, diffOp{'-', l})
		}
		for _, l := range b {
			out = append(out, diffOp{'+', l})
		}
		return out
	}
	// table[i][j] = LCS length of a[i:], b[j:], stored flat.
	w := m + 1
	table := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i*w+j] = table[(i+1)*w+j+1] + 1
			} else if table[(i+1)*w+j] >= table[i*w+j+1] {
				table[i*w+j] = table[(i+1)*w+j]
			} else {
				table[i*w+j] = table[i*w+j+1]
			}
		}
	}
	out := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffOp{' ', a[i]})
			i++
			j++
		case table[(i+1)*w+j] >= table[i*w+j+1]:
			out = append(out, diffOp{'-', a[i]})
			i++
		default:
			out = append(out, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffOp{'+', b[j]})
	}
	return out
}
