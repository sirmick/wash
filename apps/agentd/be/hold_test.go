package agentd

import (
	"strings"
	"testing"
	"time"
)

// The router's idea of idle is "no browser attached". For a desktop that
// is a sound proxy for "nothing of value is happening"; for an agent it is
// the exact inverse — nobody watching is when the work matters most. This
// service is the only thing on the box that knows the difference.
func TestHoldReasonSpeaksForRunningAgents(t *testing.T) {
	cases := []struct {
		name  string
		rows  map[string]*row
		want  string
		empty bool
	}{
		{
			name:  "quiet roster lets the session go",
			rows:  map[string]*row{"acp:1": {Row: Row{State: "done"}}},
			empty: true,
		},
		{
			name: "a working agent holds the session",
			rows: map[string]*row{"acp:1": {Row: Row{State: "working"}}},
			want: "1 agent(s) working",
		},
		{
			name: "an agent blocked on an absent human holds it too",
			rows: map[string]*row{"acp:1": {Row: Row{State: "needs-input"}, stateSince: t0}},
			want: "1 agent(s) waiting on you",
		},
		{
			name: "both are reported, because both are reasons",
			rows: map[string]*row{
				"acp:1": {Row: Row{State: "working"}},
				"acp:2": {Row: Row{State: "needs-input"}, stateSince: t0},
			},
			want: "1 agent(s) working, 1 waiting on you",
		},
		{
			name:  "a stale row is not a reason to stay up",
			rows:  map[string]*row{"acp:1": {Row: Row{State: "stale"}}},
			empty: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			for k, r := range tc.rows {
				rows[k] = r
			}
			got := holdReason(t0.Add(time.Second))
			if tc.empty {
				if got != "" {
					t.Errorf("holdReason = %q, want empty — nothing here outlives the browser", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("holdReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// needs-input cuts both ways: it is the state you most want to survive a
// disconnect, and the only one that resolves on nothing. So it gets a
// ceiling rather than a rule.
func TestNeedsInputHoldExpires(t *testing.T) {
	reset()
	rows["acp:1"] = &row{Row: Row{State: "needs-input"}, stateSince: t0}

	if got := holdReason(t0.Add(needsInputHold - time.Minute)); !strings.Contains(got, "waiting on you") {
		t.Errorf("inside the ceiling holdReason = %q, want a hold", got)
	}
	if got := holdReason(t0.Add(needsInputHold + time.Minute)); got != "" {
		t.Errorf("past the ceiling holdReason = %q, want empty — a forgotten session must be allowed to go", got)
	}
	if needsInputHold <= time.Hour {
		t.Error("the ceiling must at least survive the overnight disconnect this was written for")
	}
}
