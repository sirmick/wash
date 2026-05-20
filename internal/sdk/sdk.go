package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/sirmick/wash/internal/wire"
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
	// whatever the FE sent via app_msg.send; the SDK leaves CBOR
	// decoding to the app since the format is app-private.
	OnAppMsg func(c *Conn, win uint32, data any)

	// OnSpawnResult delivers the router's reply to a SpawnRequest.
	// instanceID is non-empty on success; err is non-nil on failure.
	OnSpawnResult func(c *Conn, appID, instanceID string, err error)

	// OnShutdown fires when the router sends "shutdown". The SDK
	// continues to receive frames until the underlying socket closes.
	OnShutdown func(c *Conn)
}

// Conn is the live connection state for one app process.
type Conn struct {
	transport wire.FrameTransport
	def       *AppDef

	instanceID string
	windowID   uint32

	// channels is the live raw channel registry. Keyed by router-
	// allocated channel id. Mutated only by dispatch + OpenChannel.
	chanMu   sync.Mutex
	channels map[uint32]*RawChannel

	// pendingOpens correlates ChannelOpen req_id ↔ the goroutine
	// waiting for the response (in OpenChannel).
	openMu       sync.Mutex
	pendingOpens map[uint64]chan openResult
	nextReqID    atomic.Uint64
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

// Manifest exposes the app's manifest.
func (c *Conn) Manifest() *Manifest { return &c.def.Manifest }

// Main is the canonical entrypoint for a wash app.
//
//   - If invoked as `<binary> --wash-manifest`, prints the manifest as
//     JSON to stdout and exits 0 (WIRE.md §5).
//   - Otherwise: adopts fd 3, runs the handshake, dispatches events
//     until the socket closes, then returns. On error it prints to
//     stderr and exits non-zero.
//
// Main installs no signal handlers — the OS sends SIGTERM at router
// shutdown, and the SDK lets the runtime tear down naturally.
func Main(def *AppDef) {
	if maybePrintManifest(def) {
		return
	}
	c, err := Connect(def)
	if err != nil {
		fatal("wash sdk: connect: %v", err)
	}
	defer c.Close()
	if err := c.Run(context.Background()); err != nil && !errors.Is(err, ErrConnClosed) {
		fatal("wash sdk: run: %v", err)
	}
}

func maybePrintManifest(def *AppDef) bool {
	for _, a := range os.Args[1:] {
		if a == "--wash-manifest" {
			b, err := json.Marshal(def.Manifest)
			if err != nil {
				fatal("wash sdk: marshal manifest: %v", err)
			}
			os.Stdout.Write(b)
			os.Stdout.Write([]byte("\n"))
			return true
		}
	}
	return false
}

// fdSocket is the inherited descriptor passed by the router (WIRE.md §1).
const fdSocket = 3

// ErrConnClosed is returned when the transport has closed cleanly.
var ErrConnClosed = errors.New("wash sdk: connection closed")

// Connect adopts fd 3, performs the handshake, and returns a ready
// Conn. Closing the returned Conn closes the socket.
func Connect(def *AppDef) (*Conn, error) {
	if def == nil {
		return nil, errors.New("AppDef is nil")
	}
	f := os.NewFile(fdSocket, "wash-router-socket")
	if f == nil {
		return nil, fmt.Errorf("fd %d not present (was this binary spawned by the router?)", fdSocket)
	}
	// Mark the wash socket CLOEXEC so anything this app forks (a
	// shell in wash-term, a subprocess in any app) does NOT inherit
	// it. Without this an arbitrary command run in the app's shell
	// could speak the wash protocol on this app's connection,
	// clobbering it. Go's exec strips CLOEXEC on inherited fds, so
	// the fd we got from the router does not have it.
	syscall.CloseOnExec(int(f.Fd()))
	t := wire.NewStreamTransport(f)
	return ConnectWith(t, def)
}

// ConnectWith uses a caller-supplied transport instead of fd 3. The
// in-process loopback test (C8) and the SDK's own tests use this.
func ConnectWith(t wire.FrameTransport, def *AppDef) (*Conn, error) {
	c := &Conn{
		transport:    t,
		def:          def,
		channels:     make(map[uint32]*RawChannel),
		pendingOpens: make(map[uint64]chan openResult),
	}
	if err := c.handshake(); err != nil {
		_ = t.Close()
		return nil, err
	}
	if def.OnReady != nil {
		def.OnReady(c, c.instanceID, c.windowID)
	}
	return c, nil
}

// Close closes the transport. Idempotent.
func (c *Conn) Close() error {
	return c.transport.Close()
}

// handshake sends identity and reads identity.ack.
func (c *Conn) handshake() error {
	ident := wire.NewIdentity(c.def.Manifest.ID, ProtocolVersion, c.def.Manifest.Version)
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

// writeEvt encodes m as CBOR and writes a channel-1 frame.
func (c *Conn) writeEvt(m any) error {
	b, err := wire.EncodeEvt(m)
	if err != nil {
		return err
	}
	return c.transport.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: channelEvent, Payload: b})
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
