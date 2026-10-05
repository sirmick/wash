package agentd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/swarm"
)

var st0 = time.Unix(1_790_000_000, 0)

func supWorkspace() *swarm.Workspace {
	return &swarm.Workspace{
		ID: "w", Name: "W", State: "active", Lead: "lead", MaxActive: 4,
		Members: []swarm.Member{
			{ID: "lead", Name: "Orchestrator", Key: "orchestrator", Session: "lead-s", State: "available"},
			{ID: "fmt", Name: "FMT1 implementer", Key: "fmt1", Session: "fmt-s", State: "available", Node: "FMT1"},
		},
		Plan:        []swarm.Node{{ID: "FMT1", Title: "Format", State: "active"}},
		Assignments: []swarm.Assignment{{ID: "a1", Member: "fmt", Node: "FMT1", State: "active"}},
	}
}

func keys(fs []finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.key)
	}
	return strings.Join(out, ",")
}

// The Redoubt hang: in a turn, silent, mail queued behind it.
func TestSupervisorFindsAWedgedMember(t *testing.T) {
	w := supWorkspace()
	w.Messages = []swarm.Message{{ID: "m", Recipient: "fmt", State: "queued", Created: st0.UnixMilli()}}
	rt := func(r memberRuntime) map[string]memberRuntime {
		return map[string]memberRuntime{"lead": {live: true, heard: st0}, "fmt": r}
	}
	now := st0.Add(6 * time.Minute)
	s := newSupervisor()
	// One snapshot is not an alert: a member between a tool ending and its
	// next output looked wedged and was not. The second check confirms.
	if got := s.findings(w, rt(memberRuntime{live: true, busy: true, heard: st0}), now); len(got) != 0 {
		t.Fatalf("wedged on first sight: %+v", got)
	}
	got := s.findings(w, rt(memberRuntime{live: true, busy: true, heard: st0}), now.Add(superviseEvery))
	if keys(got) != "wedged:fmt" || !strings.Contains(got[0].text, "1 message(s) queued") || !strings.Contains(got[0].text, "fmt1") || !strings.Contains(got[0].text, "two checks") {
		t.Fatalf("findings = %+v", got)
	}
	// A finding that went away between checks starts over.
	s.findings(w, rt(memberRuntime{live: true, busy: true, tool: true, heard: st0}), now.Add(2*superviseEvery))
	if got := s.findings(w, rt(memberRuntime{live: true, busy: true, heard: st0}), now.Add(3*superviseEvery)); len(got) != 0 {
		t.Fatalf("a lapsed finding fired on one sight: %+v", got)
	}
	for name, r := range map[string]memberRuntime{
		"a tool is open":     {live: true, busy: true, tool: true, heard: st0},
		"its processes work": {live: true, busy: true, cpuBusy: true, heard: st0},
		"it spoke recently":  {live: true, busy: true, heard: now.Add(-time.Minute)},
		"a question is open": {live: true, busy: true, asks: true, heard: st0},
	} {
		s := newSupervisor()
		s.findings(w, rt(r), now)
		if got := s.findings(w, rt(r), now.Add(superviseEvery)); strings.Contains(keys(got), "wedged") {
			t.Errorf("%s, and still wedged: %+v", name, got)
		}
	}
}

// A wedged orchestrator cannot be told; the owner is.
func TestSupervisorTellsTheOwnerAboutAWedgedOrchestrator(t *testing.T) {
	w := supWorkspace()
	s := newSupervisor()
	rt := map[string]memberRuntime{"lead": {live: true, busy: true, heard: st0}, "fmt": {live: true, busy: true, tool: true}}
	s.findings(w, rt, st0.Add(6*time.Minute))
	got := s.findings(w, rt, st0.Add(6*time.Minute+superviseEvery))
	if len(got) != 1 || !got[0].owner {
		t.Fatalf("findings = %+v", got)
	}
}

func TestSupervisorFindsIdleWorkAfterIdle(t *testing.T) {
	w := supWorkspace()
	s := newSupervisor()
	rt := map[string]memberRuntime{"lead": {live: true}, "fmt": {live: true}}
	if got := s.findings(w, rt, st0); strings.Contains(keys(got), "idle:") {
		t.Fatal("idle work reported on first sight")
	}
	if got := s.findings(w, rt, st0.Add(61*time.Second)); !strings.Contains(keys(got), "idle:fmt:a1") {
		t.Fatalf("findings = %+v", got)
	}
	w.Members[1].Waiting = "the bench"
	if got := newSupervisor().findings(w, rt, st0.Add(2*time.Minute)); strings.Contains(keys(got), "idle:") {
		t.Fatal("a member that set waiting counted as idle with work")
	}
}

func TestSupervisorFindsStoppedWork(t *testing.T) {
	w := supWorkspace()
	w.Members[1].State = "paused"
	got := newSupervisor().findings(w, map[string]memberRuntime{"lead": {live: true}}, st0)
	if !strings.Contains(keys(got), "stopped:a1:paused") {
		t.Fatalf("findings = %+v", got)
	}
	w.Members[1].State = "available"
	got = newSupervisor().findings(w, map[string]memberRuntime{"lead": {live: true}}, st0)
	if !strings.Contains(keys(got), "stopped:a1:offline") {
		t.Fatalf("findings = %+v", got)
	}
}

// The whole team idle, with work left: said once the team has been idle
// for idle, and never while anyone works or the owner is being asked.
func TestSupervisorFindsATeamStall(t *testing.T) {
	w := supWorkspace()
	w.Assignments = nil
	w.Plan = []swarm.Node{
		{ID: "A", Title: "Alpha", State: "reported"},
		{ID: "B", Title: "Beta", State: "todo"},
		{ID: "C", Title: "Gamma", State: "todo", Needs: []string{"A"}},
		{ID: "N", Title: "Note", Template: "note", State: "todo"},
	}
	w.Members[1].Waiting = "review of A"
	idle := map[string]memberRuntime{"lead": {live: true}, "fmt": {live: true}}
	s := newSupervisor()
	if got := s.findings(w, idle, st0); len(got) != 0 {
		t.Fatalf("stall reported on first sight: %+v", got)
	}
	got := s.findings(w, idle, st0.Add(61*time.Second))
	if k := keys(got); !strings.Contains(k, "reported:A") || !strings.Contains(k, "ready:B") || strings.Contains(k, "ready:C") || strings.Contains(k, ":N") || !strings.Contains(k, "waiting:") {
		t.Fatalf("findings = %s", k)
	}

	busy := map[string]memberRuntime{"lead": {live: true}, "fmt": {live: true, busy: true, heard: st0.Add(2 * time.Minute)}}
	if got := s.findings(w, busy, st0.Add(2*time.Minute)); len(got) != 0 {
		t.Fatalf("stall while a member works: %+v", got)
	}
	w.Messages = []swarm.Message{{Type: "decision_request", State: "recorded"}}
	s = newSupervisor()
	s.findings(w, idle, st0)
	if got := s.findings(w, idle, st0.Add(2*time.Minute)); len(got) != 0 {
		t.Fatalf("stall while the owner is asked: %+v", got)
	}
}

// Mail that sits queued for a member free to take it.
func TestSupervisorFindsUndeliveredMail(t *testing.T) {
	w := supWorkspace()
	w.Members[1].Waiting = "x"
	w.Messages = []swarm.Message{{ID: "m", Recipient: "fmt", Type: "instruction", State: "queued", Created: st0.UnixMilli()}}
	now := st0.Add(6 * time.Minute)
	rt := map[string]memberRuntime{"lead": {live: true, busy: true, heard: now}, "fmt": {live: true, takesATurn: true}}
	s := newSupervisor()
	if got := s.findings(w, rt, now); len(got) != 0 {
		t.Fatalf("undelivered on first sight: %+v", got)
	}
	if got := s.findings(w, rt, now.Add(superviseEvery)); keys(got) != "queued:fmt" || !strings.Contains(got[0].text, "nothing has been delivered to it yet") {
		t.Fatalf("findings = %+v", got)
	}
	w.MaxActive = 0
	s = newSupervisor()
	s.findings(w, rt, now)
	if got := s.findings(w, rt, now.Add(superviseEvery)); len(got) != 0 {
		t.Fatalf("mail held by max_active reported: %+v", got)
	}
}

// Two things that look identical to a watchdog counting queued messages, and
// are both healthy: the agent is running a turn of its own (Wash holds the
// prompt on purpose, and busy() does not see that turn), and the store is
// holding the mail itself until a waiting set resolves. Reporting either sent
// the orchestrator to interrupt a member that was working.
func TestSupervisorLeavesHeldMailAlone(t *testing.T) {
	now := st0.Add(6 * time.Minute)
	lead := memberRuntime{live: true, busy: true, heard: now}

	// In the agent's own turn: no Wash turn is live, so busy() is false,
	// but dispatch could not have claimed the session anyway.
	w := supWorkspace()
	w.Messages = []swarm.Message{{ID: "m", Recipient: "fmt", Type: "instruction", State: "queued", Created: st0.UnixMilli()}}
	rt := map[string]memberRuntime{"lead": lead, "fmt": {live: true, takesATurn: false}}
	if got := newSupervisor().findings(w, rt, now); strings.Contains(keys(got), "queued") {
		t.Fatalf("mail behind the agent's own turn reported: %+v", got)
	}

	// Held by the store: a result for an assignment in the member's waiting
	// set does not go out until the whole set resolves.
	w = supWorkspace()
	w.Members[1].Waiting = "the review round"
	w.Members[1].WaitingOn = []string{"a2"}
	w.Assignments = append(w.Assignments, swarm.Assignment{ID: "a2", Member: "lead", Node: "FMT1", State: "active"})
	w.Messages = []swarm.Message{{ID: "m", Recipient: "fmt", Type: "result", Assignment: "a2", State: "queued", Created: st0.UnixMilli()}}
	rt = map[string]memberRuntime{"lead": lead, "fmt": {live: true, takesATurn: true}}
	if got := newSupervisor().findings(w, rt, now); strings.Contains(keys(got), "queued") {
		t.Fatalf("mail the store holds on purpose reported: %+v", got)
	}
}

func supStore(t *testing.T) (*workspaceService, *swarm.Workspace) {
	t.Helper()
	s, err := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Setup("lead-s", "codex", "/p", "P", "/p")
	if err != nil {
		t.Fatal(err)
	}
	return &workspaceService{store: s, sup: newSupervisor(), kick: make(chan struct{}, 1)}, w
}

func (ws *workspaceService) supMessages(to string) []swarm.Message {
	var out []swarm.Message
	for _, m := range ws.store.View("lead-s").Messages {
		if m.Sender == "wash" && m.Recipient == to {
			out = append(out, m)
		}
	}
	return out
}

// Told once; again after repeat, doubling; the owner after max_prompts;
// then quiet. A finding that goes away and comes back is news at once.
func TestSupervisorRepeatsThenTellsTheOwner(t *testing.T) {
	ws, w := supStore(t)
	f := []finding{{key: "wedged:x", text: "wedged: X"}}
	lead := func() int { return len(ws.supMessages(w.Lead)) }
	owner := func() int { return len(ws.supMessages("human")) }

	at := st0
	ws.report(w, f, at)
	if lead() != 1 || owner() != 0 {
		t.Fatalf("first: lead %d owner %d", lead(), owner())
	}
	if m := ws.supMessages(w.Lead)[0]; m.Type != "lifecycle" || !strings.Contains(m.Body, "wedged: X") {
		t.Fatalf("message = %+v", m)
	}
	ws.report(w, f, at.Add(4*time.Minute))
	if lead() != 1 {
		t.Fatal("repeated before repeat")
	}
	at = at.Add(5 * time.Minute)
	ws.report(w, f, at)
	if lead() != 2 {
		t.Fatal("not repeated after repeat")
	}
	ws.report(w, f, at.Add(9*time.Minute))
	if lead() != 2 {
		t.Fatal("the second repeat did not double")
	}
	at = at.Add(10 * time.Minute)
	ws.report(w, f, at)
	if lead() != 3 {
		t.Fatal("third prompt missing")
	}
	at = at.Add(20 * time.Minute)
	ws.report(w, f, at)
	if lead() != 3 || owner() != 1 {
		t.Fatalf("after max_prompts: lead %d owner %d", lead(), owner())
	}
	ws.report(w, f, at.Add(time.Hour))
	if lead() != 3 || owner() != 1 {
		t.Fatal("kept going after telling the owner")
	}

	ws.report(w, nil, at.Add(2*time.Hour))
	ws.report(w, f, at.Add(2*time.Hour+time.Second))
	if lead() != 4 {
		t.Fatal("a finding that came back was not news")
	}
}

// A prompt carries only what is due. The body used to list every current
// finding whenever any one of them was due, so a finding still inside its
// backoff — or already escalated to the owner and meant to be quiet — was
// re-sent with its counters untouched, for as long as it held.
func TestSupervisorPromptsOnlyWhatIsDue(t *testing.T) {
	ws, w := supStore(t)
	old := finding{key: "wedged:x", text: "wedged: X"}
	fresh := finding{key: "ready:m1", text: "ready: M1 has nobody on it"}

	at := st0
	ws.report(w, []finding{old}, at)
	// X is now backed off for 5 minutes. A minute later a second finding
	// appears: its prompt must not drag X along.
	at = at.Add(time.Minute)
	ws.report(w, []finding{old, fresh}, at)
	msgs := ws.supMessages(w.Lead)
	if len(msgs) != 2 {
		t.Fatalf("prompts = %d, want 2", len(msgs))
	}
	if body := msgs[1].Body; strings.Contains(body, "wedged: X") || !strings.Contains(body, "ready: M1") {
		t.Fatalf("a finding inside its backoff was re-sent: %q", body)
	}
	// X keeps its place in the ladder: it is due 5 minutes after its first
	// prompt, not restarted by the second finding's.
	ws.report(w, []finding{old, fresh}, at.Add(3*time.Minute))
	if n := len(ws.supMessages(w.Lead)); n != 2 {
		t.Fatalf("prompted before either was due: %d", n)
	}
	ws.report(w, []finding{old, fresh}, at.Add(5*time.Minute))
	if body := ws.supMessages(w.Lead)[2].Body; !strings.Contains(body, "wedged: X") {
		t.Fatalf("X was not repeated when it came due: %q", body)
	}
}

func TestSupervisorOffAndSettings(t *testing.T) {
	w := &swarm.Workspace{Supervisor: swarm.Supervisor{Quiet: "30s", MaxPrompts: 1}}
	quiet, idle, repeat, max := supervisorSettings(w)
	if quiet != 30*time.Second || idle != defaultIdle || repeat != defaultRepeat || max != 1 {
		t.Fatal(quiet, idle, repeat, max)
	}
	for _, bad := range []swarm.Supervisor{{Quiet: "1s"}, {Idle: "soon"}, {MaxPrompts: -1}} {
		if bad.Validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestTreeTicksCountsDescendants(t *testing.T) {
	procs := map[int][2]uint64{10: {1, 5}, 11: {10, 7}, 12: {11, 100}, 20: {1, 1000}}
	if got := treeTicks(procs, 10); got != 112 {
		t.Fatalf("ticks = %d", got)
	}
	if p, ok := readProcs()[os.Getpid()]; !ok || p[0] != uint64(os.Getppid()) {
		t.Fatalf("readProcs does not see this process with its parent: %v %v", p, ok)
	}
}
