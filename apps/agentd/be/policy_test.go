package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/agentproto"
)

func TestSetPolicyKeepsTheRestOfAgentsJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := agentpolicy.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// What agentd stores beside the policy, plus an "always allow" rule
	// added since the tab last loaded.
	seed := `{"enabled":true,"rules":[{"match":"Read","decision":"allow"}],
	  "launch":{"catalog":"mine","mode":{"claude":"default"}},
	  "catalogs":{"mine":{"name":"Mine","adapter":"claude"}},
	  "connections":{"proxy":{"adapter":"claude"}}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	err := setPolicy(agentproto.PolicyView{
		Enabled:    true,
		Default:    "deny",
		AskDesktop: false,
		Rules:      []agentproto.PolicyRule{{Match: " Bash(git status:*) ", Decision: "allow", Cwd: "/src"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"launch", "catalogs", "connections"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%s was dropped: %s", k, data)
		}
	}
	p := agentpolicy.Load(path)
	if p.Default != "deny" || p.AskDesktopOrDefault() || len(p.Rules) != 1 || p.Rules[0].Match != "Bash(git status:*)" || p.Rules[0].Cwd != "/src" {
		t.Errorf("policy = %+v", p)
	}
	v := publishPolicy(p)
	if !v.Enabled || v.Default != "deny" || v.AskDesktop || len(v.Rules) != 1 {
		t.Errorf("published = %+v", v)
	}
}

func TestSetPolicyRefusesWhatTheFileMustNeverSay(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cases := map[string]agentproto.PolicyView{
		"default must be ask or deny": {Default: "allow"},
		"decision must be":            {Default: "ask", Rules: []agentproto.PolicyRule{{Match: "Read", Decision: "yes"}}},
		"no tool to match":            {Default: "ask", Rules: []agentproto.PolicyRule{{Match: "  ", Decision: "allow"}}},
		"one line":                    {Default: "ask", Rules: []agentproto.PolicyRule{{Match: "Bash(a\nb)", Decision: "allow"}}},
		"must be an absolute path":    {Default: "ask", Rules: []agentproto.PolicyRule{{Match: "Read", Decision: "allow", Cwd: "src"}}},
	}
	for want, v := range cases {
		err := setPolicy(v)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v", want, err)
		}
	}
	if _, err := os.Stat(agentpolicy.Path()); !os.IsNotExist(err) {
		t.Errorf("a refused save wrote the file: %v", err)
	}
}

func TestPublishPolicyDefaults(t *testing.T) {
	v := publishPolicy(agentpolicy.Policy{Default: "allow"})
	if v.Enabled || v.Default != "ask" || !v.AskDesktop {
		t.Errorf("zero policy published as %+v; want off, ask, ask_desktop", v)
	}
}
