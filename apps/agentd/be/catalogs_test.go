package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
)

func catalogsPolicy(t *testing.T, catalogs map[string]string) agentpolicy.Policy {
	t.Helper()
	p := agentpolicy.Policy{Catalogs: map[string]json.RawMessage{}}
	for id, raw := range catalogs {
		p.Catalogs[id] = json.RawMessage(raw)
	}
	return p
}

// The shipped catalogs are complete and valid; a defect in catalogs.json
// would otherwise only show as a greyed row on someone's desktop.
func TestBuiltinCatalogsAreValid(t *testing.T) {
	catalogs, bad := loadCatalogs(agentpolicy.Policy{})
	if len(bad) > 0 {
		t.Fatalf("invalid built-in catalogs: %v", bad)
	}
	// One auto catalog per adapter, plus OpenRouter through OpenCode.
	for id, adapter := range map[string]string{"anthropic": "claude", "openai": "codex", "gemini": "gemini", "opencode": "opencode", "openrouter": "opencode"} {
		c, ok := catalogs[id]
		if !ok || !c.auto() || c.Adapter != adapter {
			t.Errorf("auto catalog %s = %+v", id, c)
		}
	}
	if catalogs["openrouter"].Connection != "opencode@openrouter" {
		t.Errorf("openrouter = %+v", catalogs["openrouter"])
	}
	// A pro and a budget set per vendor, three slots each.
	for _, id := range []string{"anthropic-pro", "anthropic-budget", "openai-pro", "openai-budget", "openrouter-pro", "openrouter-budget"} {
		c, ok := catalogs[id]
		if !ok || c.auto() || len(c.Slots) != len(slotNames) {
			t.Errorf("catalog %s = %+v", id, c)
		}
	}
	// Pro and budget differ where it costs: the top slots.
	if catalogs["anthropic-pro"].Slots["frontier"].Model != "claude-fable-5-1[1m]" || catalogs["anthropic-budget"].Slots["frontier"].Model != "opus[1m]" ||
		catalogs["openai-pro"].Slots["frontier"].Model != "gpt-6-astra" || catalogs["openai-budget"].Slots["frontier"].Model != "gpt-6-sol" {
		t.Errorf("pro/budget frontier models differ from the table")
	}
	// A slot is a model slot: no permission setting ships in any catalog.
	for id, c := range catalogs {
		for slot, p := range c.Slots {
			if p.Capability != "" || p.Subagents != "" || p.Approval != "" || len(p.Configs) > 0 {
				t.Errorf("%s %s carries permission settings: %+v", id, slot, p)
			}
		}
	}
	for _, id := range []string{"openrouter-pro", "openrouter-budget"} {
		for slot, p := range catalogs[id].Slots {
			if p.Provider != "opencode" || p.Connection != "opencode@openrouter" || !strings.HasPrefix(p.Model, "openrouter/") {
				t.Errorf("%s %s = %+v", id, slot, p)
			}
		}
	}
	// Claude Code's effort values move between releases (haiku offered none
	// on claude-agent-acp 0.81.2), so the Anthropic catalogs pin no effort
	// and take the adapter's default.
	for _, id := range []string{"anthropic-pro", "anthropic-budget"} {
		for slot, p := range catalogs[id].Slots {
			if p.Effort != "" {
				t.Errorf("%s %s sets effort %q", id, slot, p.Effort)
			}
		}
	}
}

// agents.json replaces a catalog whole, by id, or adds one; the built-in
// table is not changed by it.
func TestAgentsJSONReplacesACatalogWhole(t *testing.T) {
	pol := catalogsPolicy(t, map[string]string{"anthropic-pro": `{"name":"Mine","slots":{"frontier":{"provider":"claude","model":"opus[1m]","effort":"high"},"coding":{"provider":"claude","model":"sonnet"},"small":{"provider":"claude","model":"haiku"}}}`})
	catalogs, bad := loadCatalogs(pol)
	if len(bad) > 0 {
		t.Fatal(bad)
	}
	c := catalogs["anthropic-pro"]
	if c.Name != "Mine" || c.Slots["frontier"].Model != "opus[1m]" || c.Slots["small"].Model != "haiku" {
		t.Errorf("replaced = %+v", c)
	}
	if builtinLaunch.Catalogs["anthropic-pro"].Slots["frontier"].Model == "opus[1m]" {
		t.Error("an override leaked into the built-in catalogs")
	}
	// A partial catalog is invalid, not merged: agents.json says the whole
	// of what the Catalog tab showed.
	_, bad = loadCatalogs(catalogsPolicy(t, map[string]string{"anthropic-pro": `{"name":"Half","slots":{"frontier":{"provider":"claude"}}}`}))
	if bad["anthropic-pro"] == nil {
		t.Error("a partial override was accepted")
	}
}

// A catalog that cannot work is kept, with the reason, so the launcher can
// say why instead of the row disappearing.
func TestInvalidCatalogsAreKeptWithTheirReason(t *testing.T) {
	full := func(slot string) string {
		slots := []string{}
		for _, n := range slotNames {
			v := `{"provider":"claude"}`
			if n == "coding" {
				v = slot
			}
			slots = append(slots, `"`+n+`":`+v)
		}
		return `{"name":"X","slots":{` + strings.Join(slots, ",") + `}}`
	}
	cases := map[string]string{
		"missing slots":       `{"name":"X","slots":{"frontier":{"provider":"claude"}}}`,
		"no name":             `{"slots":{"frontier":{"provider":"claude"},"coding":{"provider":"claude"},"small":{"provider":"claude"}}}`,
		"review slot":         `{"name":"X","slots":{"frontier":{"provider":"claude"},"coding":{"provider":"claude"},"review":{"provider":"claude"},"small":{"provider":"claude"}}}`,
		"auto approval":       full(`{"provider":"claude","approval":"auto"}`),
		"unknown connection":  full(`{"provider":"claude","connection":"claude@nowhere"}`),
		"connection mismatch": full(`{"provider":"codex","connection":"opencode@openrouter"}`),
		"unknown adapter":     full(`{"provider":"nope"}`),
		"capability in slot":  full(`{"provider":"claude","capability":"reviewer"}`),
		"subagents in slot":   full(`{"provider":"claude","subagents":"deny"}`),
		"configs in slot":     full(`{"provider":"claude","configs":{"mode":"plan"}}`),
		"unknown field":       `{"name":"X","colour":"red"}`,
		"auto unknown":        `{"name":"X","adapter":"nope"}`,
		"auto bad connection": `{"name":"X","adapter":"claude","connection":"opencode@openrouter"}`,
		"auto with slots":     `{"name":"X","adapter":"claude","slots":{"frontier":{"provider":"claude"},"coding":{"provider":"claude"},"small":{"provider":"claude"}}}`,
		"nothing":             `{"name":"X"}`,
	}
	for name, raw := range cases {
		catalogs, bad := loadCatalogs(catalogsPolicy(t, map[string]string{"mine": raw}))
		if bad["mine"] == nil {
			t.Errorf("%s: accepted %+v", name, catalogs["mine"])
		}
		if len(bad) != 1 {
			t.Errorf("%s: one bad catalog broke others: %v", name, bad)
		}
	}
	catalogs, bad := loadCatalogs(catalogsPolicy(t, map[string]string{"mine": cases["auto approval"]}))
	if _, err := resolveCatalog(catalogs, bad, "mine", "frontier"); err == nil {
		t.Error("an invalid catalog's slot resolved")
	}
	if _, err := resolveCatalog(catalogs, bad, "nope", ""); err == nil {
		t.Error("an unknown catalog resolved")
	}
}

// One start path. A catalog alone starts its frontier slot (or, auto, the
// adapter's default); a slot by name; a model id on the catalog's adapter;
// Advanced's settings on top; and an agent alone (wash ai --agent) starts
// that adapter on its defaults.
func TestStartProfile(t *testing.T) {
	pol := agentpolicy.Policy{}
	p, l, err := startProfile(pol, agentproto.AgentStart{Catalog: "openrouter-budget"})
	if err != nil || p.Model != builtinLaunch.Catalogs["openrouter-budget"].Slots["frontier"].Model || l.connection != "opencode@openrouter" || l.catalog != "openrouter-budget" || l.model != "" {
		t.Fatalf("catalog alone: %+v %+v %v", p, l, err)
	}
	p, l, _ = startProfile(pol, agentproto.AgentStart{Catalog: "openrouter-budget", Model: "coding"})
	if p.Model != builtinLaunch.Catalogs["openrouter-budget"].Slots["coding"].Model || p.Effort != "high" || l.model != "coding" {
		t.Errorf("slot by name: %+v %+v", p, l)
	}
	// A model id on a curated catalog: the frontier slot's adapter and
	// connection, that model, the adapter's default effort.
	p, _, _ = startProfile(pol, agentproto.AgentStart{Catalog: "openrouter-budget", Model: "openrouter/z-ai/glm-5.3"})
	if p.Provider != "opencode" || p.Connection != "opencode@openrouter" || p.Model != "openrouter/z-ai/glm-5.3" || p.Effort != "" {
		t.Errorf("model id on a curated catalog: %+v", p)
	}
	p, l, _ = startProfile(pol, agentproto.AgentStart{Catalog: "anthropic", Model: "haiku", Configs: map[string]string{"effort": "max"}})
	if p.Provider != "claude" || p.Model != "haiku" || p.Configs["effort"] != "max" || l.connection != "" {
		t.Errorf("auto catalog: %+v %+v", p, l)
	}
	p, _, _ = startProfile(pol, agentproto.AgentStart{Catalog: "openrouter"})
	if p.Provider != "opencode" || p.Connection != "opencode@openrouter" || p.Model != "" {
		t.Errorf("auto catalog through a connection: %+v", p)
	}
	// A catalog carries no permissions: nothing it says can restrict or
	// auto-approve a launch.
	p, l, _ = startProfile(pol, agentproto.AgentStart{Catalog: "anthropic-pro", Model: "small"})
	if l.capability != "" || l.noSubagents || p.Approval != "" || p.Model != "sonnet" {
		t.Errorf("small slot: %+v %+v", p, l)
	}
	p, l, err = startProfile(pol, agentproto.AgentStart{Agent: "gemini"})
	if err != nil || p.Provider != "gemini" || l.catalog != "" || l.model != "" {
		t.Errorf("agent alone: %+v %+v %v", p, l, err)
	}
	if _, _, err := startProfile(pol, agentproto.AgentStart{}); err == nil {
		t.Error("an empty request resolved")
	}
	if _, _, err := startProfile(pol, agentproto.AgentStart{Catalog: "nope"}); err == nil {
		t.Error("an unknown catalog resolved")
	}
}

// The launcher's view: a catalog is greyed with the reason it cannot start;
// the Catalog tab's view: which are built in, which are overridden, and an
// invalid catalog still lists what it has.
func TestPublishCatalogsGreysWhatCannotStart(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"claude-agent-acp", "opencode"} {
		if err := os.WriteFile(filepath.Join(dir, bin), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	views := map[string]agentproto.CatalogView{}
	for _, v := range publishCatalogs(agentpolicy.Policy{}, nil) {
		views[v.ID] = v
	}
	if v := views["anthropic-pro"]; !v.Available || !v.Builtin || v.Overridden || len(v.Slots) != 3 || v.Slots[0].Slot != "frontier" || v.Slots[2].Slot != "small" {
		t.Errorf("anthropic-pro = %+v", v)
	}
	if v := views["anthropic"]; !v.Available || v.Adapter != "claude" || len(v.Slots) != 0 {
		t.Errorf("anthropic (auto) = %+v", v)
	}
	if v := views["openai"]; v.Available || !strings.Contains(v.Note, "codex-acp") {
		t.Errorf("openai without codex = %+v", v)
	}
	if v := views["openrouter"]; v.Available || v.Note != "no openrouter key set" {
		t.Errorf("openrouter without a key = %+v", v)
	}
	if v := views["openrouter-budget"]; v.Available || v.Note != "no openrouter key set" {
		t.Errorf("openrouter-budget without a key = %+v", v)
	}
	for _, v := range publishCatalogs(agentpolicy.Policy{}, map[string]string{"openrouter": "k"}) {
		if strings.HasPrefix(v.ID, "openrouter") && !v.Available {
			t.Errorf("%s with a key = %+v", v.ID, v)
		}
	}
	for _, v := range publishCatalogs(catalogsPolicy(t, map[string]string{"mine": `{"name":"Mine","slots":{"frontier":{"provider":"claude","model":"opus[1m]"}}}`, "anthropic-pro": `{"name":"Pro","slots":{"frontier":{"provider":"claude"},"coding":{"provider":"claude"},"small":{"provider":"claude","model":"sonnet"}}}`}), nil) {
		switch v.ID {
		case "mine":
			// Invalid (two slots missing), yet the one it has is listed, so
			// the tab can show what to fix.
			if v.Available || v.Builtin || v.Overridden || !strings.Contains(v.Note, "slot") || len(v.Slots) != 1 || v.Slots[0].Model != "opus[1m]" || v.Slots[0].Note != v.Note {
				t.Errorf("invalid user catalog = %+v", v)
			}
		case "anthropic-pro":
			if !v.Builtin || !v.Overridden || v.Name != "Pro" || v.Slots[2].Model != "sonnet" {
				t.Errorf("overridden built-in = %+v", v)
			}
		}
	}
}

// A new key or an edited agents.json reaches the launcher on the next sweep,
// and an unchanged box pushes nothing.
func TestRefreshLaunchersPushesOnlyChanges(t *testing.T) {
	withPolicy(t, agentpolicy.Policy{})
	keys := map[string]string{}
	old := keyStore
	keyStore = func() map[string]string { return keys }
	t.Cleanup(func() { keyStore = old })
	var s agentproto.State
	if !refreshLaunchers(&s) || len(s.Catalogs) != len(builtinLaunch.Catalogs) {
		t.Fatalf("first refresh: %+v", s.Catalogs)
	}
	if refreshLaunchers(&s) {
		t.Error("an unchanged refresh reported a change")
	}
	keys["openrouter"] = "k"
	if !refreshLaunchers(&s) {
		t.Error("a new key was not published")
	}
	for _, c := range s.Catalogs {
		if c.ID == "openrouter-budget" && !c.Available {
			t.Errorf("openrouter-budget after the key: %+v", c)
		}
	}
}

// A workspace member's slot and a launcher's slot are the same
// AgentProfile.
func TestSlotIsAnAgentProfile(t *testing.T) {
	catalogs, bad := loadCatalogs(agentpolicy.Policy{})
	p, err := resolveCatalog(catalogs, bad, "anthropic-pro", "coding")
	if err != nil {
		t.Fatal(err)
	}
	if err := swarm.ValidateProfile(p); err != nil {
		t.Fatal(err)
	}
}

// The Catalog tab writes a catalog whole into agents.json; the launcher sees
// it on the next read. A built-in's override is removed by delete, and an
// invalid catalog is refused rather than saved greyed.
func TestSetAndDeleteCatalogWriteAgentsJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	spec := func(model string) agentproto.CatalogSpec {
		return agentproto.CatalogSpec{Name: "Mine", Slots: map[string]agentproto.SlotSpec{
			"frontier": {Adapter: "claude", Model: model, Effort: "high"},
			"coding":   {Adapter: "claude", Model: "sonnet"},
			"small":    {Adapter: "claude", Model: "haiku"},
		}}
	}
	if err := setCatalog(hostedPolicy(), "mine", spec("opus[1m]")); err != nil {
		t.Fatal(err)
	}
	if err := setCatalog(hostedPolicy(), "anthropic-pro", spec("claude-fable-5-1[1m]")); err != nil {
		t.Fatal(err)
	}
	if err := setCatalog(hostedPolicy(), "work", agentproto.CatalogSpec{Name: "Work", Adapter: "claude", Connection: "claude@openrouter"}); err != nil {
		t.Fatal(err)
	}
	catalogs, bad := loadCatalogs(hostedPolicy())
	if len(bad) > 0 || catalogs["mine"].Slots["frontier"].Model != "opus[1m]" || catalogs["mine"].Slots["frontier"].Effort != "high" || catalogs["anthropic-pro"].Name != "Mine" || !catalogs["work"].auto() || catalogs["work"].Connection != "claude@openrouter" {
		t.Fatalf("after set: %+v %v", catalogs, bad)
	}
	views := map[string]agentproto.CatalogView{}
	for _, v := range publishCatalogs(hostedPolicy(), nil) {
		views[v.ID] = v
	}
	if !views["anthropic-pro"].Overridden || views["mine"].Overridden || views["mine"].Builtin || views["work"].Adapter != "claude" {
		t.Fatalf("views: %+v %+v %+v", views["anthropic-pro"], views["mine"], views["work"])
	}
	for name, c := range map[string]agentproto.CatalogSpec{
		"two slots":   {Name: "X", Slots: map[string]agentproto.SlotSpec{"frontier": {Adapter: "claude"}, "coding": {Adapter: "claude"}}},
		"bad adapter": {Name: "X", Slots: map[string]agentproto.SlotSpec{"frontier": {Adapter: "nope"}, "coding": {Adapter: "claude"}, "small": {Adapter: "claude"}}},
		"no name":     {Slots: spec("x").Slots},
		"nothing":     {Name: "X"},
	} {
		if err := setCatalog(hostedPolicy(), "bad", c); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
	if err := setCatalog(hostedPolicy(), "bad id!", spec("x")); err == nil {
		t.Error("an invalid id was saved")
	}
	if _, ok := hostedPolicy().Catalogs["bad"]; ok {
		t.Fatal("an invalid catalog reached agents.json")
	}
	if err := deleteCatalog("anthropic-pro"); err != nil {
		t.Fatal(err)
	}
	if err := deleteCatalog("anthropic-pro"); err == nil {
		t.Fatal("deleting an absent override succeeded")
	}
	catalogs, _ = loadCatalogs(hostedPolicy())
	if catalogs["anthropic-pro"].Name != builtinLaunch.Catalogs["anthropic-pro"].Name {
		t.Fatalf("delete did not revert the built-in: %+v", catalogs["anthropic-pro"])
	}
	if err := deleteCatalog("mine"); err != nil {
		t.Fatal(err)
	}
	catalogs, _ = loadCatalogs(hostedPolicy())
	if _, ok := catalogs["mine"]; ok {
		t.Fatal("the user's own catalog survived delete")
	}
}

// The launcher's remembered permission default round-trips through
// agents.json and the roster; an unknown adapter or mode is refused.
func TestSetLaunchPrefs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := setLaunch(agentproto.LaunchPrefs{Mode: map[string]string{"claude": "acceptEdits"}, Yolo: true}); err != nil {
		t.Fatal(err)
	}
	if got := publishLaunch(hostedPolicy()); got.Mode["claude"] != "acceptEdits" || !got.Yolo {
		t.Fatalf("published: %+v", got)
	}
	if err := setLaunch(agentproto.LaunchPrefs{Mode: map[string]string{"nope": "x"}}); err == nil {
		t.Fatal("unknown adapter accepted")
	}
	if err := setLaunch(agentproto.LaunchPrefs{}); err != nil {
		t.Fatal(err)
	}
	if hostedPolicy().Launch != nil {
		t.Fatal("an empty default was kept in the file")
	}
}

// A stored default catalog is what a start that names neither a catalog nor
// an agent runs — the thing that makes AgentStart{Cwd} alone valid, which is
// what the Editor's "new agent here", the Places agent icon and
// `wash ai <dir>` all send (docs/PLACES.md §4.5).
func TestStartProfileFallsBackToTheDefaultCatalog(t *testing.T) {
	pol := agentpolicy.Policy{Launch: &agentpolicy.LaunchPrefs{Catalog: "openrouter-budget", Model: "coding"}}
	p, l, err := startProfile(pol, agentproto.AgentStart{})
	if err != nil {
		t.Fatalf("empty request with a default set: %v", err)
	}
	if p.Model != builtinLaunch.Catalogs["openrouter-budget"].Slots["coding"].Model {
		t.Errorf("default model not applied: %+v", p)
	}
	// The RESOLVED catalog/model is what history records, so Restart replays
	// what actually ran rather than re-resolving a default that may have
	// changed since.
	if l.catalog != "openrouter-budget" || l.model != "coding" {
		t.Errorf("launch record should carry the resolved default: %+v", l)
	}
	// An explicit choice still wins over the default.
	if _, l, _ = startProfile(pol, agentproto.AgentStart{Catalog: "anthropic", Model: "haiku"}); l.catalog != "anthropic" || l.model != "haiku" {
		t.Errorf("explicit catalog lost to the default: %+v", l)
	}
	// So does an explicit adapter: --agent means the adapter's own defaults.
	if p, _, _ = startProfile(pol, agentproto.AgentStart{Agent: "gemini"}); p.Provider != "gemini" || p.Model != "" {
		t.Errorf("explicit agent lost to the default: %+v", p)
	}
	// With no default set the caller is still told to choose.
	if _, _, err := startProfile(agentpolicy.Policy{}, agentproto.AgentStart{}); err == nil {
		t.Error("an empty request resolved with no default set")
	}
}

// Regression: setLaunch nils the whole Launch block when it looks empty, and
// that check used to consider only the permission fields — so a default
// catalog with no mode and no yolo was dropped on save, silently.
func TestSetLaunchKeepsACatalogOnlyDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := setLaunch(agentproto.LaunchPrefs{Catalog: "anthropic", Model: "haiku"}); err != nil {
		t.Fatal(err)
	}
	pol := hostedPolicy()
	if pol.Launch == nil {
		t.Fatal("a catalog-only default was discarded on save")
	}
	if pol.Launch.Catalog != "anthropic" || pol.Launch.Model != "haiku" {
		t.Fatalf("stored: %+v", pol.Launch)
	}
	if got := publishLaunch(pol); got.Catalog != "anthropic" || got.Model != "haiku" {
		t.Fatalf("published: %+v", got)
	}
	// A default nobody can run is refused rather than saved for every later
	// launch to fail on.
	if err := setLaunch(agentproto.LaunchPrefs{Catalog: "nope"}); err == nil {
		t.Error("unknown catalog accepted as a default")
	}
	// A model without a catalog has nothing to resolve against.
	if err := setLaunch(agentproto.LaunchPrefs{Model: "haiku"}); err == nil {
		t.Error("a model with no catalog accepted")
	}
	// Clearing still works.
	if err := setLaunch(agentproto.LaunchPrefs{}); err != nil {
		t.Fatal(err)
	}
	if hostedPolicy().Launch != nil {
		t.Fatal("an empty default was kept in the file")
	}
}

// Deleting the catalog the default names clears the default, so later
// launch-pref saves (which send the whole block) and default-resolved starts
// do not fail on a catalog that no longer exists. A built-in only reverts,
// so the default naming it survives.
func TestDeletingTheDefaultCatalogClearsTheDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	spec := agentproto.CatalogSpec{Name: "Mine", Slots: map[string]agentproto.SlotSpec{
		"frontier": {Adapter: "claude", Model: "opus[1m]"},
		"coding":   {Adapter: "claude", Model: "sonnet"},
		"small":    {Adapter: "claude", Model: "haiku"},
	}}
	if err := setCatalog(hostedPolicy(), "mine", spec); err != nil {
		t.Fatal(err)
	}
	if err := setLaunch(agentproto.LaunchPrefs{Catalog: "mine", Model: "coding"}); err != nil {
		t.Fatal(err)
	}
	if err := deleteCatalog("mine"); err != nil {
		t.Fatal(err)
	}
	if l := hostedPolicy().Launch; l != nil && (l.Catalog != "" || l.Model != "") {
		t.Fatalf("the default still names a deleted catalog: %+v", l)
	}
	if err := setLaunch(agentproto.LaunchPrefs{Mode: map[string]string{"claude": "plan"}}); err != nil {
		t.Fatalf("a mode save after deleting the default failed: %v", err)
	}

	if err := setCatalog(hostedPolicy(), "anthropic-pro", spec); err != nil {
		t.Fatal(err)
	}
	if err := setLaunch(agentproto.LaunchPrefs{Catalog: "anthropic-pro", Model: "coding"}); err != nil {
		t.Fatal(err)
	}
	if err := deleteCatalog("anthropic-pro"); err != nil {
		t.Fatal(err)
	}
	if l := hostedPolicy().Launch; l == nil || l.Catalog != "anthropic-pro" {
		t.Fatalf("reverting a built-in cleared the default: %+v", l)
	}
}

// A start that names nothing runs the default only if it can start here;
// otherwise what the launcher would preselect (default-catalog.ts): the
// catalog used last, else the first that can start.
func TestPickStartCatalog(t *testing.T) {
	views := []agentproto.CatalogView{
		{ID: "anthropic", Available: false, Note: "no ANTHROPIC_API_KEY key set"},
		{ID: "gemini", Available: true},
		{ID: "openai", Available: true},
	}
	recent := []agentproto.Session{{Catalog: "anthropic"}, {Catalog: "openai"}, {Catalog: "gemini"}}
	for _, tc := range []struct {
		name   string
		recent []agentproto.Session
		pref   string
		want   string
	}{
		{"usable default wins over history", recent, "gemini", "gemini"},
		{"unusable default falls to history, skipping unusable entries", recent, "anthropic", "openai"},
		{"no history: the first that can start", nil, "anthropic", "gemini"},
		{"no default: history", recent, "", "openai"},
		{"a deleted default is no different", nil, "gone", "gemini"},
	} {
		if got := pickStartCatalog(views, tc.recent, tc.pref); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	none := []agentproto.CatalogView{{ID: "anthropic", Note: "no ANTHROPIC_API_KEY key set"}}
	if got := pickStartCatalog(none, recent, "anthropic"); got != "" {
		t.Fatalf("nothing can start, got %q", got)
	}
	if err := noCatalogError(none, "anthropic"); !strings.Contains(err.Error(), "no ANTHROPIC_API_KEY key set") {
		t.Errorf("the error should name the default's reason: %v", err)
	}
}
