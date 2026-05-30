package wire

// Session-boundary control frames for single-stream transports
// (virtio-console / serial). On a real WebSocket the OS/TCP stack hands the
// router a fresh connection per browser — Accept→HandleShell, EOF on
// disconnect — so "one clean session per viewer" is free. A raw serial pipe
// has no such lifecycle: it is one byte-stream that every browser the proxy
// bridges shares for the VM's whole life.
//
// These two frames re-manufacture the missing lifecycle. The host-side proxy
// emits SessionOpen when a browser attaches and SessionClose when it leaves
// (or is taken over by a newer browser); the guest's transport splitter turns
// each open…close span into a fresh virtual connection it hands to HandleShell,
// exactly as a TCP Accept would. They ride channel 0 like other control
// messages, but the splitter consumes them — the shell/FE never sees them.
const (
	TSessionOpen  = "session.open"
	TSessionClose = "session.close"
)

// SessionOpen marks the start of a viewer session (a browser attached).
type SessionOpen struct {
	T string `json:"t"`
}

// NewSessionOpen builds a SessionOpen control message.
func NewSessionOpen() SessionOpen { return SessionOpen{T: TSessionOpen} }

// SessionClose marks the end of a viewer session (the browser left, or a
// newer browser took over).
type SessionClose struct {
	T string `json:"t"`
}

// NewSessionClose builds a SessionClose control message.
func NewSessionClose() SessionClose { return SessionClose{T: TSessionClose} }
