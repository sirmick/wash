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
func TestConfigureProfilesAtomicPersistenceAndAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Setup("lead", "codex", t.TempDir(), "Team", "", nil); err != nil {
		t.Fatal(err)
	}
	revision, err := s.Configure("lead", configPatch(t, `{"profiles":{"god":{"provider":"codex","model":"smart","thinking":"high"},"pleb":{"provider":"codex","model":"fast"}},"default_profile":"pleb","expected_revision":1}`))
	if err != nil || revision != 2 {
		t.Fatal(revision, err)
	}
	for _, raw := range []string{
		`{"name":"changed","profiles":{"god":{"provider":""}}}`,
		`{"profiles":{"pleb":null}}`,
		`{"profiles":{"bad name":{"provider":"codex"}}}`,
		`{"default_profile":"missing"}`,
		`{"max_active":0}`, `{"max_active":17}`, `{"max_members":0}`,
		`{"expected_revision":1,"name":"stale"}`,
		`{"profiles":{"god":{"provider":"codex","configs":{"model":""}}}}`,
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
	if _, err := s.Configure("lead", configPatch(t, `{"profiles":{"god":{"provider":"claude","model":"other"}}}`)); err != nil {
		t.Fatal(err)
	}
	w := s.View("lead")
	if len(w.Profiles) != 2 || w.Profiles["pleb"].Model != "fast" || w.Profiles["god"].Thinking != "" {
		t.Fatal("profile merge/replacement semantics", w)
	}
	recovered, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := recovered.View("lead"); !reflect.DeepEqual(got.Profiles, w.Profiles) || got.DefaultProfile != "pleb" {
		t.Fatal("configuration not persisted", got)
	}
	if _, err := s.Configure("lead", configPatch(t, `{"default_profile":"","profiles":{"pleb":null}}`)); err != nil {
		t.Fatal(err)
	}
	if len(s.View("lead").Profiles) != 1 {
		t.Fatal("profile deletion failed")
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
func TestResolveProfileSnapshotAndOverrides(t *testing.T) {
	w := &Workspace{DefaultProfile: "god", Profiles: map[string]AgentProfile{"god": {Provider: "codex", Model: "smart", Thinking: "high", Configs: map[string]string{"speed": "slow"}}}}
	name, settings, err := ResolveProfile(w, "", AgentProfile{Thinking: "low", Configs: map[string]string{"speed": "fast"}}, "claude")
	if err != nil || name != "god" || settings.Provider != "codex" || settings.Model != "smart" || settings.Thinking != "low" || settings.Configs["speed"] != "fast" {
		t.Fatal(name, settings, err)
	}
	settings.Configs["speed"] = "changed"
	if w.Profiles["god"].Configs["speed"] != "slow" {
		t.Fatal("mutable profile alias")
	}
	for _, tc := range []struct {
		name     string
		explicit AgentProfile
	}{{"missing", AgentProfile{}}, {"god", AgentProfile{Provider: "claude"}}} {
		if _, _, err := ResolveProfile(w, tc.name, tc.explicit, "codex"); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	w.DefaultProfile = ""
	if _, settings, err := ResolveProfile(w, "", AgentProfile{}, "claude"); err != nil || settings.Provider != "claude" {
		t.Fatal(settings, err)
	}
}

func TestSuccessfulConversationTurnPreservesConfigurationRevision(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "workspace.json"))
	w, err := s.Setup("lead", "codex", t.TempDir(), "Team", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TurnEnded("lead", "", false); err != nil {
		t.Fatal(err)
	}
	if s.View("lead").Revision != w.Revision {
		t.Fatal("read-only conversation turn invalidated state revision")
	}
	if _, err = s.Configure("lead", ConfigurePatch{Expected: &w.Revision}); err != nil {
		t.Fatal(err)
	}
}
