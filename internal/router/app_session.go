package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/sirmick/wash/internal/wire"
)

// closeGrace is the deadline an app has to respond to
// window.close_requested before the router force-kills (WIRE.md §10).
const closeGrace = 5 * time.Second

// AppInstance is one running app's connection state. The router holds
// one per spawned process (and one per in-process app in tests).
type AppInstance struct {
	Transport  FrameTransport
	AppID      string
	InstanceID string
	WindowID   uint32 // 0 if surface=desktop OR Kiosk
	Manifest   *Manifest
	Cmd        *exec.Cmd // nil for in-process tests

	// Kiosk is set for the --initial-app instance. It forces the
	// shell to mount the element at the root surface regardless of
	// the manifest's declared surface, and suppresses the window
	// id so per-window plumbing (set_title etc.) is skipped.
	Kiosk bool

	router *Router

	writeMu sync.Mutex

	// awaiting close confirmation. nil when no close is pending.
	closeMu      sync.Mutex
	closeConfirm chan bool

	// bundleMu guards bundleBytes + bundleChannel + bundleReady.
	// bundleBytes accumulates the JS bundle the SDK uploads
	// post-handshake on the bundle channel. The full bytes are
	// retained for the instance lifetime so every (re)attaching
	// shell can replay them — no second BE round-trip needed.
	bundleMu      sync.Mutex
	bundleBytes   []byte
	bundleChannel uint32        // channel id reserved for app-side upload
	bundleReady   chan struct{} // closed when the upload completes
}

// newBundleReady returns a fresh signaling channel; bundleReady is
// lazy-init'd so AppInstance{} stays zero-valued.
func (inst *AppInstance) ensureBundleReady() {
	inst.bundleMu.Lock()
	if inst.bundleReady == nil {
		inst.bundleReady = make(chan struct{})
	}
	inst.bundleMu.Unlock()
}

// HandleApp is the entrypoint for a freshly-spawned app: it owns the
// transport for the lifetime of the connection. It performs the
// handshake (WIRE.md §6), declares the instance to all shells, then
// runs the frame loop until ctx cancels or the transport closes.
//
// expectedAppID is what the router spawned and what the identity
// frame MUST match. windowed selects whether a window is created on
// declare (surface=window) or the element is mounted as the root
// (surface=desktop).
func (r *Router) HandleApp(ctx context.Context, t FrameTransport, manifest *Manifest, cmd *exec.Cmd) error {
	return r.handleAppOpts(ctx, t, manifest, cmd, false)
}

// HandleAppKiosk is like HandleApp but marks the instance as the
// kiosk root: surface forced to desktop, no window id, declared as
// the root surface regardless of the manifest. Used by --initial-app
// for full-screen single-app deployments and e2e tests.
func (r *Router) HandleAppKiosk(ctx context.Context, t FrameTransport, manifest *Manifest, cmd *exec.Cmd) error {
	return r.handleAppOpts(ctx, t, manifest, cmd, true)
}

func (r *Router) handleAppOpts(ctx context.Context, t FrameTransport, manifest *Manifest, cmd *exec.Cmd, kiosk bool) error {
	inst := &AppInstance{
		Transport: t,
		AppID:     manifest.ID,
		Manifest:  manifest,
		Cmd:       cmd,
		Kiosk:     kiosk,
		router:    r,
	}
	if err := inst.handshake(ctx); err != nil {
		_ = t.Close()
		return fmt.Errorf("handshake %s: %w", manifest.ID, err)
	}
	r.registerApp(inst)
	if err := r.declareAppToAllShells(ctx, inst); err != nil {
		r.log("declare to shell: %v", err)
	}
	if inst.WindowID != 0 {
		var defW, defH uint32
		if inst.Manifest.Window != nil {
			defW = inst.Manifest.Window.DefaultWidth
			defH = inst.Manifest.Window.DefaultHeight
		}
		r.broadcastPatches(r.winSession.createWindow(inst.WindowID, inst.InstanceID, inst.Manifest.Element, inst.Manifest.Icon, inst.Manifest.Name, defW, defH))
		_ = inst.WriteEvt(wire.NewEvtWindowMapped(inst.WindowID))
	}
	err := inst.loop(ctx)
	r.unregisterApp(inst)
	r.closeChannelsForApp(inst, "app exited")
	r.dropAppMsgWatchers(inst.InstanceID)
	r.winSession.dropAppState(inst.InstanceID)
	if inst.WindowID != 0 {
		r.broadcastPatches(r.winSession.destroyWindow(inst.WindowID))
	}
	_ = t.Close()
	return err
}

// handshake reads the app's identity frame, validates it, and replies
// with identity.ack. It assigns an instance id (and window id if the
// app's surface is window).
func (inst *AppInstance) handshake(ctx context.Context) error {
	type readResult struct {
		f   wire.Frame
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		f, err := inst.Transport.ReadFrame()
		ch <- readResult{f, err}
	}()
	var f wire.Frame
	select {
	case <-ctx.Done():
		return ctx.Err()
	case rr := <-ch:
		if rr.err != nil {
			return fmt.Errorf("read identity: %w", rr.err)
		}
		f = rr.f
	}
	if f.Channel != ChannelControl {
		return fmt.Errorf("identity frame on wrong channel %d", f.Channel)
	}
	msg, err := wire.DecodeCtrl(f.Payload)
	if err != nil {
		return fmt.Errorf("decode identity: %w", err)
	}
	ident, ok := msg.(wire.Identity)
	if !ok {
		return fmt.Errorf("expected identity, got %T", msg)
	}
	if ident.AppID != inst.AppID {
		_ = inst.writeCtrl(wire.NewError(wire.ErrCodeBadIdentity, "app_id mismatch"))
		return fmt.Errorf("app_id mismatch: spawned %q, identity %q", inst.AppID, ident.AppID)
	}
	if ident.Proto != ProtocolVersion {
		_ = inst.writeCtrl(wire.NewError(wire.ErrCodeProtoMismatch, "protocol version mismatch"))
		return fmt.Errorf("proto mismatch: %d", ident.Proto)
	}

	inst.InstanceID = inst.router.allocInstanceID()
	if inst.Manifest.Surface == SurfaceWindow && !inst.Kiosk {
		inst.WindowID = inst.router.allocWindowID()
	}
	ack := wire.NewIdentityAck(inst.InstanceID, inst.WindowID)
	ack.Session = inst.router.handshakeSession()
	if err := inst.writeCtrl(ack); err != nil {
		return fmt.Errorf("write identity.ack: %w", err)
	}
	return nil
}

// loop reads frames until the transport closes. ctx cancel triggers a
// best-effort shutdown.
func (inst *AppInstance) loop(ctx context.Context) error {
	type readResult struct {
		f   wire.Frame
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		for {
			f, err := inst.Transport.ReadFrame()
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
			if err := inst.dispatch(rr.f); err != nil {
				return err
			}
		}
	}
}

func (inst *AppInstance) dispatch(f wire.Frame) error {
	switch f.Channel {
	case ChannelControl:
		return inst.handleCtrl(f.Payload)
	case ChannelEvent:
		return inst.handleEvt(f.Payload)
	default:
		// Channel ≥ 2: raw byte stream.
		b := inst.router.lookupChannel(f.Channel)
		if b == nil || b.app != inst {
			inst.router.log("app %s: drop raw frame on unbound channel %d", inst.AppID, f.Channel)
			return nil
		}
		if b.kind == wire.ChannelKindBundle {
			// Bundle bytes from SDK during handshake — append to
			// the instance's bundle cache. No shell forwarding;
			// per-shell replay channels handle delivery.
			inst.bundleMu.Lock()
			inst.bundleBytes = append(inst.bundleBytes, f.Payload...)
			inst.bundleMu.Unlock()
			return nil
		}
		// Tee into the scrollback ring buffer (so a future
		// reattaching shell can replay them) and forward to the
		// currently-attached shell, if any.
		b.shellMu.Lock()
		if b.buf != nil {
			b.buf.Write(f.Payload)
		}
		sh := b.shell
		b.shellMu.Unlock()
		if sh == nil {
			// Shell detached — bytes already captured in the buffer;
			// the next attached shell will replay them.
			return nil
		}
		return sh.WriteRawFrame(f.Channel, f.Payload)
	}
}

func (inst *AppInstance) handleCtrl(payload []byte) error {
	msg, err := wire.DecodeCtrl(payload)
	if err != nil {
		return fmt.Errorf("ctrl decode: %w", err)
	}
	switch m := msg.(type) {
	case wire.ChannelOpen:
		return inst.handleChannelOpen(m)
	case wire.ChannelClose:
		// If the closing channel was the bundle uploader, the
		// bundle is now complete — flip bundleReady and fan it
		// out to every currently-attached shell.
		inst.bundleMu.Lock()
		isBundle := inst.bundleChannel == m.ChannelID && inst.bundleReady != nil
		if isBundle {
			select {
			case <-inst.bundleReady:
				// already closed
			default:
				close(inst.bundleReady)
			}
		}
		inst.bundleMu.Unlock()
		inst.router.closeChannel(m.ChannelID, "app requested close")
		if isBundle {
			inst.router.fanOutBundleToAttachedShells(inst)
		}
		return nil
	case wire.Error:
		inst.router.log("app %s: error code=%s: %s", inst.AppID, m.Code, m.Msg)
		return nil
	}
	// Other ctrl messages on app side are not expected post-handshake.
	inst.router.log("app %s: unexpected ctrl msg %T", inst.AppID, msg)
	return nil
}

// handleChannelOpen processes the app's channel.open request: validates
// the window belongs to this app (kiosk root counts), allocates an id,
// records the binding, tells both sides.
func (inst *AppInstance) handleChannelOpen(m wire.ChannelOpen) error {
	// Bundle channels are special — they're an upload-only pipe
	// from the SDK to the router used during/right after handshake
	// to ship the embedded bundle. No shell forwarding; bytes go
	// straight into inst.bundleBytes. The router replays them to
	// every (re)attaching shell on a per-shell channel.
	if m.Kind == wire.ChannelKindBundle {
		id := inst.router.allocChannelID()
		b := &channelBinding{
			channelID: id,
			app:       inst,
			windowID:  inst.WindowID,
			kind:      wire.ChannelKindBundle,
		}
		inst.router.registerChannel(b)
		inst.bundleMu.Lock()
		inst.bundleChannel = id
		if inst.bundleReady == nil {
			inst.bundleReady = make(chan struct{})
		}
		inst.bundleMu.Unlock()
		return inst.writeCtrl(wire.NewChannelOpened(m.ReqID, id))
	}
	// For kiosk / desktop-surface apps the app has WindowID=0; the
	// app must request windowID=0 as well (we treat that as "the
	// root surface of this app"). For windowed apps, the requested
	// window must match the app's window.
	if m.WindowID != inst.WindowID {
		return inst.writeCtrl(wire.NewChannelOpenErr(m.ReqID, wire.ErrCodeForbidden, "window not owned by app"))
	}
	// v0.1: one shell. Pick it (any). For multi-shell we'd need the
	// app to specify, or open one channel per shell.
	shells := inst.router.shellList()
	if len(shells) == 0 {
		return inst.writeCtrl(wire.NewChannelOpenErr(m.ReqID, wire.ErrCodeInternal, "no shell attached"))
	}
	shell := shells[0]
	id := inst.router.allocChannelID()
	b := &channelBinding{
		channelID: id,
		app:       inst,
		shell:     shell,
		windowID:  m.WindowID,
		buf:       newRingBuffer(ChannelScrollbackBytes),
	}
	inst.router.registerChannel(b)
	if err := shell.WriteCtrl(wire.NewShellChannelBind(id, m.WindowID)); err != nil {
		inst.router.closeChannel(id, "shell bind failed")
		return inst.writeCtrl(wire.NewChannelOpenErr(m.ReqID, wire.ErrCodeInternal, err.Error()))
	}
	return inst.writeCtrl(wire.NewChannelOpened(m.ReqID, id))
}

func (inst *AppInstance) handleEvt(payload []byte) error {
	t, err := wire.PeekEvtType(payload)
	if err != nil {
		return fmt.Errorf("evt peek: %w", err)
	}
	switch t {
	case wire.TEvtAppMsg:
		var m wire.EvtAppMsg
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayAppMsgToShell(m)
	case wire.TEvtAppMsgSendTo:
		var m wire.EvtAppMsgSendTo
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayAppMsgCrossInstance(m)
	case wire.TEvtWindowSetTitle:
		var m wire.EvtWindowSetTitle
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayWindowTitle(m)
	case wire.TEvtWindowConfirmClose:
		var m wire.EvtWindowConfirmClose
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		inst.deliverCloseConfirm(m.Allow)
		return nil
	case wire.TEvtSpawnRequest:
		var m wire.EvtSpawnRequest
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleSpawnRequest(m)
	case wire.TEvtNotify:
		var m wire.EvtNotify
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.relayNotify(m)
	case wire.TEvtClipboardSet:
		var m wire.EvtClipboardSet
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleClipboardSet(m)
	case wire.TEvtClipboardGet:
		var m wire.EvtClipboardGet
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		return inst.handleClipboardGet(m)
	case wire.TEvtAppStateSet:
		var m wire.EvtAppStateSet
		if err := cbor.Unmarshal(payload, &m); err != nil {
			return err
		}
		// State is JSON bytes; pass straight through. RawMessage
		// validates that it's well-formed JSON; on bad bytes we
		// drop silently — apps that mis-encode get a no-op.
		var state json.RawMessage = m.State
		if !json.Valid(state) {
			inst.router.log("app %s: app_state.set with invalid JSON, dropping", inst.AppID)
			return nil
		}
		inst.router.log("app_state.set instance=%s bytes=%d", inst.InstanceID, len(state))
		inst.router.broadcastPatches(inst.router.winSession.setAppState(inst.InstanceID, state))
		return nil
	}
	inst.router.log("app %s: unexpected evt %q", inst.AppID, t)
	return nil
}

// relayAppMsgCrossInstance forwards data to a different running
// instance as that instance's normal EvtAppMsg event. v0.1 trust
// model has no capability gate — anyone can address anyone, single-
// user assumption. The router still validates the recipient
// (existence, singleton-vs-sentinel rules) and logs failures.
func (inst *AppInstance) relayAppMsgCrossInstance(m wire.EvtAppMsgSendTo) error {
	target, _, err := inst.router.resolveRecipient(context.Background(), m.Recipient)
	if err != nil {
		inst.router.log("app %s app_msg.send.to: %v", inst.AppID, err)
		return nil
	}
	return target.WriteEvt(wire.NewEvtAppMsg(target.WindowID, m.Data))
}

// relayAppMsgToShell forwards an APP_MSG from BE to its FE half.
// CBOR-decoded values (often map[any]any) are converted to a JSON-
// friendly shape so the shell can decode without a CBOR runtime.
// Side effect: if the normalized data is a map carrying a string
// "id" field, any control-socket watcher registered for (instance,
// id) gets delivered the data before shell relay. This is how the
// `wash-launch msg --await` path correlates BE replies.
func (inst *AppInstance) relayAppMsgToShell(m wire.EvtAppMsg) error {
	normalized := toJSON(m.Data)
	if asMap, ok := normalized.(map[string]any); ok {
		if msgID, _ := asMap["id"].(string); msgID != "" {
			inst.router.dispatchAppMsgWatchers(inst.InstanceID, msgID, asMap)
		}
	}
	dataJSON, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal app_msg data: %w", err)
	}
	send := wire.NewShellAppMsgDeliver(inst.InstanceID, dataJSON)
	for _, s := range inst.router.shellList() {
		if err := s.WriteCtrl(send); err != nil {
			return err
		}
	}
	return nil
}

// toJSON walks a CBOR-decoded value and produces a JSON-marshalable
// version. Maps with non-string keys (CBOR allows any) are
// stringified; byte slices become base64 strings.
func toJSON(v any) any {
	switch x := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprint(k)
			}
			out[ks] = toJSON(vv)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = toJSON(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = toJSON(vv)
		}
		return out
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	}
	return v
}

// relayNotify forwards an app's notification to every attached shell.
// v0.1 is open — any app can notify, no capability gate.
func (inst *AppInstance) relayNotify(m wire.EvtNotify) error {
	out := wire.NewShellNotify(inst.InstanceID, m.Title, m.Body, m.Level)
	for _, s := range inst.router.shellList() {
		if err := s.WriteCtrl(out); err != nil {
			return err
		}
	}
	return nil
}

func (inst *AppInstance) relayWindowTitle(m wire.EvtWindowSetTitle) error {
	if m.Win != inst.WindowID {
		return nil
	}
	inst.router.broadcastPatches(inst.router.winSession.setTitle(m.Win, m.Title))
	return nil
}

// handleSpawnRequest enforces the spawn capability and forks a new
// child if the request is allowed.
func (inst *AppInstance) handleSpawnRequest(m wire.EvtSpawnRequest) error {
	if !inst.Manifest.HasCapability(CapSpawn) {
		return inst.WriteEvt(wire.NewEvtSpawnErr(m.AppID, wire.ErrCodeForbidden, "spawn capability not declared"))
	}
	target := inst.router.reg.ByID(m.AppID)
	if target == nil || !target.Enabled() {
		return inst.WriteEvt(wire.NewEvtSpawnErr(m.AppID, wire.ErrCodeNotFound, "unknown app id"))
	}
	if target.Manifest.ProtocolVersion != ProtocolVersion {
		return inst.WriteEvt(wire.NewEvtSpawnErr(m.AppID, wire.ErrCodeIncompatibleProtocol, "protocol mismatch"))
	}
	// Spawn in a goroutine so we don't block this app's read loop on
	// the child's handshake.
	go inst.router.spawnChild(target, inst)
	return nil
}

// spawnChild execs target and runs HandleApp on the resulting transport.
// On success, sends EvtSpawnOk to the requester; on failure,
// EvtSpawnErr. Backed by the shared spawnAndRun.
func (r *Router) spawnChild(target *Entry, requester *AppInstance) {
	inst, err := r.spawnAndRun(context.Background(), target, false)
	if err != nil {
		r.log("spawn %s: %v", target.Manifest.ID, err)
		_ = requester.WriteEvt(wire.NewEvtSpawnErr(target.Manifest.ID, wire.ErrCodeInternal, err.Error()))
		return
	}
	_ = requester.WriteEvt(wire.NewEvtSpawnOk(target.Manifest.ID, inst.InstanceID))
}

// requestClose initiates the X-style close handshake (WIRE.md §10).
// Returns when the app confirms (allow=true/false) or grace expires
// (force-close).
func (inst *AppInstance) requestClose(ctx context.Context) (allowed bool, err error) {
	inst.closeMu.Lock()
	if inst.closeConfirm != nil {
		inst.closeMu.Unlock()
		return false, errors.New("close already in progress")
	}
	ch := make(chan bool, 1)
	inst.closeConfirm = ch
	inst.closeMu.Unlock()

	defer func() {
		inst.closeMu.Lock()
		inst.closeConfirm = nil
		inst.closeMu.Unlock()
	}()

	if err := inst.WriteEvt(wire.NewEvtWindowCloseRequested(inst.WindowID)); err != nil {
		return false, err
	}
	timer := time.NewTimer(closeGrace)
	defer timer.Stop()
	select {
	case allow := <-ch:
		return allow, nil
	case <-timer.C:
		// Force-kill per §10. Subsequent loop iteration will tear down.
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			_ = inst.Cmd.Process.Kill()
		}
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (inst *AppInstance) deliverCloseConfirm(allow bool) {
	inst.closeMu.Lock()
	ch := inst.closeConfirm
	inst.closeMu.Unlock()
	if ch != nil {
		select {
		case ch <- allow:
		default:
		}
	}
}

// writeCtrl encodes m as JSON and writes a control-channel frame.
func (inst *AppInstance) writeCtrl(m any) error {
	data, err := wire.EncodeCtrl(m)
	if err != nil {
		return err
	}
	return inst.writeFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data})
}

// WriteEvt encodes m as CBOR and writes an event-channel frame.
func (inst *AppInstance) WriteEvt(m any) error {
	data, err := wire.EncodeEvt(m)
	if err != nil {
		return err
	}
	return inst.writeFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelEvent, Payload: data})
}

func (inst *AppInstance) writeFrame(f wire.Frame) error {
	inst.writeMu.Lock()
	defer inst.writeMu.Unlock()
	return inst.Transport.WriteFrame(f)
}

// writeRawFrame forwards bare bytes back to the app on a dynamic
// channel. Mirror of ShellSession.WriteRawFrame.
func (inst *AppInstance) writeRawFrame(channelID uint32, payload []byte) error {
	return inst.writeFrame(wire.Frame{Flags: wire.FlagEnd, Channel: channelID, Payload: payload})
}
