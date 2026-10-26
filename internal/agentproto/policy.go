package agentproto

// The approval policy: the rules agentd answers a hosted session's tool
// permission requests from before asking a human (agents.json `enabled`,
// `default`, `ask_desktop`, `rules`; internal/agentpolicy). The Agents
// window's Permissions tab shows it from State.Policy and writes it with
// AgentSetPolicy. It covers sessions wash hosts over ACP only; an agent run
// by hand in a terminal never asks wash anything.

// PolicyView is the policy as stored, with the defaults applied: Default
// is "ask" or "deny" (never "allow"), and an unset AskDesktop reads true. It
// only takes effect while Enabled.
type PolicyView struct {
	// Enabled is the kill switch: off, every request goes to a human.
	Enabled bool `json:"enabled"`
	// Default answers a request no rule matched: ask | deny.
	Default string `json:"default"`
	// AskDesktop brings an unmatched request to the desktop sidebar (with
	// "always allow") before the agent's own prompt.
	AskDesktop bool `json:"ask_desktop"`
	// Rules are evaluated in order; first match wins.
	Rules []PolicyRule `json:"rules,omitempty"`
}

// PolicyRule is one row: `Tool` or `Tool(glob)`, allow | deny | ask, and an
// optional directory it is scoped to.
type PolicyRule struct {
	Match    string `json:"match"`
	Decision string `json:"decision"`
	Cwd      string `json:"cwd,omitempty"`
}

// AgentSetPolicy replaces the policy, whole: those four fields of
// agents.json and nothing else in the file.
type AgentSetPolicy struct {
	Policy PolicyView `json:"policy"`
}

// PolicySaved answers AgentSetPolicy.
type PolicySaved struct {
	Error string `json:"error,omitempty"`
}

func init() {
	register(Spec{Kind: "agent_set_policy", Dir: Request, Payload: AgentSetPolicy{}, From: "a manager (manager_subscribe)", Reply: "policy_saved",
		Doc: "Store the approval policy (enabled, default, ask_desktop, rules) in agents.json, leaving the rest of the file alone."})
	register(Spec{Kind: "policy_saved", Dir: Push, Payload: PolicySaved{}, From: "the asker",
		Doc: "The outcome of storing the approval policy."})
}
