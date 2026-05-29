// Attach: control-socket path for apps to register themselves
// with the router. Router-spawned apps and terminal-launched
// apps go through the same code.
//
// Flow:
//   1. App dials the control socket (path from WASH_DISPLAY env).
//   2. App writes its Identity frame on channel 0. Includes pid.
//   3. Router checks the pending-attach map by pid:
//        - If pending: this is the dial-back from a spawn the
//          router started. The conn becomes the AppInstance's
//          transport; the spawn caller receives the inst.
//        - If not pending: this is a fresh attach (terminal-
//          launched or external tool). Router validates that the
//          claimed app_id is in the registry AND /proc/<pid>/exe
//          matches the registered binary, then accepts.
//   4. Router writes IdentityAck with the assigned instance/window
//      ids and registers the app.
//
// Auth model: registry-only. A binary that isn't in the registry
// cannot attach. A registered binary cannot claim a different
// app_id than its own (binary-path check).

package router

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// readPeerUID returns the kernel-attested uid of the connected peer,
// or UIDNoPeer if conn isn't a unix socket or SO_PEERCRED fails. The
// router uses this exclusively for the IsRoot decision; on Linux
// SO_PEERCRED is set at connect time (server side reads it on the
// accepted socket).
func readPeerUID(conn net.Conn) uint32 {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return UIDNoPeer
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return UIDNoPeer
	}
	var ucred *syscall.Ucred
	var ucredErr error
	if err := raw.Control(func(fd uintptr) {
		ucred, ucredErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || ucredErr != nil || ucred == nil {
		return UIDNoPeer
	}
	return ucred.Uid
}

// handleAttach is the wash-frame branch of the control-socket
// dispatcher. The conn is wrapped in a stream transport that
// reads from `rd` (preserving the byte peeked by handleControl);
// after a successful attach the conn lifetime is owned by the
// AppInstance loop, so we DON'T close conn on success.
func (r *Router) handleAttach(ctx context.Context, conn net.Conn, rd *bufio.Reader) {
	peerUID := readPeerUID(conn)
	transport := wire.NewStreamTransport(&bufferedReadWriter{r: rd, w: conn, c: conn})
	f, err := transport.ReadFrame()
	if err != nil {
		_ = conn.Close()
		return
	}
	if f.Channel != ChannelControl {
		_ = transport.WriteFrame(controlFrame(wire.NewError(wire.ErrCodeBadIdentity, "identity must be on channel 0")))
		_ = conn.Close()
		return
	}
	msg, err := wire.DecodeCtrl(f.Payload)
	if err != nil {
		_ = transport.WriteFrame(controlFrame(wire.NewError(wire.ErrCodeBadIdentity, "decode identity: "+err.Error())))
		_ = conn.Close()
		return
	}
	ident, ok := msg.(wire.Identity)
	if !ok {
		_ = transport.WriteFrame(controlFrame(wire.NewError(wire.ErrCodeBadIdentity, fmt.Sprintf("expected identity, got %T", msg))))
		_ = conn.Close()
		return
	}
	if ident.Proto != ProtocolVersion {
		_ = transport.WriteFrame(controlFrame(wire.NewError(wire.ErrCodeProtoMismatch, "protocol version mismatch")))
		_ = conn.Close()
		return
	}

	// Token-attach branch — the child was forked by an external
	// spawner (e.g. wash-priv under sudo). The token must match a
	// live pending record; the dialing app_id must match what the
	// token was bound to; and /proc/<pid>/exe must match the
	// registered binary path. Token check first because tokens are
	// the strong signal — pid match is secondary defense.
	if ident.AttachToken != "" {
		rec := r.takeTokenPending(ident.AttachToken)
		if rec == nil {
			_ = transport.WriteFrame(controlFrame(wire.NewError("forbidden", "attach token expired or unknown")))
			_ = conn.Close()
			return
		}
		if rec.appID != ident.AppID {
			_ = transport.WriteFrame(controlFrame(wire.NewError("forbidden", "attach token bound to a different app_id")))
			_ = conn.Close()
			return
		}
		if !validateAttachBinary(ident.PID, rec.binary) {
			procExe := fmt.Sprintf("/proc/%d/exe", ident.PID)
			actual, _ := os.Readlink(procExe)
			r.log("token attach refused: pid=%d /proc/exe=%q expected=%q", ident.PID, actual, rec.binary)
			_ = transport.WriteFrame(controlFrame(wire.NewError("forbidden", "binary does not match the prepared spawn")))
			_ = conn.Close()
			return
		}
		entry := r.reg.ByID(ident.AppID)
		if entry == nil {
			_ = transport.WriteFrame(controlFrame(wire.NewError("forbidden", "app_id no longer in registry")))
			_ = conn.Close()
			return
		}
		inst := &AppInstance{
			Transport:  transport,
			AppID:      ident.AppID,
			Manifest:   entry.Manifest,
			PeerUID:    peerUID,
			InstanceID: rec.instanceID,
			router:     r,
			startedAt:  time.Now(),
		}
		if inst.Manifest.Surface == SurfaceWindow {
			inst.WindowID = r.allocWindowID()
		}
		ack := wire.NewIdentityAck(inst.InstanceID, inst.WindowID)
		ack.Session = r.handshakeSession()
		if err := inst.writeCtrl(ack); err != nil {
			_ = conn.Close()
			return
		}
		go r.startFreshAttach(ctx, inst)
		return
	}

	// Spawn-completion branch — router started a child and is
	// blocked waiting for it to dial back.
	if rec, found := r.takePendingAttach(ident.PID); found {
		entry := r.reg.ByID(ident.AppID)
		if entry == nil {
			rec.ch <- attachResult{err: fmt.Errorf("attach: app_id %q not in registry", ident.AppID)}
			_ = conn.Close()
			return
		}
		// Stamp Kiosk BEFORE acceptIdentity — handshake's WindowID
		// allocation in handleAppOpts gates on !inst.Kiosk, so a
		// stale-false value here would phantom-allocate a window id
		// for a kiosk app and surface it in the FE's session
		// snapshot as a second mount.
		inst := &AppInstance{
			Transport: transport,
			AppID:     ident.AppID,
			Manifest:  entry.Manifest,
			PeerUID:   peerUID,
			Kiosk:     rec.kiosk,
			router:    r,
		}
		if err := r.acceptIdentity(inst); err != nil {
			rec.ch <- attachResult{err: err}
			_ = conn.Close()
			return
		}
		rec.ch <- attachResult{inst: inst}
		return
	}

	// Fresh-attach branch — process wasn't spawned by us. Validate
	// it's a registered binary that's allowed to claim ident.AppID.
	entry := r.reg.ByID(ident.AppID)
	if entry == nil {
		_ = transport.WriteFrame(controlFrame(wire.NewError("forbidden", "app_id not registered")))
		_ = conn.Close()
		return
	}
	if !validateAttachBinary(ident.PID, entry.Path) {
		_ = transport.WriteFrame(controlFrame(wire.NewError("forbidden", "binary does not match registered app_id")))
		_ = conn.Close()
		return
	}
	inst := &AppInstance{
		Transport: transport,
		AppID:     ident.AppID,
		Manifest:  entry.Manifest,
		PeerUID:   peerUID,
		router:    r,
		startedAt: time.Now(),
	}
	if err := r.acceptIdentity(inst); err != nil {
		_ = conn.Close()
		return
	}
	// No spawn caller to hand the inst to — we own the full
	// lifecycle from here (declare to attached shells, broadcast
	// the window patch, start the loop).
	go r.startFreshAttach(ctx, inst)
}

// acceptIdentity assigns instance/window ids and writes the
// IdentityAck. Shared by the spawn-completion and fresh-attach
// branches so both produce identical AppInstance state.
func (r *Router) acceptIdentity(inst *AppInstance) error {
	inst.InstanceID = r.allocInstanceID()
	if inst.Manifest.Surface == SurfaceWindow && !inst.Kiosk {
		inst.WindowID = r.allocWindowID()
	}
	ack := wire.NewIdentityAck(inst.InstanceID, inst.WindowID)
	ack.Session = r.handshakeSession()
	if err := inst.writeCtrl(ack); err != nil {
		return fmt.Errorf("write identity.ack: %w", err)
	}
	return nil
}

// validateAttachBinary resolves /proc/<pid>/exe and checks that
// it matches the registered binary path. EvalSymlinks on both
// sides so distro-managed installs (binary symlinked into PATH)
// still match.
//
// Fallback for root-owned children (the wash-priv → sudo → app
// case): /proc/<pid>/exe is unreadable to non-root by default
// (kernel restricts the symlink to ptrace-capable readers).
// /proc/<pid>/comm IS world-readable and carries the binary
// basename, truncated to TASK_COMM_LEN-1 = 15 chars. We use it as
// a sanity check when exe is blocked — the strong auth is the
// attach token anyway; this is belt-and-suspenders confirming the
// thing on the other end of the socket is at least named like the
// registered binary.
//
// Returns false on any error — defense in depth.
func validateAttachBinary(pid int, registeredPath string) bool {
	if pid <= 0 {
		return false
	}
	regResolved, err := filepath.EvalSymlinks(registeredPath)
	if err != nil {
		regResolved = registeredPath
	}
	procExe := fmt.Sprintf("/proc/%d/exe", pid)
	if actual, err := os.Readlink(procExe); err == nil {
		actualResolved, err := filepath.EvalSymlinks(actual)
		if err != nil {
			actualResolved = actual
		}
		return actualResolved == regResolved
	}
	// /proc/<pid>/exe is unreadable — most often because the peer is
	// root and we are not. Compare /proc/<pid>/comm against the
	// basename of the registered binary instead. comm is truncated
	// at 15 chars, so match against the same-prefix of basename.
	//
	// We use the *un-resolved* registeredPath so a busybox layout
	// (wash-term → wash) matches: comm is set from argv[0]'s basename
	// ("wash-term"), while basename(regResolved) would resolve through
	// the symlink to "wash" and never match. For the distro-symlink
	// case (registered = /usr/local/bin/foo → /opt/x/bin/foo) the two
	// basenames are still equal, so existing behavior is preserved.
	commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(commBytes))
	want := filepath.Base(registeredPath)
	if len(want) > 15 {
		want = want[:15]
	}
	return comm == want
}

// controlFrame helps build a one-off ctrl-channel frame from an
// outbound message. Used to write Error frames to a conn before
// it has an AppInstance wrapper.
func controlFrame(m any) wire.Frame {
	b, err := wire.EncodeCtrl(m)
	if err != nil {
		return wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: nil}
	}
	return wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: b}
}

// startFreshAttach runs the same post-handshake work as
// spawnAndRun for an app that attached without being spawned by
// the router. The split exists because spawnAndRun handles
// process reaping via cmd.Wait — for fresh attaches there's no
// Cmd; the conn closure IS the process-exit signal.
func (r *Router) startFreshAttach(ctx context.Context, inst *AppInstance) {
	r.bringUp(ctx, inst)
	if err := inst.loop(context.Background()); err != nil {
		r.log("app %s loop: %v", inst.AppID, err)
	}
	// Terminal-attached BE death is the conn-closure: any non-
	// expectedExit drop is a tombstone-worthy event. We can't read
	// the exit code (we don't own the process) and stdout/stderr
	// went to the launching terminal, but the window is real and
	// the user deserves to know it died. The dialog gets a hint
	// pointing at the terminal.
	r.maybeBroadcastFreshAttachCrash(inst)
	r.tearDown(inst)
	_ = inst.Transport.Close()
}

// maybeBroadcastFreshAttachCrash is the terminal-attach analogue of
// maybeBroadcastCrash: same event, no exit code / signal / log
// (those belong to the launching terminal), but enough to convert
// the window into a tombstone instead of silently vanishing.
func (r *Router) maybeBroadcastFreshAttachCrash(inst *AppInstance) {
	if inst.expectedExit.Load() {
		return
	}
	if inst.WindowID == 0 {
		// Desktop/kiosk/cli sessions don't have a tombstone-able
		// window. The exit is still logged via the loop-error line.
		return
	}
	uptime := ""
	if !inst.startedAt.IsZero() {
		uptime = time.Since(inst.startedAt).Round(time.Second).String()
	}
	log := "App process exited unexpectedly.\n\n" +
		"This app was launched from a terminal (or another non-router\n" +
		"parent), so the router didn't capture its stdout/stderr. Check\n" +
		"the launching terminal for the panic trace and any earlier output."
	r.log("app %s exited unexpectedly (terminal-attach) instance=%s uptime=%s",
		inst.AppID, inst.InstanceID, uptime)
	msg := wire.NewShellAppCrashed(inst.InstanceID, inst.AppID, inst.WindowID, -1, "", uptime, log)
	for _, s := range r.shellList() {
		if err := s.WriteCtrl(msg); err != nil {
			r.log("broadcast crash to shell: %v", err)
		}
	}
}

// bufferedReadWriter adapts (*bufio.Reader + net.Conn) into an
// io.ReadWriteCloser so wire.NewStreamTransport can wrap it. The
// reader path goes through the buffer (which already holds the
// peeked-then-not-consumed byte); writes go straight to the
// conn; close closes the conn.
type bufferedReadWriter struct {
	r *bufio.Reader
	w net.Conn
	c net.Conn
}

func (b *bufferedReadWriter) Read(p []byte) (int, error)  { return b.r.Read(p) }
func (b *bufferedReadWriter) Write(p []byte) (int, error) { return b.w.Write(p) }
func (b *bufferedReadWriter) Close() error                { return b.c.Close() }
