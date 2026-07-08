package router

import (
	"net"
	"net/http/httputil"

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
	// ingressAddr, when non-empty, is a local unix socket forwarded to the
	// peer router's --listen-ingress HTTP endpoint; ingressProxy dials it.
	// Locally-unknown /app/<token>/ requests are resolved against peers and
	// proxied here (remote ingress, issue #15 / docs/REMOTE.md §17).
	ingressAddr  string
	ingressProxy *httputil.ReverseProxy
}

// handlePeerRegister records (origin → socket) for the relay, but only from
// the supervisor. A spurious register from any other app is logged + dropped.
func (inst *AppInstance) handlePeerRegister(m wire.EvtPeerRegister) error {
	if inst.AppID != remoteSupervisorAppID {
		inst.router.log("peer register from %s refused (only %s may register)", inst.AppID, remoteSupervisorAppID)
		return nil
	}
	// No nesting: a relayed endpoint (host B, served via --listen-raw) cannot
	// itself relay to a further host. wash is a flat multi-homed shell, never a
	// chain — to reach a host behind this one, the LOCAL desktop connects to it
	// directly via SSH ProxyJump (docs/REMOTE.md, "we never chain routers").
	if inst.router.cfg.Relayed {
		inst.router.log("peer register origin=%s refused: this router is a relayed endpoint — wash connections don't nest", m.Origin)
		return nil
	}
	if m.Origin == "" || m.Addr == "" {
		return nil
	}
	inst.router.registerPeer(m.Origin, m.Network, m.Addr, m.IngressAddr)
	return nil
}

func (r *Router) registerPeer(origin, network, addr, ingressAddr string) {
	if network == "" {
		network = "unix"
	}
	t := peerTarget{network: network, addr: addr, ingressAddr: ingressAddr}
	if ingressAddr != "" {
		t.ingressProxy = newPeerIngressProxy(origin, ingressAddr, r.ingress)
	}
	r.peersMu.Lock()
	r.peers[origin] = t
	r.peersMu.Unlock()
	r.log("peer register origin=%s %s://%s ingress=%q", origin, network, addr, ingressAddr)
}

// AddPeer is the exported registration used by the runner's --peer-ingress
// dev/test seam — the same record the com.wash.remote supervisor creates via
// EvtPeerRegister. addr may be empty for an ingress-only registration (the
// e2e harness attaches the peer's shell wire directly, not via the relay).
func (r *Router) AddPeer(origin, network, addr, ingressAddr string) {
	r.registerPeer(origin, network, addr, ingressAddr)
}

func (r *Router) unregisterPeer(origin string) {
	r.peersMu.Lock()
	delete(r.peers, origin)
	r.peersMu.Unlock()
	// A vanished peer takes its ingress tokens with it: drop the cached
	// token→origin routes so a reconnected peer re-resolves fresh.
	r.ingress.dropRemoteOrigin(origin)
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
		// Creditless: the relay is a verbatim conduit; B does its own
		// per-channel flow control end-to-end (docs/REMOTE.md §7). An
		// A-side window would double-gate and head-of-line-block B's
		// interactive behind B's bulk in the single pump goroutine.
		noCredit: true,
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

// pumpPeerToShell copies the relay socket → the shell channel, ONE B-frame
// at a time, preserving each frame's wire CLASS for the scheduler
// (docs/REMOTE.md §7). This is the "header-aware, payload-opaque" relay: A
// reads B's frame header (class + length) to delimit + schedule it, but never
// interprets the payload — it forwards the frame's bytes verbatim. So B's
// interactive frames (keystrokes, focus) jump ahead of B's bulk (pty/file
// output) in A's strict-priority scheduler. Without this, a remote bulk stream
// head-of-line-blocks the remote terminal.
//
// The peer channel is CREDITLESS (channelBinding.noCredit): the only thing
// that could make this single goroutine block on a per-frame basis is a credit
// Reserve, and that would reintroduce the very HOL we class-preserve to avoid
// (the pump can't read B's next interactive frame while parked on a bulk
// frame's window). Flow control is B's job — B already paces each of its inner
// channels by the FE's credit, end to end — so A needs no window of its own.
// The remaining backstop is scheduler.Submit blocking when A's bulk queue
// fills, which only happens when the FE is genuinely wedged (and B is throttled
// by then anyway). Per-CHANNEL isolation across the relay is therefore
// unnecessary: per-class scheduling + B's per-channel credit already give it.
//
// DecodeFrameRaw returns B's whole frame (header+payload) as one slice with no
// re-encode/copy; the class is byte 0. "Header-only" is the invariant: A
// touches bytes 0..7 to delimit + class the frame; the payload is forwarded
// unchanged. It is NOT federation (§13): A decodes no ctrl verb, models no B
// state.
func (r *Router) pumpPeerToShell(b *channelBinding) {
	t := wire.NewStreamTransport(b.peerConn)
	for {
		raw, err := t.ReadFrameRaw()
		if err != nil {
			break
		}
		// One B-frame → the payload of one relay frame, scheduled at B's own
		// class (byte 0). Verbatim: no decode/re-encode, no credit gate.
		//
		// A legal B-frame is up to MaxPayload+headerSize bytes (a 16 MiB
		// payload + 8-byte header); wrapping that whole blob as ONE relay
		// frame's payload would exceed MaxPayload, fail EncodeFrame, and tear
		// the relay down (REVIEW-DATAPATH F10). The relay is a byte conduit —
		// the FE reassembles B's wire from arbitrarily-chunked relay bytes
		// (relay-socket.ts) — so an oversized frame is split across successive
		// relay frames on the SAME channel at the SAME class; single channel +
		// single class keeps them FIFO, so B's deframer sees the bytes intact.
		//
		// b.shell is read without shellMu, and that is safe BY INVARIANT, not
		// by luck: a peer binding (peerConn != nil) has its shell pinned at
		// creation, before this goroutine starts (a happens-before edge), and
		// reattachChannelsToShell explicitly skips live peer bindings
		// ("b.shell != nil && b.peerConn != nil → continue"), so nothing ever
		// reassigns b.shell for the pump's lifetime. The channel closing (which
		// stops the pump) is the only teardown. If you ever make a peer
		// binding's shell mutable mid-pump, this read becomes a real data race —
		// take shellMu here (and add a -race test that reattaches under load).
		if werr := writeRelayFrame(b.shell, b.channelID, raw, wire.ClassOfFlags(raw[0])); werr != nil {
			break
		}
	}
	r.closeChannel(b.channelID, "peer socket closed")
}

// writeRelayFrame forwards one B-frame's raw bytes on the peer channel. A
// frame that fits in one relay frame — every producer today — is sent whole
// at B's own class, so B's interactive frames still jump its bulk.
//
// An oversized B-frame (a 16 MiB payload is 16 MiB + 8 on the wire) must be
// split, and its pieces MUST reach the FE contiguously: relay-socket.ts
// reassembles B's wire by byte position, so any peer-channel frame slipped
// between two pieces would be parsed as part of B's frame and corrupt it.
// The scheduler is strict-priority (qos.go), so a following higher-class
// B-frame would preempt a trailing piece — the pieces are therefore sent at
// ClassControl, the top class: FIFO within a class keeps the pump's back-to-
// back pieces adjacent, and nothing outranks Control to slip between them.
// (Cross-channel Control frames are demuxed by channel at the FE before byte
// reassembly, so only another peer-channel frame could corrupt — and the
// pump is the sole peer-channel producer.) A max B-frame yields exactly two
// pieces; oversized frames don't occur today (REVIEW-DATAPATH F10), so the
// brief top-priority burst is a non-issue.
func writeRelayFrame(s *ShellSession, channelID uint32, raw []byte, class wire.Class) error {
	if len(raw) <= wire.MaxPayload {
		return s.WriteRawFrameClass(channelID, raw, class)
	}
	for len(raw) > wire.MaxPayload {
		if err := s.WriteRawFrameClass(channelID, raw[:wire.MaxPayload], wire.ClassControl); err != nil {
			return err
		}
		raw = raw[wire.MaxPayload:]
	}
	return s.WriteRawFrameClass(channelID, raw, wire.ClassControl)
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
