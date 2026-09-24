package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/internal/workspacemcp"
)

func profileOptions(model, thinking string) []acp.ConfigOption {
	levels := []acp.ConfigOptionValue{{Value: "low"}}
	if model == "smart" {
		levels = append(levels, acp.ConfigOptionValue{Value: "high"})
	}
	return []acp.ConfigOption{
		{ID: "provider_model", Category: "model", CurrentValue: model, Options: []acp.ConfigOptionValue{{Value: "fast"}, {Value: "smart"}}},
		{ID: "effort", Category: "thought_level", CurrentValue: thinking, Options: levels},
	}
}
func TestProfileAppliesModelBeforeThinkingAndVerifiesSettings(t *testing.T) {
	for _, raw := range []bool{false, true} {
		settings := swarm.AgentProfile{Model: "smart", Thinking: "high"}
		if raw {
			settings = swarm.AgentProfile{Configs: map[string]string{"effort": "high", "provider_model": "smart"}}
		}
		order := []string{}
		model, thinking := "fast", "low"
		effective, err := configureWorkspaceSession(settings, profileOptions(model, thinking), func(id, value string) ([]acp.ConfigOption, error) {
			order = append(order, id)
			if id == "provider_model" {
				model = value
				thinking = "low"
			} else {
				if model != "smart" {
					t.Fatal("thinking before model")
				}
				thinking = value
			}
			return profileOptions(model, thinking), nil
		})
		if err != nil || !reflect.DeepEqual(order, []string{"provider_model", "effort"}) || effective["provider_model"] != "smart" || effective["effort"] != "high" {
			t.Fatal(order, effective, err)
		}
	}
}
func TestProfileRejectsUnsupportedConflictingAndCoercedSettings(t *testing.T) {
	for _, settings := range []swarm.AgentProfile{
		{Model: "unknown"}, {Thinking: "high"}, {Configs: map[string]string{"unknown": "value"}},
		{Model: "smart", Configs: map[string]string{"provider_model": "fast"}},
	} {
		if _, err := configureWorkspaceSession(settings, profileOptions("fast", "low"), func(string, string) ([]acp.ConfigOption, error) {
			t.Fatal("invalid option reached adapter")
			return nil, nil
		}); err == nil {
			t.Fatal("accepted", settings)
		}
	}
	for _, fail := range []bool{false, true} {
		_, err := configureWorkspaceSession(swarm.AgentProfile{Model: "smart"}, profileOptions("fast", "low"), func(string, string) ([]acp.ConfigOption, error) {
			if fail {
				return nil, errors.New("provider error")
			}
			return profileOptions("fast", "low"), nil
		})
		if err == nil {
			t.Fatal("ignored provider failure/coercion")
		}
	}
	if _, err := configureWorkspaceSession(swarm.AgentProfile{Thinking: "high"}, nil, func(string, string) ([]acp.ConfigOption, error) { return nil, nil }); err == nil {
		t.Fatal("missing thinking accepted")
	}
}
func TestWorkspaceGetAndConfigure(t *testing.T) {
	s, _ := swarm.Open(filepath.Join(t.TempDir(), "workspace.json"))
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "workspace-config-lead", key: "workspace-config-test", agent: "codex", configs: profileOptions("fast", "low")}
	h.sessionReady.Store(true)
	hostedMu.Lock()
	hostedAll[h.key] = h
	hostedMu.Unlock()
	defer func() { hostedMu.Lock(); delete(hostedAll, h.key); hostedMu.Unlock() }()
	call := func(name, raw string) (any, error) {
		return ws.call(context.Background(), h, workspacemcp.Call{Name: name, Arguments: json.RawMessage(raw)})
	}
	if got, err := call("workspace_get", `{}`); err != nil || got != nil {
		t.Fatal(got, err)
	}
	w, err := s.Setup(h.sessionID, h.agent, t.TempDir(), "Team", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = call("workspace_configure", `{"profiles":{"god":{"provider":"codex","model":"smart","thinking":"high"}},"expected_revision":1}`); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"profiles":{"bad":{"provider":"not-a-provider"}}}`, `{"profiles":{"bad":{"provider":"codex","secret":"ignored"}}}`, `{"max_active":null}`, `{"profiles":null}`} {
		before := s.Snapshot()
		if _, err := call("workspace_configure", args); err == nil {
			t.Fatal("invalid config", args)
		}
		if !reflect.DeepEqual(before, s.Snapshot()) {
			t.Fatal("partial invalid mutation")
		}
	}
	if _, err = s.Send(h.sessionID, w.Lead, "progress", "retained body", "", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, include := range []bool{false, true} {
		args, _ := json.Marshal(map[string]bool{"include_messages": include})
		got, err := call("workspace_get", string(args))
		if err != nil {
			t.Fatal(err)
		}
		state := got.(map[string]any)
		view := state["workspace"].(*swarm.Workspace)
		if (len(view.Messages) > 0) != include || state["message_history_included"] != include || view.Profiles["god"].Model != "smart" {
			t.Fatal(state)
		}
		sessions := state["sessions"].(map[string]any)
		options := sessions[w.Lead].(map[string]any)["config_options"].([]acp.ConfigOption)
		if len(options) != 2 || options[1].Category != "thought_level" {
			t.Fatal(options)
		}
		options[0].Options[0].Value = "mutated"
		if h.configs[0].Options[0].Value != "fast" {
			t.Fatal("live settings leaked mutable references")
		}
	}
}

func TestWorkspaceHistoryPagesBoundBytesAndPreserveCursor(t *testing.T) {
	messages := []swarm.Message{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		messages = append(messages, swarm.Message{ID: id, Body: strings.Repeat("x", 32768)})
	}
	page, cursor, more, err := workspaceHistoryPage(messages, "", 100)
	if err != nil || len(page) != 7 || cursor != "g" || !more {
		t.Fatal(len(page), cursor, more, err)
	}
	page, cursor, more, err = workspaceHistoryPage(messages, cursor, 100)
	if err != nil || len(page) != 3 || cursor != "j" || more || page[0].ID != "h" {
		t.Fatal(len(page), cursor, more, err)
	}
	if _, _, _, err := workspaceHistoryPage(messages, "missing", 10); err == nil {
		t.Fatal("invalid cursor")
	}
	if _, _, _, err := workspaceHistoryPage(messages, "", 101); err == nil {
		t.Fatal("invalid limit")
	}
}

func TestWorkspaceAboutBeforeSetupAndWithoutMutation(t *testing.T) {
	s, err := swarm.Open(filepath.Join(t.TempDir(), "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	ws := &workspaceService{store: s}
	h := &hosted{sessionID: "about-lead", agent: "codex", mode: "read-only", configs: profileOptions("fast", "low")}
	call := func(raw string) (any, error) {
		return ws.call(context.Background(), h, workspacemcp.Call{Name: "workspace_get", Arguments: json.RawMessage(raw)})
	}
	for _, attached := range []bool{false, true} {
		if attached {
			if _, err = s.Setup(h.sessionID, h.agent, t.TempDir(), "Team", "", nil); err != nil {
				t.Fatal(err)
			}
		}
		before := s.Snapshot()
		got, err := call(`{"view":"about"}`)
		if err != nil {
			t.Fatal(err)
		}
		about := got.(map[string]any)
		wantRole := "unattached"
		if attached {
			wantRole = "orchestrator"
		}
		if about["caller"].(map[string]any)["role"] != wantRole {
			t.Fatal(about)
		}
		if about["instructions"] != workspacemcp.Instructions {
			t.Fatal("discovery guidance drift")
		}
		permissions := about["permissions"].(map[string]any)
		if permissions["adapter_mode"] != "read-only" || !strings.HasPrefix(permissions["filesystem_enforcement"].(string), "unknown:") {
			t.Fatal(permissions)
		}
		permissions["adapter_settings"].(map[string]string)["provider_model"] = "changed"
		if h.configs[0].CurrentValue != "fast" {
			t.Fatal("leaked mutable settings")
		}
		for _, raw := range []string{`{"view":"unknown"}`, `{"view":"about","include_messages":true}`, `{"view":"about","after":"x"}`, `{"view":"about","limit":1}`} {
			if _, err := call(raw); err == nil {
				t.Fatalf("accepted invalid view: %s", raw)
			}
		}
		if !reflect.DeepEqual(before, s.Snapshot()) {
			t.Fatal("about mutated workspace")
		}
		state, err := call(`{"view":"state"}`)
		if err != nil || (state != nil) != attached {
			t.Fatal(state, err)
		}
	}
}

// Observed on resume: the loaded session offered model "default, opus" for a
// member launched on claude-fable-5-1[1m] (still on 1M context), and the strict
// launch path gave up on the model before ever restoring effort "high".
func TestRestoreSkipsWhatTheLoadedSessionDoesNotOfferAndAppliesTheRest(t *testing.T) {
	values := func(vs ...string) []acp.ConfigOptionValue {
		out := []acp.ConfigOptionValue{}
		for _, v := range vs {
			out = append(out, acp.ConfigOptionValue{Value: v})
		}
		return out
	}
	options := []acp.ConfigOption{
		{ID: "model", Category: "model", CurrentValue: "default", Options: values("default", "opus")},
		{ID: "effort", Category: "thought_level", CurrentValue: "default", Options: values("default", "low", "medium", "high")},
	}
	var set []string
	skipped, err := restoreWorkspaceSession(swarm.AgentProfile{Provider: "claude", Model: "claude-fable-5-1[1m]", Thinking: "high"}, options, func(id, value string) ([]acp.ConfigOption, error) {
		set = append(set, id+"="+value)
		next := append([]acp.ConfigOption(nil), options...)
		for i := range next {
			if next[i].ID == id {
				next[i].CurrentValue = value
			}
		}
		return next, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(skipped, []string{"model=claude-fable-5-1[1m]"}) || !reflect.DeepEqual(set, []string{"effort=high"}) {
		t.Fatalf("skipped=%v set=%v", skipped, set)
	}
}
