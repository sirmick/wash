package agentproto

// Roster messages: the State tree, published three ways.

// Subscribe asks for the whole roster, now and on every change. Handled by
// the SDK's StateService.
type Subscribe struct{}

// Unsubscribe stops a Subscribe.
type Unsubscribe struct{}

// RosterState is the whole roster, sent by the StateService to every
// subscriber on each change.
type RosterState struct {
	State State `json:"state"`
}

// ManagerState is the Agents manager's view of the roster: every row with
// its transcript preview and workspace placement, without the per-session
// settings only a controller needs.
type ManagerState struct {
	State State `json:"state"`
}

// SessionState is one Agent window's view: its own row and the questions
// waiting on it.
type SessionState struct {
	Key   string `json:"key"`
	State State  `json:"state"`
}

func init() {
	register(Spec{Kind: "subscribe", Dir: Request, Payload: Subscribe{}, From: "any app (the session gateway, hostgw)", Reply: "state, now and on every change",
		Doc: "Subscribe to the whole roster."})
	register(Spec{Kind: "unsubscribe", Dir: Request, Payload: Unsubscribe{}, From: "a subscriber",
		Doc: "Stop receiving state."})
	register(Spec{Kind: "state", Dir: Push, Payload: RosterState{}, From: "every subscriber",
		Doc: "The whole roster. Sent by the SDK StateService, which owns this message's encoding."})
	register(Spec{Kind: "manager_state", Dir: Push, Payload: ManagerState{}, From: "every manager (manager_subscribe)",
		Doc: "The manager's roster view, sent on subscribe and whenever it changes."})
	register(Spec{Kind: "session_state", Dir: Push, Payload: SessionState{}, From: "a session's controller", Keyed: true,
		Doc: "One session's row and questions, sent on claim and whenever they change."})
}
