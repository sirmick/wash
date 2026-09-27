package agentd

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

func resetControllersForTest() {
	controllerState.Lock()
	controllerState.byKey = map[string]string{}
	controllerState.byInstance = map[string]string{}
	controllerState.launching = map[string]bool{}
	controllerState.managers = map[string]struct{}{}
	controllerState.gone = map[string]time.Time{}
	controllerState.Unlock()
}

func TestManagerViewOmitsSessionOnlyCollections(t *testing.T) {
	state := agentproto.State{Rows: []agentproto.Row{{Key: "k", Configs: []agentproto.Config{{ID: "model"}}, Commands: []agentproto.Command{{Name: "review"}}, Modes: []agentproto.Mode{{ID: "plan"}}, Roots: []string{"/work"}}}}
	got := managerView(state)
	if len(got.Rows) != 1 || len(got.Rows[0].Configs) != 0 || len(got.Rows[0].Commands) != 0 || len(got.Rows[0].Modes) != 0 {
		t.Fatalf("manager view retained session-only data: %#v", got)
	}
	if len(got.Rows[0].Roots) != 1 {
		t.Fatalf("manager view lost roster data: %#v", got)
	}
}

func TestManagerViewPlacesMembersUnderTheirOrchestrator(t *testing.T) {
	s, err := swarm.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Setup("lead-s", "claude", t.TempDir(), "Team", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Mutate("lead-s", true, func(w *swarm.Workspace, _ *swarm.Member) error {
		w.Plan = []swarm.Node{{ID: "K5", Title: "Timer", State: "todo"}}
		w.Members = append(w.Members,
			swarm.Member{ID: "impl", Name: "implementer", Role: "implementer", Node: "K5", Session: "impl-s", State: "available"},
			swarm.Member{ID: "gone", Name: "old", Session: "gone-s", State: "ended"})
		return nil
	})
	old := workspaces
	workspaces = &workspaceService{store: s}
	defer func() { workspaces = old }()
	got := managerView(agentproto.State{Rows: []agentproto.Row{{Key: "a", SessionID: "lead-s"}, {Key: "b", SessionID: "impl-s"}, {Key: "c", SessionID: "gone-s"}, {Key: "d"}}})
	lead, impl := got.Rows[0].Workspace, got.Rows[1].Workspace
	if lead == nil || !lead.Orchestrator || lead.LeadSession != "lead-s" || lead.ID != w.ID {
		t.Fatalf("orchestrator row: %#v", lead)
	}
	if impl == nil || impl.Orchestrator || impl.LeadSession != "lead-s" || impl.Member != "implementer" || impl.NodeTitle != "Timer" {
		t.Fatalf("member row: %#v", impl)
	}
	if got.Rows[2].Workspace != nil || got.Rows[3].Workspace != nil {
		t.Fatal("ended member or plain session placed in a team")
	}
}

func TestControllerLeaseIsExclusive(t *testing.T) {
	resetControllersForTest()
	t.Cleanup(resetControllersForTest)

	if owner, ok := claimController("k1", "i1"); !ok || owner != "i1" {
		t.Fatalf("first claim = (%q,%v), want (i1,true)", owner, ok)
	}
	if owner, ok := claimController("k1", "i2"); ok || owner != "i1" {
		t.Fatalf("competing claim = (%q,%v), want (i1,false)", owner, ok)
	}
	if key := releaseController("i1"); key != "k1" {
		t.Fatalf("release = %q, want k1", key)
	}
	if owner, ok := claimController("k1", "i2"); !ok || owner != "i2" {
		t.Fatalf("claim after release = (%q,%v), want (i2,true)", owner, ok)
	}
}

func TestControllerLaunchReservationCoalesces(t *testing.T) {
	resetControllersForTest()
	t.Cleanup(resetControllersForTest)

	if !reserveControllerLaunch("k1") {
		t.Fatal("first launch reservation rejected")
	}
	if reserveControllerLaunch("k1") {
		t.Fatal("duplicate launch reservation accepted")
	}
	if _, ok := claimController("k1", "i1"); !ok {
		t.Fatal("spawned controller could not claim reservation")
	}
	if reserveControllerLaunch("k1") {
		t.Fatal("launch reserved while a controller exists")
	}
}

func TestSessionViewContainsOnlyRequestedSession(t *testing.T) {
	state := agentproto.State{
		Rows:     []agentproto.Row{{Key: "k1", Title: "one"}, {Key: "k2", Title: "two"}},
		Asks:     []agentproto.Ask{{ID: "a1", RowKey: "k1"}, {ID: "a2", RowKey: "k2"}},
		Recent:   []agentproto.Session{{SessionID: "history"}},
		Adapters: []agentproto.Adapter{{ID: "codex"}},
	}
	got := sessionView(state, "k2")
	if len(got.Rows) != 1 || got.Rows[0].Key != "k2" || len(got.Asks) != 1 || got.Asks[0].ID != "a2" {
		t.Fatalf("session view = %#v", got)
	}
	if len(got.Recent) != 0 || len(got.Adapters) != 0 {
		t.Fatalf("session view leaked manager data: %#v", got)
	}
}

// A roster change on one session must not re-send every other controller
// its unchanged view — that per-window copy is the fanout the split removes.
func TestControllerViewsSendOnlyWhatChanged(t *testing.T) {
	resetControllersForTest()
	oldSend, oldSvc, oldConn := viewSend, svc, controllerConn
	sent := map[string]int{}
	viewSend = func(instance string, _ any) { sent[instance]++ }
	viewMu.Lock()
	viewSent = map[string][]byte{}
	viewMu.Unlock()
	t.Cleanup(func() {
		resetControllersForTest()
		viewSend, svc, controllerConn = oldSend, oldSvc, oldConn
		viewMu.Lock()
		viewSent = map[string][]byte{}
		viewMu.Unlock()
	})

	// A zero service holds state without a bus; MutateIf returning false
	// writes it without trying to publish.
	svc = new(sdk.StateService[agentproto.State])
	svc.MutateIf(func(s *agentproto.State) bool {
		s.Rows = []agentproto.Row{{Key: "k1", Title: "one"}, {Key: "k2", Title: "two"}}
		return false
	})
	controllerConn = &sdk.Conn{}
	claimController("k1", "i1")
	claimController("k2", "i2")

	publishControllerViews()
	if sent["i1"] != 1 || sent["i2"] != 1 {
		t.Fatalf("first publish sent %v, want one view each", sent)
	}
	publishControllerViews()
	if sent["i1"] != 1 || sent["i2"] != 1 {
		t.Fatalf("unchanged publish re-sent views: %v", sent)
	}
	svc.MutateIf(func(s *agentproto.State) bool {
		s.Rows = []agentproto.Row{{Key: "k1", Title: "one"}, {Key: "k2", Title: "renamed"}}
		return false
	})
	publishControllerViews()
	if sent["i1"] != 1 || sent["i2"] != 2 {
		t.Fatalf("a k2 change sent %v, want only i2 again", sent)
	}
	// A row's age alone is not news: publish() restamps every row's since_ms
	// on every rebuild, so ignoring it is what lets the comparison work.
	svc.MutateIf(func(s *agentproto.State) bool {
		s.Rows = []agentproto.Row{{Key: "k1", Title: "one", SinceMS: 5000}, {Key: "k2", Title: "renamed", SinceMS: 7000}}
		return false
	})
	publishControllerViews()
	if sent["i1"] != 1 || sent["i2"] != 2 {
		t.Fatalf("an age-only change re-sent views: %v", sent)
	}
	// A claim or subscribe is a request for the current view and always answers.
	sendView("i1", "k1", func(s agentproto.State) agentproto.State { return sessionView(s, "k1") })
	if sent["i1"] != 2 {
		t.Fatalf("forced view not sent: %v", sent)
	}
}

// A window that died before its spawn result arrived must not take the
// lease: nothing would ever release it, and the session could never get a
// window again.
func TestClaimRefusesAnInstanceAlreadyGone(t *testing.T) {
	resetControllersForTest()
	t.Cleanup(resetControllersForTest)

	if !reserveControllerLaunch("k1") {
		t.Fatal("reservation rejected")
	}
	noteInstanceGone("i-dead", time.Now())
	if _, ok := claimController("k1", "i-dead"); ok {
		t.Fatal("a gone instance was granted the controller lease")
	}
	if controllerFor("k1") != "" {
		t.Fatalf("lease held by %q, want none", controllerFor("k1"))
	}
	// The failed claim released the launch reservation, so the session can
	// try again.
	if !reserveControllerLaunch("k1") {
		t.Fatal("session stuck launching after a claim for a dead window")
	}
	if _, ok := claimController("k1", "i-live"); !ok {
		t.Fatal("a live instance could not claim")
	}
}

// Roles replace app ids: a manager is whoever subscribed as one, and a
// session's controller is whoever holds its lease, whatever app either is.
func TestRolesAreClaimedNotAppIDs(t *testing.T) {
	resetControllersForTest()
	t.Cleanup(resetControllersForTest)

	if isManager("i-any") {
		t.Fatal("an instance that never subscribed is a manager")
	}
	controllerState.Lock()
	controllerState.managers["i-any"] = struct{}{}
	controllerState.Unlock()
	if !isManager("i-any") {
		t.Fatal("a subscribed manager was not recognised")
	}
	forgetManager("i-any")
	if isManager("i-any") {
		t.Fatal("a gone manager kept the role")
	}

	if _, ok := claimController("acp:1", "i-editor"); !ok {
		t.Fatal("a lease was refused to a non-ai app")
	}
	if !controls(wire.Sender{AppID: "com.wash.edit", InstanceID: "i-editor"}, "acp:1") {
		t.Fatal("the lease holder does not control its session")
	}
	if controls(wire.Sender{AppID: "com.wash.ai", InstanceID: "i-other"}, "acp:1") {
		t.Fatal("an Agent window controls a session it holds no lease on")
	}
	if controls(wire.Sender{}, "acp:1") {
		t.Fatal("an unattributed sender controls a session")
	}
}
