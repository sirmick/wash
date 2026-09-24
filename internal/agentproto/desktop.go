package agentproto

// Desktop events: what agentd asks of the desktop a person is using, as
// opposed to what it tells a frontend. Every one goes through a single
// handler in agentd (apps/agentd/be/desktop.go); Wash's implementation of it
// spawns Agent windows and posts notifications through the router, which is
// what shows a question even when no Agent window is open. They are typed
// and listed here so that handler is the one place that knows how a desktop
// does these things.

// OpenSession opens a window showing a session. agentd has already
// reserved the session's lease for it; the window is handed the session
// with an attach push.
type OpenSession struct {
	Key string `json:"key"`
}

// Notify brings something to the person's attention. With a Key, activating
// the notification sends wash.focus for that session.
type Notify struct {
	Key   string `json:"key,omitempty"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// Level is info | warn | error.
	Level string `json:"level"`
}

func init() {
	register(Spec{Kind: "open_session", Dir: DesktopDir, Payload: OpenSession{}, From: "the desktop",
		Doc: "Open a window on a session; it is attached once it starts."})
	register(Spec{Kind: "notify", Dir: DesktopDir, Payload: Notify{}, From: "the desktop",
		Doc: "A notification: a question waiting, a flash message, a failure to report."})
}
