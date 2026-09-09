package agentd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/acp"
)

func strp(s string) *string { return &s }

// The shape people read: file headers, hunk ranges, three lines of
// context, +/- lines. A one-line change in the middle of a file must not
// carry the whole file with it.
func TestUnifiedDiffIsHunkedWithContext(t *testing.T) {
	var old, cur strings.Builder
	for i := 1; i <= 20; i++ {
		line := "line " + itoa(uint64(i)) + "\n"
		old.WriteString(line)
		if i == 10 {
			cur.WriteString("line ten, changed\n")
			continue
		}
		cur.WriteString(line)
	}
	got := unifiedDiff("/w/notes.md", strp(old.String()), strp(cur.String()))
	want := strings.Join([]string{
		"--- a/w/notes.md",
		"+++ b/w/notes.md",
		"@@ -7,7 +7,7 @@",
		" line 7",
		" line 8",
		" line 9",
		"-line 10",
		"+line ten, changed",
		" line 11",
		" line 12",
		" line 13",
		"",
	}, "\n")
	if got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
}

// Two changes far apart are two hunks; two close together merge.
func TestUnifiedDiffMergesNearbyHunks(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "l" + itoa(uint64(i))
	}
	old := strings.Join(lines, "\n") + "\n"
	far := append([]string(nil), lines...)
	far[2] = "X"
	far[25] = "Y"
	got := unifiedDiff("f", strp(old), strp(strings.Join(far, "\n")+"\n"))
	if strings.Count(got, "@@ -") != 2 {
		t.Errorf("far-apart changes should be two hunks:\n%s", got)
	}
	near := append([]string(nil), lines...)
	near[10] = "X"
	near[14] = "Y"
	got = unifiedDiff("f", strp(old), strp(strings.Join(near, "\n")+"\n"))
	if strings.Count(got, "@@ -") != 1 {
		t.Errorf("changes within reach should share a hunk:\n%s", got)
	}
}

// Created and deleted files are named as /dev/null on the missing side,
// which is what every diff reader expects.
func TestUnifiedDiffCreateAndDelete(t *testing.T) {
	got := unifiedDiff("/w/new.go", nil, strp("package x\n"))
	if !strings.HasPrefix(got, "--- /dev/null\n+++ b/w/new.go\n@@ -0,0 +1 @@\n+package x\n") {
		t.Errorf("create:\n%s", got)
	}
	got = unifiedDiff("/w/old.go", strp("a\nb\n"), nil)
	if !strings.HasPrefix(got, "--- a/w/old.go\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b\n") {
		t.Errorf("delete:\n%s", got)
	}
	if unifiedDiff("same", strp("x\n"), strp("x\n")) != "" {
		t.Error("identical texts produced a diff")
	}
}

// A pair too big for the table still yields a correct (coarse) diff.
func TestUnifiedDiffDegradesPastTheCap(t *testing.T) {
	big := strings.Repeat("a\n", 3000)
	other := strings.Repeat("b\n", 3000)
	got := unifiedDiff("f", strp(big), strp(other))
	if !strings.Contains(got, "-a\n") || !strings.Contains(got, "+b\n") {
		t.Error("coarse diff lost a side")
	}
	if len(got) > maxDiffBytes+64 {
		t.Errorf("diff not truncated: %d bytes", len(got))
	}
}

// The wire shape, end to end: a tool_call carrying a `diff` content block
// and a location, as claude-agent-acp sends for an Edit, lands in the
// transcript as a row with a path and a rendered diff. types.go dropped
// both fields before; this is the assertion that they are read.
func TestToolCallDiffBlockReachesTheTranscript(t *testing.T) {
	withStateDir(t)
	raw := `{"sessionUpdate":"tool_call","toolCallId":"t-9","kind":"edit","title":"Edit notes.md","status":"completed",
	  "locations":[{"path":"/w/notes.md","line":2}],
	  "content":[{"type":"diff","path":"/w/notes.md","oldText":"a\nb\n","newText":"a\nc\n"}]}`
	var u acp.SessionUpdate
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	evs := appendUpdate("acp:diff", u, t0)
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	e := evs[0]
	if e.Kind != EventTool || e.Path != "/w/notes.md" {
		t.Errorf("event = %+v, want a tool row pointing at /w/notes.md", e)
	}
	if !strings.Contains(e.Diff, "-b\n+c\n") {
		t.Errorf("diff not rendered: %q", e.Diff)
	}
	// An update that carries no diff keeps the one already there.
	var upd acp.SessionUpdate
	_ = json.Unmarshal([]byte(`{"sessionUpdate":"tool_call_update","toolCallId":"t-9","status":"completed"}`), &upd)
	evs = appendUpdate("acp:diff", upd, t0)
	if len(evs) != 1 || evs[0].Diff != e.Diff || evs[0].Path != e.Path {
		t.Errorf("update lost the diff: %+v", evs)
	}
}
