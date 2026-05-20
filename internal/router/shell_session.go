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

	// lastFocused is the window currently believed to hold focus on
	// this shell. Used to emit EvtWindowUnfocus on the previous
	// window when focus moves. Touched only from the shell's frame
	// loop goroutine.
	lastFocused uint32
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
	if err := s.writeCtrlLocked(wire.NewShellAppDeclared(
		inst.InstanceID,
		inst.Manifest.Element,
		surface,
		manifestJSON,
	)); err != nil {
		return err
	}
	if inst.WindowID != 0 {
		var w, h uint32
		if inst.Manifest.Window != nil {
			w = inst.Manifest.Window.DefaultWidth
			h = inst.Manifest.Window.DefaultHeight
		}
		return s.writeCtrlLocked(wire.NewShellWindowCreate(
			inst.WindowID, inst.InstanceID, inst.Manifest.Name, w, h,
		))
	}
	return nil
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
	// deduped.
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
		// v0.0 reserves ≥ 1 on the WS side.
		return nil
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
			for _, sh := range s.router.shellList() {
				_ = sh.WriteCtrl(wire.NewShellWindowDestroy(m.WindowID))
			}
			// teardown is handled by the loop when the app exits.
			if inst.Cmd != nil && inst.Cmd.Process != nil {
				// Send SIGTERM gracefully; loop will detect EOF.
				_ = inst.Cmd.Process.Signal(stopSignal())
			}
		}
	}()
	return nil
}

func (s *ShellSession) handleWindowFocus(m wire.ShellWindowFocus) error {
	if m.WindowID == s.lastFocused {
		return nil
	}
	prev := s.lastFocused
	s.lastFocused = m.WindowID

	s.router.mu.Lock()
	inst := s.router.byWin[m.WindowID]
	prevInst := s.router.byWin[prev]
	s.router.mu.Unlock()

	if prev != 0 && prevInst != nil {
		if err := prevInst.WriteEvt(wire.NewEvtWindowUnfocus(prev)); err != nil {
			s.router.log("unfocus relay: %v", err)
		}
	}
	if inst == nil {
		return nil
	}
	return inst.WriteEvt(wire.NewEvtWindowFocus(m.WindowID))
}

func (s *ShellSession) handleWindowResize(m wire.ShellWindowResize) error {
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
