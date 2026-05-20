package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/sirmick/wash/internal/wire"
)

// ShellSession is the per-shell connection state. The router keeps
// many (v0.0 in practice keeps one).
type ShellSession struct {
	Transport FrameTransport

	router  *Router
	writeMu sync.Mutex

	// declared is guarded by writeMu (set when announcing, cleared
	// when undeclaring) — kept under the same lock as writes so a
	// declare and any follow-on relay are observed in order by the
	// receiver.
	declared map[string]bool
}

// declareInstance sends ShellAppDeclared (and ShellWindowCreate for
// windowed apps) for inst, exactly once per ShellSession. Concurrent
// callers race safely — the second is a no-op.
//
// The dedupe and the writes both run under writeMu, so a parallel
// declareExistingAppsTo holding the lock keeps relays from squeezing
// in between declared+create and any follow-on title/focus relay.
func (s *ShellSession) declareInstance(inst *AppInstance) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.declareInstanceLocked(inst)
}

// declareInstanceLocked assumes writeMu is held by the caller.
//
// Sends app.declared so the shell starts fetching the bundle. The
// window itself comes via session.snapshot / session.patch — the
// router owns geometry, not declare.
func (s *ShellSession) declareInstanceLocked(inst *AppInstance) error {
	if s.declared == nil {
		s.declared = make(map[string]bool)
	}
	if s.declared[inst.InstanceID] {
		return nil
	}
	s.declared[inst.InstanceID] = true

	manifestJSON, err := json.Marshal(inst.Manifest)
	if err != nil {
		return err
	}
	surface := inst.Manifest.Surface
	if inst.Kiosk {
		surface = SurfaceDesktop
	}
	return s.writeCtrlLocked(wire.NewShellAppDeclared(
		inst.InstanceID,
		inst.Manifest.Element,
		surface,
		manifestJSON,
	))
}

// undeclareInstance forgets inst so a future declare can fire again
// (e.g. if instancing logic permits a fresh handshake).
func (s *ShellSession) undeclareInstance(instanceID string) {
	s.writeMu.Lock()
	delete(s.declared, instanceID)
	s.writeMu.Unlock()
}

// HandleShell takes ownership of t for the lifetime of a browser
// shell's connection: ensures the session app is running, declares
// already-running instances to it (so a late-connecting shell sees
// the world), then runs the frame loop.
func (r *Router) HandleShell(ctx context.Context, t FrameTransport) error {
	sess := &ShellSession{Transport: t, router: r}
	defer t.Close()

	// Hold writeMu for the whole setup. While we hold it, any
	// concurrent HandleApp.declareInstance blocks at the same mutex,
	// so the receiver sees catalog → declared → create in order
	// regardless of which goroutine got there first.
	sess.writeMu.Lock()
	if err := sess.writeCtrlLocked(wire.NewShellCatalog(r.catalog())); err != nil {
		sess.writeMu.Unlock()
		return err
	}
	r.registerShell(sess)
	defer r.unregisterShell(sess)

	// Snapshot apps under the router lock; declare them while we
	// still hold writeMu so any racing HandleApp is correctly
	// deduped. Order matters: app.declared (one per app) must arrive
	// before the session.snapshot so the shell has bundles in flight
	// by the time it sees window upserts.
	r.mu.Lock()
	snapshot := make([]*AppInstance, 0, len(r.apps))
	for _, inst := range r.apps {
		snapshot = append(snapshot, inst)
	}
	r.mu.Unlock()
	for _, inst := range snapshot {
		if err := sess.declareInstanceLocked(inst); err != nil {
			sess.writeMu.Unlock()
			return err
		}
	}
	if err := sess.writeCtrlLocked(wire.NewShellSessionSnapshot(r.winSession.snapshot())); err != nil {
		sess.writeMu.Unlock()
		return err
	}
	sess.writeMu.Unlock()

	if err := r.EnsureSessionRunning(ctx); err != nil {
		r.log("ensure session: %v", err)
	}
	if err := r.EnsureInitialAppRunning(ctx); err != nil {
		r.log("ensure initial: %v", err)
	}
	return sess.loop(ctx)
}


func (s *ShellSession) loop(ctx context.Context) error {
	type readResult struct {
		f   wire.Frame
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		for {
			f, err := s.Transport.ReadFrame()
			ch <- readResult{f, err}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case rr := <-ch:
			if rr.err != nil {
				if errors.Is(rr.err, io.EOF) {
					return nil
				}
				return rr.err
			}
			if err := s.dispatch(rr.f); err != nil {
				return err
			}
		}
	}
}

func (s *ShellSession) dispatch(f wire.Frame) error {
	if f.Channel != ChannelControl {
		// Channel ≥ 1 on WS: raw byte stream. Forward to the bound
		// app verbatim on the same channel id.
		b := s.router.lookupChannel(f.Channel)
		if b == nil || b.shell != s {
			s.router.log("shell: drop raw frame on unbound channel %d", f.Channel)
			return nil
		}
		return b.app.writeRawFrame(f.Channel, f.Payload)
	}
	msg, err := wire.DecodeCtrl(f.Payload)
	if err != nil {
		return fmt.Errorf("shell ctrl decode: %w", err)
	}
	switch m := msg.(type) {
	case wire.ShellAssetFetch:
		return s.handleAssetFetch(m)
	case wire.ShellWindowCloseClicked:
		return s.handleWindowCloseClicked(m)
	case wire.ShellWindowFocus:
		return s.handleWindowFocus(m)
	case wire.ShellWindowMove:
		return s.handleWindowMove(m)
	case wire.ShellWindowResize:
		return s.handleWindowResize(m)
	case wire.ShellWindowState:
		return s.handleWindowState(m)
	case wire.ShellAppMsgSend:
		return s.handleAppMsgSend(m)
	case wire.ShellLog:
		return s.handleShellLog(m)
	}
	s.router.log("shell: unexpected ctrl msg %T", msg)
	return nil
}

func (s *ShellSession) handleShellLog(m wire.ShellLog) error {
	src := m.Source
	if src == "" {
		src = "shell"
	}
	if m.Stack != "" {
		s.router.log("browser/%s [%s] %s\n%s", src, m.Level, m.Msg, m.Stack)
	} else {
		s.router.log("browser/%s [%s] %s", src, m.Level, m.Msg)
	}
	return nil
}

func (s *ShellSession) handleAssetFetch(m wire.ShellAssetFetch) error {
	inst := s.router.appByInstance(m.InstanceID)
	if inst == nil {
		s.router.log("shell asset.fetch for unknown instance %q", m.InstanceID)
		return nil
	}
	return inst.requestAsset(m.Name, m.InstanceID)
}

func (s *ShellSession) handleWindowCloseClicked(m wire.ShellWindowCloseClicked) error {
	s.router.mu.Lock()
	inst := s.router.byWin[m.WindowID]
	s.router.mu.Unlock()
	if inst == nil {
		return nil
	}
	// Drive the close handshake in a goroutine — the loop must keep
	// reading so confirm_close can arrive.
	go func() {
		allowed, err := inst.requestClose(context.Background())
		if err != nil {
			s.router.log("close window %d: %v", m.WindowID, err)
			return
		}
		if allowed {
			// Tell shells the window is gone now. The app's loop
			// teardown will also call destroyWindow when it exits;
			// the second call is a no-op (already deleted).
			s.router.broadcastPatches(s.router.winSession.destroyWindow(m.WindowID))
			if inst.Cmd != nil && inst.Cmd.Process != nil {
				// Send SIGTERM gracefully; loop will detect EOF.
				_ = inst.Cmd.Process.Signal(stopSignal())
			}
		}
	}()
	return nil
}

// handleWindowFocus updates router state and tells the affected apps
// about focus/unfocus on their event channels. Focus state is global
// to the session — flipping it in one shell propagates to all shells
// via the broadcast patch.
func (s *ShellSession) handleWindowFocus(m wire.ShellWindowFocus) error {
	// Snapshot previous focused window before the mutation so we can
	// emit EvtWindowUnfocus to whoever lost it.
	prev := s.router.winSession.focusedWindowID()
	patches := s.router.winSession.focus(m.WindowID)
	if len(patches) == 0 {
		return nil
	}
	s.router.broadcastPatches(patches)
	if prev != 0 && prev != m.WindowID {
		s.router.mu.Lock()
		prevInst := s.router.byWin[prev]
		s.router.mu.Unlock()
		if prevInst != nil {
			if err := prevInst.WriteEvt(wire.NewEvtWindowUnfocus(prev)); err != nil {
				s.router.log("unfocus relay: %v", err)
			}
		}
	}
	s.router.mu.Lock()
	inst := s.router.byWin[m.WindowID]
	s.router.mu.Unlock()
	if inst == nil {
		return nil
	}
	return inst.WriteEvt(wire.NewEvtWindowFocus(m.WindowID))
}

func (s *ShellSession) handleWindowMove(m wire.ShellWindowMove) error {
	s.router.broadcastPatches(s.router.winSession.move(m.WindowID, m.X, m.Y))
	// No EvtWindowMove on the app side yet — apps that care about
	// position would need a new event; nothing requests it today.
	return nil
}

func (s *ShellSession) handleWindowResize(m wire.ShellWindowResize) error {
	s.router.broadcastPatches(s.router.winSession.resize(m.WindowID, m.W, m.H))
	s.router.mu.Lock()
	inst := s.router.byWin[m.WindowID]
	s.router.mu.Unlock()
	if inst == nil {
		return nil
	}
	return inst.WriteEvt(wire.NewEvtWindowResize(m.WindowID, m.W, m.H))
}

func (s *ShellSession) handleWindowState(m wire.ShellWindowState) error {
	switch m.State {
	case wire.WindowStateNormal, wire.WindowStateMinimized, wire.WindowStateMaximized:
	default:
		s.router.log("shell: invalid window.state %q", m.State)
		return nil
	}
	s.router.broadcastPatches(s.router.winSession.setState(m.WindowID, m.State))
	s.router.mu.Lock()
	inst := s.router.byWin[m.WindowID]
	s.router.mu.Unlock()
	if inst == nil {
		return nil
	}
	return inst.WriteEvt(wire.NewEvtWindowState(m.WindowID, m.State))
}

func (s *ShellSession) handleAppMsgSend(m wire.ShellAppMsgSend) error {
	inst := s.router.appByInstance(m.InstanceID)
	if inst == nil {
		return nil
	}
	// The shell sends data as a JSON value; we relay it as the
	// CBOR-encoded app_msg payload. The data is passed through
	// without inspection — see "transport, not interpreter".
	var raw any
	if err := json.Unmarshal(m.Data, &raw); err != nil {
		return fmt.Errorf("app_msg.send data: %w", err)
	}
	return inst.WriteEvt(wire.NewEvtAppMsg(inst.WindowID, raw))
}

// WriteCtrl encodes m as JSON and writes a shell control-channel frame.
func (s *ShellSession) WriteCtrl(m any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeCtrlLocked(m)
}

// writeCtrlLocked assumes writeMu is held by the caller.
func (s *ShellSession) writeCtrlLocked(m any) error {
	data, err := wire.EncodeCtrl(m)
	if err != nil {
		return err
	}
	return s.Transport.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data})
}

// WriteRawFrame writes a bare-byte frame on a dynamic channel. The
// router calls this when forwarding raw bytes from an app to its
// bound shell.
func (s *ShellSession) WriteRawFrame(channelID uint32, payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.Transport.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: payload})
}
