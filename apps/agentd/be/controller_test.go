package agentd

import "testing"

func resetControllersForTest() {
	controllerState.Lock()
	controllerState.byKey = map[string]string{}
	controllerState.byInstance = map[string]string{}
	controllerState.launching = map[string]bool{}
	controllerState.managers = map[string]struct{}{}
	controllerState.Unlock()
}

func TestManagerViewOmitsSessionOnlyCollections(t *testing.T) {
	state := State{Rows: []Row{{Key: "k", Configs: []Config{{ID: "model"}}, Commands: []Command{{Name: "review"}}, Modes: []Mode{{ID: "plan"}}, Roots: []string{"/work"}}}}
	got := managerView(state)
	if len(got.Rows) != 1 || len(got.Rows[0].Configs) != 0 || len(got.Rows[0].Commands) != 0 || len(got.Rows[0].Modes) != 0 {
		t.Fatalf("manager view retained session-only data: %#v", got)
	}
	if len(got.Rows[0].Roots) != 1 {
		t.Fatalf("manager view lost roster data: %#v", got)
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
	state := State{
		Rows:     []Row{{Key: "k1", Title: "one"}, {Key: "k2", Title: "two"}},
		Asks:     []Ask{{ID: "a1", RowKey: "k1"}, {ID: "a2", RowKey: "k2"}},
		Recent:   []Session{{SessionID: "history"}},
		Adapters: []Adapter{{ID: "codex"}},
	}
	got := sessionView(state, "k2")
	if len(got.Rows) != 1 || got.Rows[0].Key != "k2" || len(got.Asks) != 1 || got.Asks[0].ID != "a2" {
		t.Fatalf("session view = %#v", got)
	}
	if len(got.Recent) != 0 || len(got.Adapters) != 0 {
		t.Fatalf("session view leaked manager data: %#v", got)
	}
}
