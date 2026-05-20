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

	pmu     sync.Mutex
	pending map[uint64]*pendingAsset // by per-app asset id

	// awaiting close confirmation. nil when no close is pending.
	closeMu      sync.Mutex
	closeConfirm chan bool
}

// pendingAsset is the per-asset state the router tracks while a
// bundle file is in flight from this app back to the shell.
type pendingAsset struct {
	ShellInstance string // the originating ShellAssetFetch's instance_id
	Name          string
	MIME          string
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
		pending:   make(map[uint64]*pendingAsset),
	}
	if err := inst.handshake(ctx); err != nil {
		_ = t.Close()
		return fmt.Errorf("handshake %s: %w", manifest.ID, err)
	}
	r.registerApp(inst)
	if err := r.declareAppToAllShells(ctx, inst); err != nil {
		r.log("declare to shell: %v", err)
	}
	// Tell the app its window is mapped (if it has one). For
	// surface=desktop, no window event.
	if inst.WindowID != 0 {
		_ = inst.WriteEvt(wire.NewEvtWindowMapped(inst.WindowID))
	}
	err := inst.loop(ctx)
	r.unregisterApp(inst)
	// Tell shells the window is gone.
	if inst.WindowID != 0 {
		for _, s := range r.shellList() {
			_ = s.WriteCtrl(wire.NewShellWindowDestroy(inst.WindowID))
		}
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
		// v0.0 channels ≥ 2 are reserved; drop with a log.
		inst.router.log("app %s: drop frame on reserved channel %d", inst.AppID, f.Channel)
		return nil
	}
}

func (inst *AppInstance) handleCtrl(payload []byte) error {
	msg, err := wire.DecodeCtrl(payload)
	if err != nil {
		return fmt.Errorf("ctrl decode: %w", err)
	}
	switch m := msg.(type) {
	case wire.AssetReadOK:
		inst.setPendingMIME(m.ID, m.MIME)
		return nil
	case wire.AssetData:
		return inst.deliverAssetChunk(m)
	case wire.AssetReadErr:
		inst.router.log("app %s: asset.read.err id=%d code=%s: %s", inst.AppID, m.ID, m.Code, m.Msg)
		inst.closePending(m.ID)
		return nil
	case wire.Error:
		inst.router.log("app %s: error code=%s: %s", inst.AppID, m.Code, m.Msg)
		return nil
	}
	// Other ctrl messages on app side are not expected post-handshake.
	inst.router.log("app %s: unexpected ctrl msg %T", inst.AppID, msg)
	return nil
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
	}
	inst.router.log("app %s: unexpected evt %q", inst.AppID, t)
	return nil
}

// relayAppMsgToShell forwards an APP_MSG from BE to its FE half.
// CBOR-decoded values (often map[any]any) are converted to a JSON-
// friendly shape so the shell can decode without a CBOR runtime.
func (inst *AppInstance) relayAppMsgToShell(m wire.EvtAppMsg) error {
	dataJSON, err := json.Marshal(toJSON(m.Data))
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
	msg := wire.NewShellWindowTitle(m.Win, m.Title)
	for _, s := range inst.router.shellList() {
		if err := s.WriteCtrl(msg); err != nil {
			return err
		}
	}
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
// EvtSpawnErr.
func (r *Router) spawnChild(target *Entry, requester *AppInstance) {
	cmd, parent, err := Spawn(target.Path, target.Manifest.ID, "", nil)
	if err != nil {
		r.log("spawn %s: %v", target.Manifest.ID, err)
		_ = requester.WriteEvt(wire.NewEvtSpawnErr(target.Manifest.ID, wire.ErrCodeInternal, err.Error()))
		return
	}
	t := NewStreamTransport(parent)
	// We need the new instance's id before reporting success. Spawn
	// the HandleApp goroutine and have it signal back. The simplest
	// scheme: do handshake here synchronously to get the instance id,
	// then hand off to a loop goroutine.
	inst := &AppInstance{
		Transport: t,
		AppID:     target.Manifest.ID,
		Manifest:  target.Manifest,
		Cmd:       cmd,
		router:    r,
		pending:   make(map[uint64]*pendingAsset),
	}
	if err := inst.handshake(context.Background()); err != nil {
		_ = t.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		r.log("spawn handshake %s: %v", target.Manifest.ID, err)
		_ = requester.WriteEvt(wire.NewEvtSpawnErr(target.Manifest.ID, wire.ErrCodeInternal, err.Error()))
		return
	}
	r.registerApp(inst)
	if err := r.declareAppToAllShells(context.Background(), inst); err != nil {
		r.log("declare: %v", err)
	}
	if inst.WindowID != 0 {
		_ = inst.WriteEvt(wire.NewEvtWindowMapped(inst.WindowID))
	}
	_ = requester.WriteEvt(wire.NewEvtSpawnOk(target.Manifest.ID, inst.InstanceID))
	// Run the loop until the child exits.
	go func() {
		err := inst.loop(context.Background())
		if err != nil {
			r.log("app %s loop: %v", inst.AppID, err)
		}
		r.unregisterApp(inst)
		if inst.WindowID != 0 {
			for _, s := range r.shellList() {
				_ = s.WriteCtrl(wire.NewShellWindowDestroy(inst.WindowID))
			}
		}
		_ = t.Close()
		_ = cmd.Wait()
	}()
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

// pending-asset bookkeeping ---------------------------------------------------

func (inst *AppInstance) addPending(id uint64, p *pendingAsset) {
	inst.pmu.Lock()
	inst.pending[id] = p
	inst.pmu.Unlock()
}

func (inst *AppInstance) setPendingMIME(id uint64, mime string) {
	inst.pmu.Lock()
	if p, ok := inst.pending[id]; ok {
		p.MIME = mime
	}
	inst.pmu.Unlock()
}

func (inst *AppInstance) closePending(id uint64) {
	inst.pmu.Lock()
	delete(inst.pending, id)
	inst.pmu.Unlock()
}

func (inst *AppInstance) getPending(id uint64) *pendingAsset {
	inst.pmu.Lock()
	defer inst.pmu.Unlock()
	if p, ok := inst.pending[id]; ok {
		// return a copy to avoid races on caller writes
		cp := *p
		return &cp
	}
	return nil
}

// deliverAssetChunk translates AssetData into ShellAssetDeliver and
// pushes it to every attached shell. On end=true the pending entry is
// removed.
func (inst *AppInstance) deliverAssetChunk(m wire.AssetData) error {
	pa := inst.getPending(m.ID)
	if pa == nil {
		inst.router.log("app %s: asset.data for unknown id %d (ignored)", inst.AppID, m.ID)
		return nil
	}
	out := wire.NewShellAssetDeliver(inst.InstanceID, pa.Name, m.Bytes, m.End, pa.MIME)
	for _, s := range inst.router.shellList() {
		if err := s.WriteCtrl(out); err != nil {
			return err
		}
	}
	if m.End {
		inst.closePending(m.ID)
	}
	return nil
}

// requestAsset is called by the shell session to ask this app for a
// bundle file. It allocates an asset id, records the request, and
// emits AssetRead on the app's channel 0.
func (inst *AppInstance) requestAsset(name string, fromInstance string) error {
	id := inst.router.allocAssetID()
	inst.addPending(id, &pendingAsset{ShellInstance: fromInstance, Name: name})
	return inst.writeCtrl(wire.NewAssetRead(id, name))
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
