package router

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
)

// cliSession is a control-socket connection that has been promoted
// to a streaming back-channel for wash-priv's inline ops (wash-sudo
// CLI). It is *not* an AppInstance — there is no manifest, no
// window, no event-channel handshake — but it does get a synthetic
// instance id of the form "cli-<n>" so SendAppMsgTo from any wash
// app can address it.
//
// Lifecycle: created by the control socket's priv.run handler,
// registered in r.cliSessions, unregistered when the conn closes.
// All writes through writeJSON are serialised by writeMu; closed
// is set once so a late writer can fast-fail instead of EPIPE.
//
// Trust: the conn arrives via the chmod-0600 control socket, so the
// peer is the same user. SO_PEERCRED gives us the unforgeable peer
// pid, which wash-priv displays in the approval row alongside the
// tty + comm read from /proc/<pid>/*.
type cliSession struct {
	instanceID string
	conn       net.Conn
	peerUID    uint32
	peerPID    int32

	writeMu sync.Mutex
	closed  atomic.Bool
}

// registerCliSession allocates a synthetic instance id, reads peer
// credentials from the unix socket, and stores the session in the
// router's map. Returns the session for the control handler to drive.
// On non-unix sockets peerUID/peerPID stay at their zero values —
// callers should treat that as "unknown" rather than "root" (see
// UIDNoPeer for the AppInstance equivalent).
func (r *Router) registerCliSession(conn net.Conn) *cliSession {
	uid, pid := readPeerCreds(conn)
	id := fmt.Sprintf("cli-%d", r.nextInstance.Add(1))
	s := &cliSession{
		instanceID: id,
		conn:       conn,
		peerUID:    uid,
		peerPID:    pid,
	}
	r.cliMu.Lock()
	r.cliSessions[id] = s
	r.cliMu.Unlock()
	return s
}

// findCliSession returns the session for an instance id, or nil.
// Callers MUST nil-check; resolveRecipient uses this to detect
// cli-addressed SendAppMsgTo before falling through to AppInstance
// lookup.
func (r *Router) findCliSession(id string) *cliSession {
	if id == "" {
		return nil
	}
	r.cliMu.Lock()
	defer r.cliMu.Unlock()
	return r.cliSessions[id]
}

// unregisterCliSession removes the session from the map and marks
// it closed so concurrent writers stop quickly. The conn close is
// the caller's responsibility — typically the control handler
// defers conn.Close().
func (r *Router) unregisterCliSession(s *cliSession) {
	if s == nil {
		return
	}
	s.closed.Store(true)
	r.cliMu.Lock()
	delete(r.cliSessions, s.instanceID)
	r.cliMu.Unlock()
}

// writeJSON serialises payload as a single line on the control conn.
// Safe for concurrent callers — wash-priv's stdout/stderr/exit-code
// goroutines all share one session.
func (s *cliSession) writeJSON(payload map[string]any) error {
	if s.closed.Load() {
		return fmt.Errorf("cli session %s closed", s.instanceID)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.conn.Write(b)
	return err
}

// writeAppMsg is what relayAppMsgCrossInstance calls when an
// EvtAppMsg targets this session. The payload arrives as
// json.RawMessage from the event channel; it ships verbatim inside
// a {t:"priv.event", from:{…}, data:{…}} envelope so wash-sudo's
// reader sees a consistent frame shape.
func (s *cliSession) writeAppMsg(from *Sender, data json.RawMessage) error {
	out := map[string]any{
		"t":    "priv.event",
		"data": data,
	}
	if from != nil {
		fromMap := map[string]any{}
		if from.AppID != "" {
			fromMap["app_id"] = from.AppID
		}
		if from.InstanceID != "" {
			fromMap["instance_id"] = from.InstanceID
		}
		out["from"] = fromMap
	}
	return s.writeJSON(out)
}

// Sender is a router-local mirror of wire.Sender so cliSession can
// accept it without an import cycle with internal/wire. Identical
// fields; used only on the relay → cli session edge.
type Sender struct {
	AppID      string
	InstanceID string
}

// readPeerCreds reads (uid, pid) via SO_PEERCRED. Returns
// (UIDNoPeer, 0) on a non-unix conn or any failure — caller treats
// that as "unknown peer" rather than failing.
func readPeerCreds(conn net.Conn) (uint32, int32) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return UIDNoPeer, 0
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return UIDNoPeer, 0
	}
	var ucred *syscall.Ucred
	var ucredErr error
	if err := raw.Control(func(fd uintptr) {
		ucred, ucredErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || ucredErr != nil || ucred == nil {
		return UIDNoPeer, 0
	}
	return ucred.Uid, ucred.Pid
}
