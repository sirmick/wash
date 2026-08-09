package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/sirmick/wash/pkg/wire"
)

// Channel ids — must match router and WIRE.md §3.
const (
	channelControl = 0
	channelEvent   = 1
)

// AppDef is the app's static definition supplied to Main.
//
// All callbacks may be nil. They run on the SDK's goroutine, one at a
// time; do not block them on the SDK (use a worker goroutine for slow
// work, then call back in to the SDK to send messages).
type AppDef struct {
	// Manifest is the app's static manifest, printed verbatim by
	// --wash-manifest.
	Manifest Manifest

	// Assets is the embedded FS that backs asset.read requests
	// (WIRE.md §7). nil means "no assets to serve" — useful for
	// trivial test programs.
	Assets fs.FS

	// OnReady fires once after handshake.ack with the assigned
	// instance and (if any) window id.
	OnReady func(c *Conn, instanceID string, windowID uint32)

	// Lifecycle / window events.
	OnMapped  func(c *Conn, win uint32)
	OnFocus   func(c *Conn, win uint32)
	OnUnfocus func(c *Conn, win uint32)
	OnResize  func(c *Conn, win uint32, w, h uint32)
	OnState   func(c *Conn, win uint32, state string) // "normal" | "minimized" | "maximized"

	// OnCloseRequested is the X-style WM_DELETE analogue. Return true
	// to allow close; return false to veto. If nil, the SDK confirms
	// close (allow=true) automatically — sensible default for v0.0.
	OnCloseRequested func(c *Conn, win uint32) bool

	// OnAppMsg is the FE → BE pipe for this app's own halves. data is
	// the JSON-decoded payload — map[string]any for objects, []any
	// for arrays, scalars for primitives. The SDK leaves further
	// typed-decode to the app since the shape is app-private.
	//
	// Called only when no From envelope is present — i.e. the message
	// arrived from this app's own FE, not from another app. Cross-app
	// deliveries go to OnAppMsgFrom; if OnAppMsgFrom is nil, OnAppMsg
	// also receives cross-app deliveries (with no way to learn the
	// sender) so existing apps keep working unchanged.
	OnAppMsg func(c *Conn, win uint32, data any)

	// OnAppMsgFrom delivers an app_msg sent by *another* instance via
	// SendAppMsgTo. The sender is router-attested — populated from
	// the routing envelope, never from the payload. Use this in apps
	// that act on cross-app requests (wash-priv, wash-bulk-style
	// services) where the requester's identity matters.
	OnAppMsgFrom func(c *Conn, win uint32, data any, from wire.Sender)

	// OnInstanceGone fires after the router tears down another app
	// instance. Long-lived services use this to remove subscriptions
	// addressed to that instance while keeping their own server-side
	// state intact.
	OnInstanceGone func(c *Conn, appID, instanceID string)

	// OnSpawnResult delivers the router's reply to a SpawnRequest.
	// instanceID is non-empty on success; err is non-nil on failure.
	OnSpawnResult func(c *Conn, appID, instanceID string, err error)

	// OnPrepareSpawnResult delivers the router's reply to a
	// PrepareSpawn call. On success, the app receives the minted
	// instance id, the attach token to pass through to the child
	// (via WASH_ATTACH_TOKEN env), and the registered binary path
	// to exec. On failure, err is non-nil and other fields are zero.
	// Used by wash-priv to drive an external (sudo) spawn while the
	// router still tracks lifecycle.
	OnPrepareSpawnResult func(c *Conn, reqID uint64, instanceID, attachToken, binary string, err error)

	// OnClipboardChanged fires when another app set the clipboard.
	// mime is the new content type. Apps that care should follow up
	// with ClipboardGet.
	OnClipboardChanged func(c *Conn, mime string)
}

// Conn is the live connection state for one app process.
type Conn struct {
	transport wire.FrameTransport
	def       *AppDef

	instanceID string
	windowID   uint32
	session    wire.Session

	// channels is the live raw channel registry. Keyed by router-
	// allocated channel id. Mutated only by dispatch + OpenChannel.
	chanMu   sync.Mutex
	channels map[uint32]*RawChannel

	// droppedChans records channel ids whose bytes arrived after the
	// channel left the registry, so the drop is logged once per id —
	// the close race is normal, a persistent drop is a wiring bug.
	droppedMu    sync.Mutex
	droppedChans map[uint32]bool

	// The req-id-keyed correlation registries below all share the
	// generic pendingCalls helper (see pending.go): register a cap-1
	// waiter under a req id, resolve it once from dispatch, cancel it on
	// any abandon path. nextReqID mints the uint64 keys for every
	// writeEvt-based round-trip.
	nextReqID atomic.Uint64

	// pendingOpens correlates ChannelOpen req_id ↔ the goroutine
	// waiting for the response (in OpenChannel).
	pendingOpens *pendingCalls[uint64, openResult]

	// pendingClipboardGet correlates ClipboardGet req_id with the
	// waiting goroutine in ClipboardGet.
	pendingClipboardGet *pendingCalls[uint64, clipboardResult]

	// pendingIngress correlates PublishIngress req_id with the waiting
	// goroutine. Resolved by dispatch on ingress.published / ingress.err.
	pendingIngress *pendingCalls[uint64, ingressResult]

	// pendingRestart correlates RestartApp req_id with the waiting
	// goroutine. Resolved by dispatch on app.restart.ok / app.restart.err.
	pendingRestart *pendingCalls[uint64, restartResult]

	// pendingWindowCreate correlates CreateWindow req_id with the
	// waiting goroutine. Resolved by dispatch on window.created /
	// window.create.err. See docs/DISPLAY.md §4.
	pendingWindowCreate *pendingCalls[uint64, windowCreateResult]

	// privPending tracks in-flight PrivRunInlineSync calls keyed by
	// req_id. dispatchEvt intercepts incoming app_msgs from com.wash
	// .priv whose req_id matches a pending call, accumulates the
	// stream bytes, and resolves on priv.result. Non-matching priv
	// messages flow through to OnAppMsgFrom as normal.
	privMu      sync.Mutex
	privPending map[string]*privCall

	// done is closed exactly once when Close runs. App-side goroutines
	// can select on Done() to tie themselves to connection lifetime
	// without threading a context through every callback.
	closeOnce sync.Once
	done      chan struct{}
}

type clipboardResult struct {
	mime string
	data []byte
	err  error
}

type ingressResult struct {
	path string
	err  error
}

type restartResult struct {
	instanceID string
	err        error
}

type openResult struct {
	ch  *RawChannel
	err error
}

// InstanceID returns the router-assigned instance id (empty until
// after handshake).
func (c *Conn) InstanceID() string { return c.instanceID }

// WindowID returns the router-assigned window id; zero for
// surface=desktop apps.
func (c *Conn) WindowID() uint32 { return c.windowID }

// Session returns the router-supplied session bag set at handshake
// time. Fields default to the zero value when the router didn't ship
// one (e.g. test harnesses), so apps can call Session().Root and get
// "" for "unconfined" without nil-checking.
func (c *Conn) Session() wire.Session { return c.session }

// Manifest exposes the app's manifest.
func (c *Conn) Manifest() *Manifest { return &c.def.Manifest }

// LaunchOpenPath returns the file path the app was launched to open, parsed
// from the `--open <path>` argv the router injects for an open.request (see
// CapOpen / docs/IMAGES.md). Empty when launched normally. Apps that handle
// opens read this in OnReady and drive their FE to that file.
func (c *Conn) LaunchOpenPath() string {
	args := os.Args
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "--open" {
			return args[i+1]
		}
	}
	return ""
}

// Main is the canonical entrypoint for a wash app.
//
//   - If invoked as `<binary> --wash-manifest`, prints the manifest as
//     JSON to stdout and exits 0 (WIRE.md §5).
//   - Otherwise: adopts fd 3, runs the handshake, dispatches events
//     until the socket closes, then returns. On error it prints to
//     stderr and exits non-zero.
//
// Main installs no signal handler unless something asks for one: an app
// that spawns a child tree registers cleanup with OnTerminate (see
// terminate.go), and a coverage run installs the handler to flush -cover
// counters. With neither, the OS sends SIGTERM at router shutdown and the
// SDK lets the runtime tear the process down naturally.
func Main(def *AppDef) {
	if maybePrintManifest(def) {
		return
	}
	installCoverageFlushOnSignal()
	if err := Run(context.Background(), def); err != nil {
		fatal("wash sdk: %v", err)
	}
}

// Run is the post-handshake event loop: connect, dispatch frames
// until the socket closes, and close cleanly. It returns nil on a
// normal shutdown and an error otherwise — callers that need to
// distinguish probe vs run wrap this with their own --wash-manifest
// dispatch (Main does it for standalone binaries; the multi-call
// dispatcher does it for its argv[0] cases).
func Run(ctx context.Context, def *AppDef) error {
	c, err := Connect(def)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer c.Close()
	// Run any registered terminate hooks on the way out. The signal handler
	// (OnTerminate) covers a router-driven SIGTERM, but a router that crashes
	// or restarts just closes our connection — Run returns, no signal — and an
	// app that spawns a child tree (wash-vscode → code-server) would otherwise
	// leak it. runTerminateHooks is run-once, so this and the signal path don't
	// double-fire.
	defer runTerminateHooks()
	// Heartbeat: ships runtime.stats every few seconds to the
	// router for the About panel. ctx-bound so it dies when Run
	// returns.
	hbCtx, cancelHB := context.WithCancel(ctx)
	defer cancelHB()
	c.startHeartbeat(hbCtx)
	if err := c.Run(ctx); err != nil && !errors.Is(err, ErrConnClosed) {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}

func maybePrintManifest(def *AppDef) bool {
	// Router's probe invokes `<binary> --wash-manifest` with no other
	// args (WIRE.md §5). Match only the conventional position.
	if len(os.Args) < 2 || os.Args[1] != "--wash-manifest" {
		return false
	}
	// Emit framed probe output: the manifest header line followed by
	// the embedded FE bundle(s) as raw bytes (wire.WriteProbe). The
	// router caches them at scan time — no post-handshake upload step,
	// no base64.
	var bundles []wire.NamedBundle
	if def.Assets != nil {
		if b, err := readEmbeddedBundle(def.Assets, "index.js"); err == nil {
			bundles = append(bundles, wire.NamedBundle{Kind: wire.BundleMain, Bytes: b})
		}
		// On read failure we still ship the manifest. The router lists
		// the app disabled with a "missing bundle" reason when a shell
		// tries to mount it; a malformed build is the operator's
		// problem, not the probe's.
		if def.Manifest.SettingsPanel != nil {
			if b, err := readEmbeddedBundle(def.Assets, "panel.js"); err == nil {
				bundles = append(bundles, wire.NamedBundle{Kind: wire.BundlePanel, Bytes: b})
			}
		}
	}
	if err := wire.WriteProbe(os.Stdout, def.Manifest, bundles); err != nil {
		fatal("wash sdk: write manifest: %v", err)
	}
	return true
}

// readEmbeddedBundle pulls a named bundle (index.js / panel.js) out of
// the app's embedded FS as raw bytes for the probe payload.
func readEmbeddedBundle(fsys fs.FS, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// EnvDisplay is the env var that points apps at a running
// router's wash socket. Router-spawned apps inherit it; programs
// run from a terminal (or anywhere else) can attach by setting it
// themselves. Analogous to X11's DISPLAY.
const EnvDisplay = "WASH_DISPLAY"

// ErrConnClosed is returned when the transport has closed cleanly.
var ErrConnClosed = errors.New("wash sdk: connection closed")

// Connect dials the wash socket pointed to by WASH_DISPLAY,
// performs the handshake, and returns a ready Conn. Closing the
// returned Conn closes the socket.
//
// The handshake's Identity message carries os.Getpid(); the
// router validates that against /proc/<pid>/exe matching the
// registered binary for AppID, so a random binary can't
// impersonate a registered app.
func Connect(def *AppDef) (*Conn, error) {
	if def == nil {
		return nil, errors.New("AppDef is nil")
	}
	display := os.Getenv(EnvDisplay)
	if display == "" {
		return nil, fmt.Errorf("%s not set (run via the router or set %s=/path/to/wash.sock)", EnvDisplay, EnvDisplay)
	}
	conn, err := net.Dial("unix", display)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", display, err)
	}
	// Mark the socket CLOEXEC so anything this app forks (a shell
	// in wash-term, a subprocess in any app) does NOT inherit it.
	// Without this an arbitrary command run in the app's shell
	// could speak the wash protocol on this app's connection,
	// clobbering it.
	if uc, ok := conn.(*net.UnixConn); ok {
		if f, ferr := uc.File(); ferr == nil {
			syscall.CloseOnExec(int(f.Fd()))
			// Don't close f — File() dup'd the fd; we want the
			// dup alive only long enough to flip CLOEXEC. Close
			// it now; the original conn fd retains the flag.
			_ = f.Close()
		}
	}
	t := wire.NewStreamTransport(conn)
	return ConnectWith(t, def)
}

// ConnectWith uses a caller-supplied transport instead of fd 3. The
// in-process loopback test (C8) and the SDK's own tests use this.
func ConnectWith(t wire.FrameTransport, def *AppDef) (*Conn, error) {
	c := &Conn{
		transport:           t,
		def:                 def,
		channels:            make(map[uint32]*RawChannel),
		pendingOpens:        newPendingCalls[uint64, openResult](),
		pendingClipboardGet: newPendingCalls[uint64, clipboardResult](),
		pendingIngress:      newPendingCalls[uint64, ingressResult](),
		pendingRestart:      newPendingCalls[uint64, restartResult](),
		pendingWindowCreate: newPendingCalls[uint64, windowCreateResult](),
		done:                make(chan struct{}),
	}
	if err := c.handshake(); err != nil {
		_ = t.Close()
		return nil, err
	}
	if def.OnReady != nil {
		def.OnReady(c, c.instanceID, c.windowID)
	}
	// No post-handshake bundle upload: the router already has the
	// embedded FE bundle bytes from the --wash-manifest probe and
	// streams them straight to attached shells. See wire.ProbeHeader
	// + router.Registry.Entry.Bundle.
	return c, nil
}

// Close closes the transport. Idempotent. Also closes the Done()
// channel so app goroutines tied to the connection exit.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return c.transport.Close()
}

// Done returns a channel that is closed when the connection is torn
// down (Close called, or Run returned because the transport closed).
// App goroutines started in OnReady should select on this so they
// exit with the connection — without it, multi-instance apps leak a
// goroutine per closed window.
func (c *Conn) Done() <-chan struct{} { return c.done }

// handshake sends identity (with pid for router-side auth) and
// reads identity.ack.
//
// If WASH_ATTACH_TOKEN is set, the SDK adds it to the identity. The
// router uses the token to match the dial-back to a pending record
// minted via EvtSpawnRequest (with Prepare=true) — required when this process was forked
// by something other than the router itself (e.g. wash-priv → sudo).
func (c *Conn) handshake() error {
	var ident wire.Identity
	if tok := os.Getenv("WASH_ATTACH_TOKEN"); tok != "" {
		ident = wire.NewIdentityWithToken(c.def.Manifest.ID, ProtocolVersion, c.def.Manifest.Version, os.Getpid(), tok)
	} else {
		ident = wire.NewIdentityWithPID(c.def.Manifest.ID, ProtocolVersion, c.def.Manifest.Version, os.Getpid())
	}
	if err := c.writeCtrl(ident); err != nil {
		return fmt.Errorf("write identity: %w", err)
	}
	f, err := c.transport.ReadFrame()
	if err != nil {
		return fmt.Errorf("read identity.ack: %w", err)
	}
	if f.Channel != channelControl {
		return fmt.Errorf("expected ack on channel %d, got %d", channelControl, f.Channel)
	}
	msg, err := wire.DecodeCtrl(f.Payload)
	if err != nil {
		return fmt.Errorf("decode identity.ack: %w", err)
	}
	switch m := msg.(type) {
	case wire.IdentityAck:
		c.instanceID = m.InstanceID
		c.windowID = m.WindowID
		if m.Session != nil {
			c.session = *m.Session
		}
		return nil
	case wire.Error:
		return fmt.Errorf("router refused: %s (%s)", m.Msg, m.Code)
	}
	return fmt.Errorf("unexpected handshake reply: %T", msg)
}

// writeCtrl encodes m and writes a channel-0 frame.
func (c *Conn) writeCtrl(m any) error {
	b, err := wire.EncodeCtrl(m)
	if err != nil {
		return err
	}
	return c.transport.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: channelControl, Payload: b})
}

// writeEvt encodes m as JSON and writes a channel-1 frame at the
// default class (Interactive). Most call sites are interactive
// (lifecycle, window ops, small RPCs); explicit bulk goes through
// writeEvtClass.
func (c *Conn) writeEvt(m any) error {
	return c.writeEvtClass(m, wire.ClassInteractive)
}

// writeEvtClass is writeEvt with an explicit class for the frame.
// Bulk producers (pty output, large list replies, file streams) go
// through this path so the router's scheduler can prioritize away
// from them when interactive traffic is waiting.
func (c *Conn) writeEvtClass(m any, class wire.Class) error {
	b, err := wire.EncodeEvt(m)
	if err != nil {
		return err
	}
	f := wire.Frame{Flags: wire.FlagEnd, Channel: channelEvent, Payload: b}.WithClass(class)
	return c.transport.WriteFrame(f)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
