package router

import (
	"net"

	"github.com/sirmick/wash/internal/wire"
)

// Remote-apps relay (docs/REMOTE.md). The browser keeps ONE connection to
// A; host B's wire rides a multiplexed "peer" channel on it, which A
// splices verbatim to a socket the com.wash.remote supervisor ssh -L'd to
// reach B. A never decodes B's frames — it is a byte conduit, like the ssh
// client itself, preserving the flat-router / verbatim-splice invariant.
//
// Flow:
//   - supervisor (origin owner) → EvtPeerRegister{origin, network, addr}
//     → registerPeer (gated to the supervisor's app id).
//   - browser → ShellPeerAttach{origin} → handlePeerAttach: dial the
//     socket, allocate a channel, channel.bind{kind:peer,origin} to the
//     shell, and start pumpPeerToShell (socket → channel). The reverse
//     (channel → socket) is the raw-frame dispatch in shell_session.go.

// remoteSupervisorAppID is the only app allowed to register peer sockets —
// dialing arbitrary local sockets and splicing them to a browser is not a
// capability for any app. Duplicated as a string (no import of the app
// package) — the contract is the id, same trust boundary either way.
const remoteSupervisorAppID = "com.wash.remote"

// peerTarget is a registered remote-host socket (where to dial for origin).
type peerTarget struct {
	network string // "unix" | "tcp"
	addr    string
}

// handlePeerRegister records (origin → socket) for the relay, but only from
// the supervisor. A spurious register from any other app is logged + dropped.
func (inst *AppInstance) handlePeerRegister(m wire.EvtPeerRegister) error {
	if inst.AppID != remoteSupervisorAppID {
		inst.router.log("peer register from %s refused (only %s may register)", inst.AppID, remoteSupervisorAppID)
		return nil
	}
	if m.Origin == "" || m.Addr == "" {
		return nil
	}
	inst.router.registerPeer(m.Origin, m.Network, m.Addr)
	return nil
}

func (r *Router) registerPeer(origin, network, addr string) {
	if network == "" {
		network = "unix"
	}
	r.peersMu.Lock()
	r.peers[origin] = peerTarget{network: network, addr: addr}
	r.peersMu.Unlock()
	r.log("peer register origin=%s %s://%s", origin, network, addr)
}

func (r *Router) unregisterPeer(origin string) {
	r.peersMu.Lock()
	delete(r.peers, origin)
	r.peersMu.Unlock()
	r.log("peer unregister origin=%s", origin)
}

func (r *Router) lookupPeer(origin string) (peerTarget, bool) {
	r.peersMu.Lock()
	defer r.peersMu.Unlock()
	t, ok := r.peers[origin]
	return t, ok
}

// handlePeerAttach dials the registered socket for origin, allocates a peer
// channel on this shell, announces it (channel.bind kind=peer), and starts
// the socket→channel pump. Fire-and-forget: a failure is logged and no
// channel is bound, so the FE simply never gets a second RouterClient.
func (s *ShellSession) handlePeerAttach(m wire.ShellPeerAttach) error {
	if m.Origin == "" {
		return nil
	}
	// Idempotent: one relay channel per (shell, origin). A repeated attach
	// (buggy FE, reconnect) must not dial another socket + leak a channel.
	s.peerMu.Lock()
	for _, b := range s.peerChannels {
		if b.origin == m.Origin {
			s.peerMu.Unlock()
			return nil
		}
	}
	s.peerMu.Unlock()
	target, ok := s.router.lookupPeer(m.Origin)
	if !ok {
		s.router.log("peer attach %s: no registration", m.Origin)
		return s.WriteCtrl(wire.NewShellPeerError(m.Origin, "no registration for origin"))
	}
	conn, err := net.Dial(target.network, target.addr)
	if err != nil {
		s.router.log("peer attach %s: dial %s://%s: %v", m.Origin, target.network, target.addr, err)
		return s.WriteCtrl(wire.NewShellPeerError(m.Origin, "dial failed: "+err.Error()))
	}
	id := s.router.allocChannelID()
	b := &channelBinding{
		channelID: id,
		kind:      wire.ChannelKindPeer,
		shell:     s,
		peerConn:  conn,
		origin:    m.Origin,
	}
	s.router.registerChannel(b)
	s.trackPeer(b)
	if err := s.WriteCtrl(wire.ShellChannelBind{
		T:         wire.TShellChannelBind,
		ChannelID: id,
		Kind:      wire.ChannelKindPeer,
		Origin:    m.Origin,
	}); err != nil {
		s.router.closeChannel(id, "peer bind write failed")
		return err
	}
	s.router.log("peer attach %s: channel=%d -> %s://%s", m.Origin, id, target.network, target.addr)
	go s.router.pumpPeerToShell(b)
	return nil
}

// pumpPeerToShell copies the relay socket → the shell channel: host B's
// wire bytes become raw frames the FE feeds to B's RouterClient. Bulk class
// so the whole remote stream is flow-controlled by the channel's credit
// window (the FE replenishes as it absorbs) AND yields to A's LOCAL
// interactive traffic in the scheduler — a remote app must not starve the
// local desktop, and a B flood must not OOM A/the browser. (B's own
// interactive/bulk split is flattened here: A treats the relay as opaque
// bytes — preserving it would need per-class relay channels, a later
// refinement; docs/REMOTE.md §7.) Exits when the socket closes or the shell
// write fails; closeChannel cleans both up.
func (r *Router) pumpPeerToShell(b *channelBinding) {
	buf := make([]byte, 32*1024)
	for {
		n, err := b.peerConn.Read(buf)
		if n > 0 {
			payload := make([]byte, n)
			copy(payload, buf[:n])
			if werr := b.shell.WriteRawFrameClass(b.channelID, payload, wire.ClassBulk); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	r.closeChannel(b.channelID, "peer socket closed")
}

// ---- per-shell peer-channel tracking (teardown on disconnect) ----

func (s *ShellSession) trackPeer(b *channelBinding) {
	s.peerMu.Lock()
	s.peerChannels[b.channelID] = b
	s.peerMu.Unlock()
}

func (s *ShellSession) untrackPeer(id uint32) {
	s.peerMu.Lock()
	delete(s.peerChannels, id)
	s.peerMu.Unlock()
}

// detachPeer closes every relay channel this shell holds for origin.
func (s *ShellSession) detachPeer(origin string) {
	s.peerMu.Lock()
	var ids []uint32
	for id, b := range s.peerChannels {
		if b.origin == origin {
			ids = append(ids, id)
		}
	}
	s.peerMu.Unlock()
	for _, id := range ids {
		s.router.closeChannel(id, "peer detached")
	}
}

// closeAllPeers tears down every relay channel this shell holds — called on
// shell disconnect so a browser leaving closes its ssh -L'd sockets.
func (s *ShellSession) closeAllPeers() {
	s.peerMu.Lock()
	ids := make([]uint32, 0, len(s.peerChannels))
	for id := range s.peerChannels {
		ids = append(ids, id)
	}
	s.peerMu.Unlock()
	for _, id := range ids {
		s.router.closeChannel(id, "shell disconnected")
	}
}
