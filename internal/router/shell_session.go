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
}

// HandleShell takes ownership of t for the lifetime of a browser
// shell's connection: ensures the session app is running, declares
// already-running instances to it (so a late-connecting shell sees
// the world), then runs the frame loop.
func (r *Router) HandleShell(ctx context.Context, t FrameTransport) error {
	sess := &ShellSession{Transport: t, router: r}
	r.registerShell(sess)
	defer r.unregisterShell(sess)
	defer t.Close()

	if err := r.EnsureSessionRunning(ctx); err != nil {
		r.log("ensure session: %v", err)
		// continue — a shell with no session is still useful for
		// debugging in commits where the session app does not
		// exist yet.
	}

	if err := r.declareExistingAppsTo(sess); err != nil {
		return err
	}
	return sess.loop(ctx)
}

// declareExistingAppsTo emits ShellAppDeclared + ShellWindowCreate for
// every currently-registered app to one shell. Called on shell connect
// so a late join sees existing windows.
func (r *Router) declareExistingAppsTo(s *ShellSession) error {
	r.mu.Lock()
	snapshot := make([]*AppInstance, 0, len(r.apps))
	for _, inst := range r.apps {
		snapshot = append(snapshot, inst)
	}
	r.mu.Unlock()
	for _, inst := range snapshot {
		manifestJSON, err := json.Marshal(inst.Manifest)
		if err != nil {
			return err
		}
		if err := s.WriteCtrl(wire.NewShellAppDeclared(inst.InstanceID, inst.Manifest.Element, inst.Manifest.Surface, manifestJSON)); err != nil {
			return err
		}
		if inst.WindowID != 0 {
			var w, h uint32
			if inst.Manifest.Window != nil {
				w, h = inst.Manifest.Window.DefaultWidth, inst.Manifest.Window.DefaultHeight
			}
			if err := s.WriteCtrl(wire.NewShellWindowCreate(inst.WindowID, inst.InstanceID, inst.Manifest.Name, w, h)); err != nil {
				return err
			}
		}
	}
	return nil
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
	case wire.ShellAppMsgSend:
		return s.handleAppMsgSend(m)
	}
	s.router.log("shell: unexpected ctrl msg %T", msg)
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
	s.router.mu.Lock()
	inst := s.router.byWin[m.WindowID]
	s.router.mu.Unlock()
	if inst == nil {
		return nil
	}
	return inst.WriteEvt(wire.NewEvtWindowFocus(m.WindowID))
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
	data, err := wire.EncodeCtrl(m)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.Transport.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data})
}
