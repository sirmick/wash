package agentproto

// Roles. The router attests only an app's id and instance, so agentd does
// not decide what a frontend may do by which app it is. It decides by what
// the frontend has claimed:
//
//   - a manager has sent manager_subscribe: it gets the manager's roster
//     view, and may set and test connection keys;
//   - a session's controller holds its lease (session_claim, or AgentStart
//     with Claim): it is the one window focus is sent to, the one that
//     closes when the session detaches, and the only one whose workspace
//     sidebar requests are served.
//
// A lease is exclusive: a second claim is refused (claim_denied) and the
// holder is raised instead. Everything else — prompting, answering,
// stopping — is open to any frontend, as it always was: those are the
// person's actions, and any window showing the session may take them.

// ManagerSubscribe registers the sender as a manager and asks for the
// manager's roster view, now and on every change.
type ManagerSubscribe struct{}

// SessionClaim takes (or re-affirms) the controller lease on a session.
type SessionClaim struct {
	Key string `json:"key"`
}

// SessionClaimed grants the lease; a session_state follows.
type SessionClaimed struct {
	Key string `json:"key"`
}

// ClaimDenied refuses the lease: another instance holds it, and has been
// raised. A window denied its session closes.
type ClaimDenied struct {
	Key string `json:"key"`
}

// Focus asks for a session's window to come forward, opening one if
// nothing is showing it. The shell sends it when a notification about the
// session is clicked; frontends send it to go to a running session.
type Focus struct {
	Key string `json:"key"`
}

// Raise tells a session's controller to come forward.
type Raise struct {
	Key string `json:"key"`
}

// Attach hands a window agentd opened the session it is to show; the lease
// is already its.
type Attach struct {
	Key string `json:"key"`
}

func init() {
	register(Spec{Kind: "manager_subscribe", Dir: Request, Payload: ManagerSubscribe{}, From: "any frontend, which becomes a manager", Reply: "manager_state, now and on every change",
		Doc: "Subscribe to the manager's roster view."})
	register(Spec{Kind: "session_claim", Dir: Request, Payload: SessionClaim{}, From: "any frontend", Reply: "session_claimed then session_state, or claim_denied",
		Doc: "Take the controller lease on a session."})
	register(Spec{Kind: "wash.focus", Dir: Request, Payload: Focus{}, From: "the shell (a notification click, no sender) or any frontend",
		Doc: "Bring a session's window forward, opening one if needed."})

	register(Spec{Kind: "session_claimed", Dir: Push, Payload: SessionClaimed{}, From: "the claimant", Keyed: true,
		Doc: "The lease is yours."})
	register(Spec{Kind: "claim_denied", Dir: Push, Payload: ClaimDenied{}, From: "the claimant", Keyed: true,
		Doc: "Another window holds the lease."})
	register(Spec{Kind: "wash.focus", Dir: Push, Payload: Raise{}, From: "the session's controller", Keyed: true,
		Doc: "Come to the front."})
	register(Spec{Kind: "attach", Dir: Push, Payload: Attach{}, From: "a window agentd opened for a session", Keyed: true,
		Doc: "The session this new window shows."})
}
