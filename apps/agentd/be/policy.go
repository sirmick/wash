package agentd

// The approval policy as the Agents window edits it (agentproto/policy.go).
// agentd is the file's only writer: the Permissions tab, "always allow" in
// the sidebar, and the catalog/launch saves all go through agentpolicy.Update
// or Append here. Each changes only its own fields, so none of them reverts
// another's.

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// maxPolicyRules bounds a save; a table this long is a mistake, not a policy.
const maxPolicyRules = 500

// publishPolicy is the policy with its defaults applied, as the tab shows it.
func publishPolicy(pol agentpolicy.Policy) agentproto.PolicyView {
	v := agentproto.PolicyView{
		Enabled:    pol.Enabled,
		Default:    agentpolicy.DecisionAsk,
		// The stored choice (unset means yes), not the effective one: the
		// tab shows it greyed while the policy is off, so turning the policy
		// on doesn't appear to flip it.
		AskDesktop: pol.AskDesktop == nil || *pol.AskDesktop,
	}
	if pol.Default == agentpolicy.DecisionDeny {
		v.Default = agentpolicy.DecisionDeny
	}
	for _, r := range pol.Rules {
		v.Rules = append(v.Rules, agentproto.PolicyRule{Match: r.Match, Decision: r.Decision, Cwd: r.Cwd})
	}
	return v
}

// validatePolicy refuses what the file must never say: an "allow" default
// (indistinguishable from no permission system), an unknown decision, an
// empty or multi-line match, a relative directory.
func validatePolicy(v agentproto.PolicyView) error {
	switch v.Default {
	case agentpolicy.DecisionAsk, agentpolicy.DecisionDeny:
	default:
		return fmt.Errorf("the default must be ask or deny, not %q", v.Default)
	}
	if len(v.Rules) > maxPolicyRules {
		return fmt.Errorf("at most %d rules", maxPolicyRules)
	}
	for i, r := range v.Rules {
		n := i + 1
		switch r.Decision {
		case agentpolicy.DecisionAllow, agentpolicy.DecisionDeny, agentpolicy.DecisionAsk:
		default:
			return fmt.Errorf("rule %d: decision must be allow, deny or ask", n)
		}
		m := strings.TrimSpace(r.Match)
		if m == "" {
			return fmt.Errorf("rule %d: no tool to match", n)
		}
		if len(m) > 1000 || strings.ContainsAny(m, "\r\n") {
			return fmt.Errorf("rule %d: the match is one line", n)
		}
		if r.Cwd != "" && !filepath.IsAbs(r.Cwd) {
			return fmt.Errorf("rule %d: the directory must be an absolute path", n)
		}
	}
	return nil
}

// setPolicy stores the four policy fields and leaves the rest of agents.json
// (catalogs, connections, launch, agents, MCP servers) as it is on disk.
func setPolicy(v agentproto.PolicyView) error {
	if err := validatePolicy(v); err != nil {
		return err
	}
	return agentpolicy.Update(agentpolicy.Path(), func(p *agentpolicy.Policy) error {
		p.Enabled = v.Enabled
		p.Default = v.Default
		ask := v.AskDesktop
		p.AskDesktop = &ask
		p.Rules = nil
		for _, r := range v.Rules {
			p.Rules = append(p.Rules, agentpolicy.Rule{Match: strings.TrimSpace(r.Match), Decision: r.Decision, Cwd: r.Cwd})
		}
		return nil
	})
}

func registerPolicyHandlers(bus *sdk.Bus) {
	sdk.HandleFromVoid(bus, "agent_set_policy", func(conn *sdk.Conn, _ string, req agentproto.AgentSetPolicy, from wire.Sender) error {
		if !isManager(from.InstanceID) {
			return nil
		}
		var reply agentproto.PolicySaved
		if err := setPolicy(req.Policy); err != nil {
			reply.Error = err.Error()
			log.Printf("agentd: policy not saved: %v", err)
		} else {
			log.Printf("agentd: policy saved enabled=%v default=%s rules=%d", req.Policy.Enabled, req.Policy.Default, len(req.Policy.Rules))
			mutateState(func(s *agentproto.State) { refreshLaunchers(s) })
		}
		return agentproto.Send(conn, wire.Recipient{InstanceID: from.InstanceID}, reply)
	})
}
