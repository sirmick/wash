package swarm

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func configPatch(t *testing.T, raw string) ConfigurePatch {
	t.Helper()
	var p ConfigurePatch
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestConfigureAtomicPersistenceAndAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Setup("lead", "codex", t.TempDir(), "Team", "", nil); err != nil {
		t.Fatal(err)
	}
	revision, err := s.Configure("lead", configPatch(t, `{"catalog":"openai-budget","expected_revision":1}`))
	if err != nil || revision != 2 {
		t.Fatal(revision, err)
	}
	for _, raw := range []string{
		`{"name":""}`,
		`{"max_active":0}`, `{"max_active":17}`, `{"max_members":0}`,
		`{"expected_revision":1,"name":"stale"}`,
	} {
		before := s.Snapshot()
		if _, err := s.Configure("lead", configPatch(t, raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
		if !reflect.DeepEqual(before, s.Snapshot()) {
			t.Fatalf("partial mutation: %s", raw)
		}
	}
	if err := s.Mutate("lead", true, func(w *Workspace, _ *Member) error {
		w.Members = append(w.Members, Member{ID: "worker", Session: "worker", State: "available", CanSpawn: true})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"worker", "stranger"} {
		if _, err := s.Configure(session, configPatch(t, `{"name":"not allowed"}`)); err == nil {
			t.Fatal("unauthorized configuration")
		}
	}
	if _, err := s.Configure("lead", configPatch(t, `{"max_active":1,"max_members":1}`)); err == nil {
		t.Fatal("lowered below live membership")
	}
	if _, err := s.Configure("lead", configPatch(t, `{"catalog":"openai-pro","name":"Renamed"}`)); err != nil {
		t.Fatal(err)
	}
	w := s.View("lead")
	if w.Catalog != "openai-pro" || w.Name != "Renamed" {
		t.Fatal("catalog change not applied", w)
	}
	recovered, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := recovered.View("lead"); got.Catalog != "openai-pro" || !reflect.DeepEqual(got.Packages, w.Packages) {
		t.Fatal("configuration not persisted", got)
	}
}
func TestConfigureConcurrentRevision(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "workspace.json"))
	if _, err := s.Setup("lead", "codex", t.TempDir(), "Team", "", nil); err != nil {
		t.Fatal(err)
	}
	patch := configPatch(t, `{"expected_revision":1,"name":"Winner"}`)
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Configure("lead", patch); results <- err }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 || s.View("lead").Revision != 2 {
		t.Fatal("lost update", successes)
	}
}
func TestSuccessfulConversationTurnPreservesConfigurationRevision(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "workspace.json"))
	w, err := s.Setup("lead", "codex", t.TempDir(), "Team", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TurnEnded("lead", nil, false); err != nil {
		t.Fatal(err)
	}
	if s.View("lead").Revision != w.Revision {
		t.Fatal("read-only conversation turn invalidated state revision")
	}
	if _, err = s.Configure("lead", ConfigurePatch{Expected: &w.Revision}); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalProfileValidatesAndResolves(t *testing.T) {
	for _, c := range []struct {
		p  AgentProfile
		ok bool
	}{
		{AgentProfile{Provider: "claude"}, true},
		{AgentProfile{Provider: "claude", Approval: "ask"}, true},
		{AgentProfile{Provider: "claude", Approval: "auto"}, true},
		{AgentProfile{Provider: "claude", Approval: "yolo"}, false},
		{AgentProfile{Provider: "claude", Approval: "auto", Capability: "reviewer"}, false},
	} {
		if err := ValidateProfile(c.p); (err == nil) != c.ok {
			t.Errorf("%+v: err=%v, want ok=%v", c.p, err, c.ok)
		}
	}
	base := AgentProfile{Provider: "claude", Approval: "auto"}
	if got, err := Overlay(base, AgentProfile{}); err != nil || got.Approval != "auto" {
		t.Fatalf("base approval lost: %+v %v", got, err)
	}
	if got, err := Overlay(base, AgentProfile{Approval: "ask"}); err != nil || got.Approval != "ask" {
		t.Fatalf("member override ignored: %+v %v", got, err)
	}
}

func TestPackagesNameCodesAndPatchByKey(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	if _, err := s.Setup("lead", "claude", t.TempDir(), "Team", "", nil); err != nil {
		t.Fatal(err)
	}
	title := func(t string) *Package { return &Package{Title: t} }
	if _, err := s.Configure("lead", ConfigurePatch{Packages: map[string]*Package{"CT1": title("Console input-flood test"), "G1": title("Clear review debt")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Configure("lead", ConfigurePatch{Packages: map[string]*Package{"G1": nil}}); err != nil {
		t.Fatal(err)
	}
	if got := s.View("lead").Packages; len(got) != 1 || got["CT1"].Title != "Console input-flood test" {
		t.Fatalf("packages = %+v", got)
	}
	for _, bad := range []map[string]*Package{{"has space": title("x")}, {"CT1": title("")}} {
		if _, err := s.Configure("lead", ConfigurePatch{Packages: bad}); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// Overlay is how a catalog slot takes a member's explicit settings.
func TestOverlay(t *testing.T) {
	base := AgentProfile{Provider: "claude", Connection: "claude@openrouter", Model: "sonnet", Capability: "reviewer", Configs: map[string]string{"a": "1"}}
	got, err := Overlay(base, AgentProfile{Model: "haiku", Configs: map[string]string{"b": "2"}})
	if err != nil || got.Model != "haiku" || got.Connection != "claude@openrouter" || got.Capability != "reviewer" || got.Configs["a"] != "1" || got.Configs["b"] != "2" {
		t.Fatal(got, err)
	}
	if base.Configs["b"] != "" {
		t.Fatal("Overlay changed its base")
	}
	if _, err := Overlay(base, AgentProfile{Provider: "codex"}); err == nil {
		t.Fatal("a provider override crossed the base's provider")
	}
	if _, err := Overlay(base, AgentProfile{Approval: "auto"}); err == nil {
		t.Fatal("an overlay broke the profile rules (reviewer with auto)")
	}
}
