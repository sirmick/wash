package swarm

import (
	"path/filepath"
	"strings"
	"testing"
)

func planStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Setup("lead", "claude", t.TempDir(), "Plan", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("lead", true, func(w *Workspace, m *Member) error {
		w.Members = append(w.Members,
			Member{ID: "arch", Name: "Architect", Role: "architect", Session: "arch-s", State: "available", Lifetime: "resident"},
			Member{ID: "impl", Name: "Implementer", Role: "implementer", Node: "A", Session: "impl-s", State: "available", Lifetime: "resident", Creator: m.ID},
			Member{ID: "red", Name: "Red team", Role: "reviewer", Node: "A", Session: "red-s", State: "available", Lifetime: "resident", Creator: m.ID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s, w.Lead
}

func str(s string) *string { return &s }

func setPlan(s *Store, session string, patches map[string]*NodePatch) error {
	return s.Mutate(session, false, func(w *Workspace, m *Member) error { return ApplyPlan(w, m, patches) })
}

// The shakedown's plan: three milestones, the middle one expanded into A, B
// and C, where C needs A and B.
func seed(t *testing.T, s *Store) {
	t.Helper()
	needs := func(ids ...string) *[]string { return &ids }
	err := setPlan(s, "lead", map[string]*NodePatch{
		"M1": {Title: str("Plan"), Template: str("milestone"), State: str("active")},
		"M2": {Title: str("Build"), Template: str("milestone"), Needs: needs("M1")},
		"M3": {Title: str("Ship"), Template: str("milestone"), Needs: needs("M2")},
		"A":  {Title: str("alpha.txt"), Parent: str("M2")},
		"B":  {Title: str("beta.txt"), Parent: str("M2")},
		"C":  {Title: str("gamma.txt"), Parent: str("M2"), Needs: needs("A", "B")},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPlanValidationRefusesBrokenGraphs(t *testing.T) {
	s, _ := planStore(t)
	seed(t, s)
	before := s.Snapshot()
	for name, patch := range map[string]map[string]*NodePatch{
		"cycle":          {"A": {Needs: &[]string{"C"}}},
		"missing parent": {"D": {Title: str("d"), Parent: str("nope")}},
		"unknown need":   {"D": {Title: str("d"), Needs: &[]string{"nope"}}},
		"need ancestor":  {"A": {Needs: &[]string{"M2"}}},
		"parent cycle":   {"M2": {Parent: str("A")}},
		"bad state":      {"A": {State: str("Not Done")}},
		"bad template":   {"A": {Template: str("epic")}},
		"long body":      {"A": {Body: str(strings.Repeat("x", ReportLimit+1))}},
		"two lines":      {"A": {Title: str("a\nb")}},
		"needed node":    {"A": nil},
		"parent node":    {"M2": nil},
		"stale revision": {"A": {Title: str("x"), Expected: new(int64)}},
	} {
		if err := setPlan(s, "lead", patch); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	after := s.Snapshot()
	if len(after.Workspaces[0].Plan) != len(before.Workspaces[0].Plan) {
		t.Fatal("a refused change altered the plan")
	}
}

// Work starts only on a node whose needs are done, or with the reason
// recorded; it moves the node active, a result reported, and only the
// orchestrator sets done, with nothing open.
func TestWorkIsOnTheNodeAndNeedsGateTheStart(t *testing.T) {
	s, _ := planStore(t)
	seed(t, s)
	if _, err := s.Assign("lead", "impl", "C", "", "Write gamma", "", ""); err == nil || !strings.Contains(err.Error(), "needs") || !strings.Contains(err.Error(), "override") {
		t.Fatalf("C started before A and B: %v", err)
	}
	// M2's own need (M1) gates A too.
	if _, err := s.Assign("lead", "impl", "", "", "Write alpha", "", ""); err == nil || !strings.Contains(err.Error(), "M1") {
		t.Fatalf("A started inside an unstarted milestone: %v", err)
	}
	if err := setPlan(s, "lead", map[string]*NodePatch{"M1": {State: str("done")}}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Assign("lead", "impl", "", "", "Write alpha", "", "")
	if err != nil {
		t.Fatal(err)
	}
	w := s.View("lead")
	if a.Node != "A" || PlanNode(w, "A").State != "active" {
		t.Fatalf("assignment %+v, node %+v", a, PlanNode(w, "A"))
	}
	if err := setPlan(s, "lead", map[string]*NodePatch{"A": {State: str("done")}}); err == nil {
		t.Fatal("done with work open")
	}
	if err := s.Complete("impl-s", a.ID, "Wrote alpha.txt", false); err != nil {
		t.Fatal(err)
	}
	if got := PlanNode(s.View("lead"), "A").State; got != "reported" {
		t.Fatalf("A is %s after its result, want reported", got)
	}
	// The override is recorded on the node, with who and why.
	if _, err := s.Assign("lead", "red", "C", "fixture needs C early", "Review gamma", "", ""); err != nil {
		t.Fatal(err)
	}
	if o := PlanNode(s.View("lead"), "C").Overrides; len(o) != 1 || !strings.Contains(o[0], "fixture needs C early") || !strings.Contains(o[0], "Orchestrator") {
		t.Fatalf("override not recorded: %v", o)
	}
}

func TestArchitectPlansOnlyWhatHasNotStarted(t *testing.T) {
	s, _ := planStore(t)
	seed(t, s)
	if err := setPlan(s, "arch-s", map[string]*NodePatch{"D": {Title: str("delta.txt"), Parent: str("M3")}, "B": {Body: str("see docs/beta.md")}}); err != nil {
		t.Fatalf("Architect could not plan unstarted nodes: %v", err)
	}
	if err := setPlan(s, "arch-s", map[string]*NodePatch{"M1": {Body: str("x")}}); err == nil {
		t.Fatal("Architect changed an active node")
	}
	if err := setPlan(s, "arch-s", map[string]*NodePatch{"B": {State: str("active")}}); err == nil {
		t.Fatal("Architect moved a node's state")
	}
	if err := setPlan(s, "impl-s", map[string]*NodePatch{"B": {Body: str("x")}}); err == nil {
		t.Fatal("an implementer edited the plan")
	}
}

func TestAcceptWritesTrailersAndNudgesTheNextMilestone(t *testing.T) {
	s, lead := planStore(t)
	seed(t, s)
	if err := setPlan(s, "lead", map[string]*NodePatch{"M1": {State: str("done")}}); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Assign("lead", "impl", "", "", "Write alpha", "", "")
	_ = s.Complete("impl-s", a.ID, "Wrote alpha.txt", false)
	r, _ := s.Assign("lead", "red", "", "", "Review alpha", "", "")
	_ = s.Complete("red-s", r.ID, "OK: alpha.txt says alpha\nno findings", false)
	if err := s.Mutate("lead", false, func(w *Workspace, m *Member) error {
		_, err := UpdateQA(w, m, QAUpdate{ID: "A-case", Action: "open", Node: "A", Title: "Case", Assignee: "impl", Body: "Lower case?"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var got Accepted
	if err := s.Mutate("lead", false, func(w *Workspace, m *Member) error {
		var err error
		got, err = Accept(w, m, "A", []Gate{{Command: "grep -q alpha words/alpha.txt", ExitCode: 0}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := "Plan-Node: A\nQA: A-case\nGates: grep -q alpha words/alpha.txt 0\nReviewed-by: Red team: OK: alpha.txt says alpha"
	if got.Trailers != want {
		t.Fatalf("trailers:\n%s\nwant:\n%s", got.Trailers, want)
	}
	if PlanNode(s.View("lead"), "A").State != "done" {
		t.Fatal("accept did not set done")
	}
	// B and C done: M2 is complete, and the orchestrator hears about it once.
	if err := setPlan(s, "lead", map[string]*NodePatch{"B": {State: str("done")}, "C": {State: str("done")}}); err != nil {
		t.Fatal(err)
	}
	if err := setPlan(s, "lead", map[string]*NodePatch{"C": {Body: str("again")}}); err != nil {
		t.Fatal(err)
	}
	if err := setPlan(s, "lead", map[string]*NodePatch{"M2": {State: str("done")}}); err != nil {
		t.Fatal(err)
	}
	complete, sketch := 0, 0
	for _, msg := range s.View("lead").Messages {
		if msg.Sender == "wash" && msg.Recipient == lead {
			if strings.Contains(msg.Body, "Every node in milestone M2") {
				complete++
			}
			if strings.Contains(msg.Body, "M3 (Ship) is next, still a sketch") {
				sketch++
			}
		}
	}
	if complete != 1 || sketch != 1 {
		t.Fatalf("nudges: complete %d, sketch %d", complete, sketch)
	}
}

func TestReplacePlanRefusedWhileWorkIsOpen(t *testing.T) {
	s, _ := planStore(t)
	seed(t, s)
	_ = setPlan(s, "lead", map[string]*NodePatch{"M1": {State: str("done")}})
	if _, err := s.Assign("lead", "impl", "", "", "Write alpha", "", ""); err != nil {
		t.Fatal(err)
	}
	err := s.Mutate("lead", false, func(w *Workspace, m *Member) error {
		return ReplacePlan(w, m, []Node{{ID: "X", Title: "x"}})
	})
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("plan replaced under open work: %v", err)
	}
}

// The same reason to start early is one override on the node, however many
// members start on it; and a reviewer launched with its enforcement
// unverified says so in the Reviewed-by trailer it earns.
func TestOverridesRecordOnceAndUnverifiedReviewsSaySo(t *testing.T) {
	s, _ := planStore(t)
	seed(t, s)
	if err := setPlan(s, "lead", map[string]*NodePatch{"M1": {State: str("done")}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Mutate("lead", true, func(w *Workspace, _ *Member) error {
		GetMember(w, "red").LaunchSettings = &AgentProfile{Provider: "claude", Capability: "reviewer", Enforcement: "unverified"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"impl", "red"} {
		if _, err := s.Assign("lead", member, "C", "the owner wants gamma drafted alongside", "Start gamma", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if o := PlanNode(s.View("lead"), "C").Overrides; len(o) != 1 {
		t.Fatalf("one decision, %d overrides: %v", len(o), o)
	}
	r, err := s.Assign("lead", "red", "A", "", "Review alpha", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// The queued review runs after the override one; resolve that first.
	for _, a := range s.View("lead").Assignments {
		if a.Member == "red" && a.Node == "C" {
			_ = s.Complete("red-s", a.ID, "Gamma looked at.", false)
		}
	}
	if err := s.Complete("red-s", r.ID, "OK, no findings", false); err != nil {
		t.Fatal(err)
	}
	var got Accepted
	if err := s.Mutate("lead", false, func(w *Workspace, m *Member) error {
		var err error
		got, err = Accept(w, m, "A", nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Trailers, "Reviewed-by: Red team: OK, no findings [enforcement unverified]") {
		t.Fatalf("trailers:\n%s", got.Trailers)
	}
}
