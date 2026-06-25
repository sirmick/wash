package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// Channel IDs (WIRE.md §3). v0.0 fixes them; OPEN-time discipline
// negotiation is post-v0.0.
const (
	ChannelControl = 0 // JSON: handshake/asset on app socket; shell vocabulary on WS
	ChannelEvent   = 1 // JSON: app event channel
)

// Config is the runtime configuration the caller (cmd/wash-router)
// derives from env + flags.
type Config struct {
	Listen       string   // host:port
	AppsDirs     []string // ordered; first occurrence wins
	SessionAppID string   // which manifest is the desktop surface

	// NoSession suppresses the autoboot session app. Combine with
	// InitialAppID to run a single app full-screen ("kiosk" mode).
	NoSession bool

	// InitialAppID is the app spawned on first shell connect (instead
	// of, or alongside, the session). Its FE element mounts at the
	// root surface regardless of its manifest's declared surface.
	InitialAppID string

	// ShowHidden includes apps marked manifest.Hidden in the catalog
	// the shell sees. Off by default — used by e2e tests and
	// debugging to surface the test app in the chrome launcher.
	ShowHidden bool

	// ControlSocket is the Unix-socket path the router listens on for
	// the wash-launch CLI and similar tools. Empty disables. The
	// router exports this as WASH_CONTROL_SOCKET in the env of every
	// app it spawns, so children (including shells in wash-term) can
	// invoke launches without re-discovering it.
	ControlSocket string

	// ScreenshotDir is where POST /screenshot writes PNG files. Empty
	// disables the endpoint. Files are named with the timestamp at
	// upload time.
	ScreenshotDir string

	// Dev enables the binary-watcher loop: fsnotify on each apps
	// dir + the router binary itself. On change, matching app
	// instances are killed and shells are told to reload; a
	// router-binary change triggers self re-exec. Off in
	// production; on for `wash-router --dev` or dev-restart.sh.
	Dev bool

	// FSRoot is the filesystem sandbox the router ships to every
	// app in the handshake's Session bag. Empty means unconfined.
	// Apps that touch the filesystem (wash-fm, wash-fs) treat all
	// paths as relative to this prefix when non-empty.
	FSRoot string

	// ListenUnix, if non-empty, switches the router from the legacy
	// TCP/HTTP+WS listener (cfg.Listen) into multi-user mode: it
	// binds a Unix socket at this path and accepts SCM_RIGHTS
	// handoffs from wash-login. Each accepted handoff carries a
	// browser-facing TCP fd plus the buffered HTTP-upgrade request
	// bytes; the router runs the WS upgrade itself and attaches
	// the resulting WebSocket as a shell view. See docs/MULTIUSER.md.
	ListenUnix string

	// Relayed marks this router as a remote-apps relay ENDPOINT (started
	// with --listen-raw): it exists only to be viewed through another
	// host's shell — the "B" in docs/REMOTE.md — never to front a browser.
	// Every app it spawns is composited remotely, so the remote-family
	// apps (com.wash.connect / com.wash.remote) must NOT run or register
	// peers here: wash is a flat multi-homed shell, never a chain
	// (R2 — "we never chain routers"). Surfaced to spawned apps as
	// WASH_RELAYED=1, and enforced router-side in handlePeerRegister.
	Relayed bool

	// Name is the human-readable session name surfaced in the
	// stat RPC and in /proc/<pid>/cmdline. Informational only;
	// the router never makes routing decisions on it. Immutable
	// once set.
	Name string
	// IdleTimeout is the period of no-attached-shell after which
	// the router self-exits. Zero disables idle reaping. Default
	// applied by the runner is 30 minutes when ListenUnix is set.
	IdleTimeout time.Duration
	// AllowUID is the uid whose handoffs the SCM_RIGHTS listener
	// accepts (verified via SO_PEERCRED on the ctl socket). Zero
	// defaults to the router's own uid (the normal single-tenant
	// case where wash-login runs alongside under one system
	// account). Multi-tenant deployments set this to the
	// wash-system uid that owns wash-login.
	AllowUID uint32

	// AuthToken gates the TCP/HTTP listener (cfg.Listen / --transport=ws):
	// GET /, /ws and /screenshot require this token, presented as a
	// ?token= query (which is then stamped into the wash_router cookie)
	// or the cookie itself. Empty disables the gate — correct for the
	// unix-socket (--listen-unix, OS-perm gated) and byte-stream
	// (virtio/serial/fd, device-owned) paths, which never expose an
	// open TCP port. The standalone ws router generates one at startup
	// unless --no-auth is set. The token is the credential: it proves
	// the caller is the operator who launched the router (Jupyter-style),
	// not a network stranger — there's no PAM here because the router
	// already runs as one fixed user.
	AuthToken string

	// AllowCrossOrigin relaxes the /ws same-origin check so a browser can
	// open a shell connection to this router from a *different* origin.
	// Needed for remote apps (docs/REMOTE.md R2): the user's desktop is
	// served by router A, but its shell opens a second connection to
	// router B (reached over an ssh -L tunnel), whose Host differs from
	// the page's Origin. Only set on a router intended to be reached
	// cross-origin — it is gated by the SSH tunnel / loopback bind, not by
	// the same-origin policy. The unix-socket/multi-user path already skips
	// the check (it sits behind wash-login).
	AllowCrossOrigin bool

	// HostAllowlist, when non-empty, restricts which Host header values the
	// TCP listener accepts — a DNS-rebinding defense. Empty (the default)
	// is permissive: every Host passes, so existing LAN / mDNS deployments
	// are unaffected. Loopback names (localhost, 127.0.0.1, ::1) and the
	// configured bind host are always accepted; add LAN hostnames here to
	// switch on enforcement. See hostAllowed in security.go.
	HostAllowlist []string
}

// Logger is a minimal sink; cmd/wash-router supplies a real one.
type Logger func(format string, args ...any)

// Router is the central coordinator. Construct with NewRouter, then
// either call Run(ctx) for the full server lifecycle or feed it
// transports directly via HandleApp/HandleShell for tests.
type Router struct {
	cfg    Config
	reg    *Registry
	log    Logger
	assets http.FileSystem // embedded shell-runtime FS; served via TShellAssetRead

	mu     sync.Mutex
	apps   map[string]*AppInstance // by instance id
	byWin  map[uint32]*AppInstance // by window id (0 keys are skipped)
	shells map[*ShellSession]struct{}
	// headShell is the most-recently fully-attached shell — the
	// "foreground" connection. New terminal/raw channels bind to it
	// (instead of an arbitrary shells[0]) and reattach migrates
	// terminal channels to it, so a terminal follows the current
	// connection across reconnects / stacked WS connections. Guarded
	// by mu; nil when no shell is attached. See reattachChannelsToShell.
	headShell *ShellSession

	nextWindow   atomic.Uint32
	nextInstance atomic.Uint64
	nextChannel  atomic.Uint32 // starts at 1, returns 2+ via allocChannelID

	// Link-health session totals (docs/QOS.md). linkTotals accumulates
	// each finished shell connection's counters so the desktop info
	// panel's MB figures and the About screen survive WS reconnects;
	// connectCount is the number of shell connections served; started
	// stamps session (router process) start for the uptime readout.
	linkTotals   *LinkStats
	connectCount atomic.Uint64
	started      time.Time

	// Asset cache (docs/QOS.md): path → cached identity+gzip bytes, so the
	// asset.read path reads + compresses each file once per process rather
	// than on every connection. Keyed by path + stat signature (loadAsset).
	assetCacheMu sync.Mutex
	assetCache   map[string]*assetEntry

	channelsMu sync.Mutex
	channels   map[uint32]*channelBinding

	// peers maps a remote-host origin → the local socket the supervisor
	// (com.wash.remote) ssh -L'd to reach host B's router. A shell
	// peer.attaches an origin and the router splices a channel to this
	// socket (docs/REMOTE.md). Guarded by peersMu.
	peersMu sync.Mutex
	peers   map[string]peerTarget

	// appMsgWatchers lets a non-shell caller (e.g. the control
	// socket's `msg` op) wait for a specific outbound app_msg keyed
	// by the request-id convention in the payload. Stored as
	// instanceID → matchID → one-shot channel.
	appMsgMu       sync.Mutex
	appMsgWatchers map[string]map[string]chan map[string]any

	// singletons maps a singleton manifest's app_id to its currently-
	// running instance, or absent if none. Used to (a) refuse a
	// second spawn of a singleton, and (b) resolve sentinel addresses
	// in EvtAppMsg / ShellAppMsgSend. Mutations always happen
	// while r.mu is held — same lock that guards r.apps — so the
	// lookup stays consistent with the live instance map.
	singletons map[string]*AppInstance

	// pendingAttach tracks router-spawned children awaiting their
	// dial-back. Keyed by pid; the spawn caller blocks on the
	// channel, and the control-socket's attach handler resolves it
	// when the matching Identity arrives. The record carries the
	// kiosk flag so the handler can stamp it onto the inst BEFORE
	// the handshake runs — handshake's WindowID allocation gates on
	// inst.Kiosk, so setting it post-attach would race the FE-bound
	// IdentityAck and surface a phantom window.
	pendingMu     sync.Mutex
	pendingAttach map[int]*pendingAttach

	// pendingByToken tracks dial-backs from externally-spawned
	// children (wash-priv's "spawn under sudo" path). Keyed by the
	// attach token the router minted in EvtPrepareSpawnOk. The
	// record carries the binary path expected at handshake — the
	// same /proc/<pid>/exe check used by pid-matched attaches — and
	// the app_id the token was bound to, so a token leaked to a
	// different app can't be redeemed.
	pendingByToken map[string]*tokenPending

	// cliSessions are control-socket connections promoted to a
	// streaming back-channel for wash-priv inline ops (wash-sudo
	// CLI). Each has a synthetic instance_id of the form "cli-<n>";
	// SendAppMsgTo from any app to that id is routed to the
	// connection by writing the payload as a JSON envelope.
	cliMu       sync.Mutex
	cliSessions map[string]*cliSession

	clipboard clipboardState

	sessionMu sync.Mutex
	session   sessionAppState

	initialMu sync.Mutex
	initial   sessionAppState

	// backgroundMu guards backgroundStarted, the set of background
	// surface app ids EnsureBackgroundAppsRunning has spawned. Each
	// id maps to a per-app start-state so a failed spawn can be
	// retried on the next shell connect without leaking a "started
	// once" sentinel.
	backgroundMu      sync.Mutex
	backgroundStarted map[string]bool

	// publishedEnv holds WASH_*-namespaced env hints an env-publish app
	// (wash-display) has published; spawnEnv merges them into every
	// subsequently-spawned app's environment. See docs/DISPLAY_ENV.md.
	publishedEnvMu sync.Mutex
	publishedEnv   map[string]string

	// windowSession is the canonical state for windows the shell
	// renders. Sent as a snapshot on shell connect; mutated by router
	// or shell actions, with patches broadcast to every shell.
	winSession windowSession

	// runtimeStats is the per-instance Go-runtime self-report from
	// each app's SDK heartbeat. The About panel queries the full
	// table via the com.wash.router synthetic peer. See
	// internal/router/runtime_stats.go.
	statsMu      sync.Mutex
	runtimeStats map[string]*runtimeStatsRecord

	// ingress is the token→backend registry for the generic embed-
	// a-web-app proxy (/app/<token>/*). See ingress.go.
	ingress *ingressRegistry
}

// NewRouter constructs a router; cfg.AppsDirs are expected to already
// be split. The Registry is populated by the caller via Scan before
// Run.
func NewRouter(cfg Config, reg *Registry, log Logger) *Router {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Router{
		cfg:               cfg,
		reg:               reg,
		log:               log,
		apps:              make(map[string]*AppInstance),
		byWin:             make(map[uint32]*AppInstance),
		shells:            make(map[*ShellSession]struct{}),
		channels:          make(map[uint32]*channelBinding),
		appMsgWatchers:    make(map[string]map[string]chan map[string]any),
		singletons:        make(map[string]*AppInstance),
		pendingAttach:     make(map[int]*pendingAttach),
		pendingByToken:    make(map[string]*tokenPending),
		cliSessions:       make(map[string]*cliSession),
		backgroundStarted: make(map[string]bool),
		ingress:           newIngressRegistry(log),
		peers:             make(map[string]peerTarget),
		linkTotals:        &LinkStats{},
		started:           time.Now(),
	}
}

// sessionLinkTotals folds the live connection's snapshot into the banked
// session totals for display (the desktop info panel + About). Banked
// totals come from already-finished connections; live is the current one.
func (r *Router) sessionLinkTotals(live wire.LinkStatsSnapshot) wire.LinkStatsSnapshot {
	return r.linkTotals.snapshot([numClasses]int{}).Plus(live)
}

// SetAssets installs the embedded shell-runtime FS so TShellAssetRead
// handlers can serve files from it. Called by the runner after the
// router is constructed and the embedded FS is resolved.
func (r *Router) SetAssets(fs http.FileSystem) { r.assets = fs }

// tokenPending is a pending-attach record keyed by attach token
// (Identity.AttachToken). Differs from pendingAttach (pid-keyed) in
// that the spawner is the one driving fork+exec — the router only
// validates the eventual dial-back. The record is consumed at
// successful attach OR by the spawner's connection going away
// (orphan tokens are reaped in cleanupPendingByToken).
type tokenPending struct {
	appID      string       // the token may only be redeemed for this app id
	instanceID string       // pre-minted; the attaching child gets this back in IdentityAck
	binary     string       // /proc/<pid>/exe must match this path at attach
	spawner    *AppInstance // the app that called PrepareSpawn — used for orphan reap
	expires    time.Time    // 60s grace; expired tokens are refused
}

// attachResult is what spawnAndRun reads from a pending-attach
// channel: either the connected AppInstance (handshake complete,
// caller takes over the registerApp / declare / loop steps), or
// an error if the attach was refused (proto mismatch, app_id
// mismatch, validation failure).
type attachResult struct {
	inst *AppInstance
	err  error
}

// pendingAttach is the record stashed in router.pendingAttach for
// each in-flight router-spawned child. The chan carries the post-
// handshake AppInstance back to spawnAndRun; kiosk is set on the
// inst BEFORE the handshake's WindowID allocation runs (see the
// pendingAttach map's comment for why the timing matters).
type pendingAttach struct {
	ch    chan attachResult
	kiosk bool
}

// takePendingAttach claims and removes the pending entry for pid.
// Returns (rec, true) if found. The caller is responsible for
// writing the attach result before another goroutine starts a
// new spawn that could reuse the pid (unlikely but possible).
func (r *Router) takePendingAttach(pid int) (*pendingAttach, bool) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	ch, ok := r.pendingAttach[pid]
	if ok {
		delete(r.pendingAttach, pid)
	}
	return ch, ok
}

// SubscribeAppMsg registers a one-shot watcher for the next outbound
// app_msg from instanceID whose data has "id" == matchID. The
// returned channel receives the (already JSON-normalized) data map;
// the watcher is removed after delivery or when cancel() is called.
// Used by the control socket's `msg` op to await BE replies; not on
// the hot path.
func (r *Router) SubscribeAppMsg(instanceID, matchID string) (<-chan map[string]any, func()) {
	ch := make(chan map[string]any, 1)
	r.appMsgMu.Lock()
	byMatch := r.appMsgWatchers[instanceID]
	if byMatch == nil {
		byMatch = make(map[string]chan map[string]any)
		r.appMsgWatchers[instanceID] = byMatch
	}
	// One-shot per (instance, matchID). A late re-subscribe replaces
	// any earlier waiter — the previous channel is closed so its
	// caller sees a nil receive and can tell the slot was stolen.
	if prev, ok := byMatch[matchID]; ok {
		close(prev)
	}
	byMatch[matchID] = ch
	r.appMsgMu.Unlock()
	cancel := func() {
		r.appMsgMu.Lock()
		if cur, ok := r.appMsgWatchers[instanceID][matchID]; ok && cur == ch {
			delete(r.appMsgWatchers[instanceID], matchID)
			if len(r.appMsgWatchers[instanceID]) == 0 {
				delete(r.appMsgWatchers, instanceID)
			}
		}
		r.appMsgMu.Unlock()
	}
	return ch, cancel
}

// dispatchAppMsgWatchers delivers data to any waiter registered for
// (instanceID, matchID) and removes the entry. Best-effort — if the
// channel is already gone (cancelled) or the buffer is full, the
// data is dropped. Returns true if a watcher was matched.
func (r *Router) dispatchAppMsgWatchers(instanceID, matchID string, data map[string]any) bool {
	r.appMsgMu.Lock()
	byMatch := r.appMsgWatchers[instanceID]
	if byMatch == nil {
		r.appMsgMu.Unlock()
		return false
	}
	ch, ok := byMatch[matchID]
	if !ok {
		r.appMsgMu.Unlock()
		return false
	}
	delete(byMatch, matchID)
	if len(byMatch) == 0 {
		delete(r.appMsgWatchers, instanceID)
	}
	r.appMsgMu.Unlock()
	select {
	case ch <- data:
	default:
	}
	return true
}

// dropAppMsgWatchers tears down all waiters for an instance. Called
// when an app exits so blocked subscribers don't hang. A nil-data
// send tells callers the instance is gone.
func (r *Router) dropAppMsgWatchers(instanceID string) {
	r.appMsgMu.Lock()
	byMatch := r.appMsgWatchers[instanceID]
	delete(r.appMsgWatchers, instanceID)
	r.appMsgMu.Unlock()
	for _, ch := range byMatch {
		close(ch)
	}
}

// Registry exposes the catalog for callers that want to surface it
// (e.g. cmd/wash-router for a /catalog endpoint).
func (r *Router) Registry() *Registry { return r.reg }

// catalog builds the snapshot the shell sees on connect. Surface=
// desktop apps, surface=background apps, and Hidden apps are
// filtered out — they don't belong in launchers (the session app
// and background services autoboot; hidden apps are test/utility).
func (r *Router) catalog() []wire.ShellCatalogApp {
	entries := r.reg.Entries()
	out := make([]wire.ShellCatalogApp, 0, len(entries))
	for _, e := range entries {
		if e.Manifest == nil {
			// Listed-disabled with no parsable manifest: skip; we
			// have no name to render.
			continue
		}
		if e.Manifest.Surface == SurfaceDesktop || e.Manifest.Surface == SurfaceBackground {
			continue
		}
		if e.Manifest.Hidden && !r.cfg.ShowHidden {
			continue
		}
		out = append(out, wire.ShellCatalogApp{
			ID:          e.Manifest.ID,
			Name:        e.Manifest.Name,
			Icon:        e.Manifest.Icon,
			Accent:      e.Manifest.Accent,
			Surface:     e.Manifest.Surface,
			Instancing:  e.Manifest.Instancing,
			Disabled:    !e.Enabled(),
			Reason:      e.Reason,
			RootVariant: e.Manifest.RootVariant,
		})
	}
	return out
}

// panelCatalog returns the app-supplied settings panels: one descriptor
// per enabled registry entry whose manifest declares a SettingsPanel.
// Unlike catalog(), this does NOT filter by surface — most panels come
// from surface=background services, which are intentionally absent from
// the launchable Apps list. Sorted by (Order, Section) for a stable nav.
func (r *Router) panelCatalog() []wire.ShellPanelDesc {
	entries := r.reg.Entries()
	out := make([]wire.ShellPanelDesc, 0)
	for _, e := range entries {
		if !e.Enabled() || e.Manifest.SettingsPanel == nil {
			continue
		}
		sp := e.Manifest.SettingsPanel
		out = append(out, wire.ShellPanelDesc{
			AppID:   e.Manifest.ID,
			Section: sp.Section,
			Element: sp.Element,
			Icon:    sp.Icon,
			Order:   sp.Order,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Section < out[j].Section
	})
	return out
}

// Config returns the active configuration.
func (r *Router) Config() Config { return r.cfg }

// handshakeSession builds the Session bag the router ships to every
// app in the IdentityAck. Named distinctly from r.session (the
// desktop-session app state).
func (r *Router) handshakeSession() *wire.Session {
	// closeGrace is a router-wide constant today; the wire field
	// gives apps a way to read it without guessing. If it ever
	// becomes per-app configurable, the value at this site is what
	// each instance sees.
	return &wire.Session{
		Root:         r.cfg.FSRoot,
		CloseGraceMs: uint32(closeGrace / time.Millisecond),
		OpenExts:     r.openExts(),
	}
}

// openExts is the union of every enabled app's manifest Opens — the
// session-wide file-association table the FE consults (via Session) to
// decide whether a path can open in a handler app. Sorted + de-duped;
// matches resolveOpen's lowercased extension comparison.
func (r *Router) openExts() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range r.reg.Entries() {
		if !e.Enabled() || e.Manifest == nil {
			continue
		}
		for _, o := range e.Manifest.Opens {
			lo := strings.ToLower(o)
			if !seen[lo] {
				seen[lo] = true
				out = append(out, lo)
			}
		}
	}
	sort.Strings(out)
	return out
}

// allocInstanceID returns a fresh per-process instance id. The format
// is intentionally opaque — apps must treat it as a string token.
func (r *Router) allocInstanceID() string {
	n := r.nextInstance.Add(1)
	return fmt.Sprintf("i-%d", n)
}

func (r *Router) allocWindowID() uint32 {
	return r.nextWindow.Add(1)
}

// allocChannelID returns a fresh raw-channel id. v0.1 uses the same
// id space on the app socket and the WS; the allocator starts at 2
// so it's safe on both (channel 0 = ctrl, channel 1 = app-side event).
func (r *Router) allocChannelID() uint32 {
	return r.nextChannel.Add(1) + 1
}

// spawnEnv builds the env additions every spawned app receives on top
// of the router's process environment.
func (r *Router) spawnEnv() []string {
	var env []string
	if r.cfg.ControlSocket != "" {
		env = append(env, "WASH_CONTROL_SOCKET="+r.cfg.ControlSocket)
	}
	// Tell spawned apps this desktop is a relayed remote view (--listen-raw
	// endpoint) so the remote-family apps refuse to nest a connection.
	if r.cfg.Relayed {
		env = append(env, "WASH_RELAYED=1")
	}
	// WASH_BIN_DIR points at the directory the wash-router binary
	// lives in — in dev, that's where wash-launch and every wash app
	// sit; in prod packagings it depends on the install layout.
	// Consumers like wash-term prepend it to the shell's PATH so the
	// in-terminal experience is "type wash-launch and it works" —
	// the xterm-loads-DISPLAY analogue but for the wash CLI tools.
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		env = append(env, "WASH_BIN_DIR="+filepath.Dir(exe))
	}
	// Merge any published display env (WASH_WAYLAND_DISPLAY etc.) so a
	// spawned app — notably wash-term's shell via pty.WithWashEnv — can
	// reach the compositor's sockets. See docs/DISPLAY_ENV.md.
	r.publishedEnvMu.Lock()
	for k, v := range r.publishedEnv {
		env = append(env, k+"="+v)
	}
	r.publishedEnvMu.Unlock()
	return env
}

// ChannelScrollbackBytes is the per-channel ring-buffer capacity for
// bytes flowing app → shell. Sized to comfortably hold a few
// thousand lines of terminal output so a reattaching shell can replay
// the recent scrollback.
const ChannelScrollbackBytes = 256 * 1024

// channelBinding is a router-side raw channel — paired writers on
// each transport plus enough state to clean up when either end goes
// away. Bytes on channel id ChannelID flow app ↔ shell verbatim.
//
// shell may be nil after the bound shell disconnects (refresh, network
// drop). bytes from app are then captured in the ring buffer only;
// the next shell that attaches receives the buffered scrollback as
// a single replay frame, followed by live bytes.
type channelBinding struct {
	channelID uint32
	app       *AppInstance
	windowID  uint32 // the shell-side window the channel is rooted at
	kind      string // wire.ChannelKindGeneric or wire.ChannelKindBundle

	// peerConn is set for a remote-apps relay channel (kind="peer",
	// docs/REMOTE.md): the endpoint is this socket (an ssh -L'd unix
	// socket reaching host B) instead of an app. Raw frames on the channel
	// are written verbatim to it; a pump goroutine copies the reverse.
	// origin names the host (for logs + the channel.bind to the shell).
	peerConn net.Conn
	origin   string

	// shellMu guards shell + buf + behind. Held briefly during forward
	// and rebind paths.
	shellMu sync.Mutex
	shell   *ShellSession
	buf     *ringBuffer

	// behind marks a terminal channel whose FE has stopped keeping up:
	// a non-blocking forward (docs/PTY_ROBUST.md, Fix B) found neither
	// credit nor scheduler room, so live output is suppressed and held
	// byte-exact in buf. While behind, NO live bytes are streamed — that
	// would ship a torn stream (a hole mid-escape leaves the terminal in
	// a wrong mode). The flag is cleared only by a resync (a clean
	// reset + realigned snapshot), never by merely resuming. Guarded by
	// shellMu. Always false for peer/noCredit channels.
	behind bool

	// credit is the FE-→router flow-control ledger for this
	// channel (docs/QOS.md §5). Bulk-class router→shell writes
	// reserve from it; the FE replenishes via channel.credit on
	// the shell control channel. Interactive-class writes
	// (bundle/replay transactional flows) bypass it. Nil when
	// noCredit is set (relay channels — see below).
	credit *ChannelCredit

	// noCredit skips the per-channel credit ledger entirely. Set for
	// remote-apps relay (peer) channels: the relay is a verbatim conduit
	// and host B already does per-channel flow control end-to-end (the FE
	// credits B's inner channels as it absorbs them, docs/REMOTE.md §7). An
	// A-side window would only double-gate — and worse, its blocking
	// Reserve runs in the single pumpPeerToShell goroutine, so an exhausted
	// aggregate window head-of-line-blocks B's interactive frames behind
	// B's bulk. Creditless, the relay never blocks reading B's socket.
	noCredit bool
}

func (r *Router) registerChannel(b *channelBinding) {
	if b.credit == nil && !b.noCredit {
		b.credit = NewChannelCredit(DefaultChannelCredit)
	}
	r.channelsMu.Lock()
	r.channels[b.channelID] = b
	r.channelsMu.Unlock()
}

func (r *Router) lookupChannel(id uint32) *channelBinding {
	r.channelsMu.Lock()
	defer r.channelsMu.Unlock()
	return r.channels[id]
}

// closeChannel removes the binding and (best-effort) tells both sides
// it is gone. Idempotent — repeated calls for the same id are no-ops.
func (r *Router) closeChannel(id uint32, reason string) {
	r.channelsMu.Lock()
	b := r.channels[id]
	delete(r.channels, id)
	r.channelsMu.Unlock()
	if b == nil {
		return
	}
	// Unblock any producer waiting on this channel's credit so the
	// teardown can proceed without leaks.
	if b.credit != nil {
		b.credit.Close()
	}
	if b.app != nil {
		_ = b.app.writeCtrl(wire.NewChannelClosed(id, reason))
	}
	// Remote-apps relay: close the ssh -L'd socket (unblocks the pump) and
	// drop it from the owning shell's tracking.
	if b.peerConn != nil {
		_ = b.peerConn.Close()
	}
	b.shellMu.Lock()
	sh := b.shell
	b.shellMu.Unlock()
	if sh != nil {
		if b.peerConn != nil {
			sh.untrackPeer(id)
		}
		_ = sh.WriteCtrl(wire.NewShellChannelUnbind(id, reason))
	}
}

// closeChannelsForApp tears down every channel owned by inst. Called
// when an app exits.
func (r *Router) closeChannelsForApp(inst *AppInstance, reason string) {
	r.channelsMu.Lock()
	var ids []uint32
	for id, b := range r.channels {
		if b.app == inst {
			ids = append(ids, id)
		}
	}
	r.channelsMu.Unlock()
	for _, id := range ids {
		r.closeChannel(id, reason)
	}
}

// bringUp performs every step between "the app's handshake has just
// completed" and "the loop is about to start": register the inst,
// allocate the window-create patch (windowed apps only), declare to
// shells, broadcast the patch, send the initial WindowMapped event.
//
// Shared by spawnAndRun (router started the child), startFreshAttach
// (terminal-launched / token-attached), and HandleApp (transport
// supplied directly — used by tests).
//
// Order matters: declare-to-shells happens BEFORE the window patch
// so the shell has the bundle in flight when window.upsert lands.
func (r *Router) bringUp(ctx context.Context, inst *AppInstance) {
	r.registerApp(inst)
	var patches []wire.SessionPatch
	if inst.WindowID != 0 {
		var defW, defH uint32
		var chromeless bool
		if inst.Manifest.Window != nil {
			defW = inst.Manifest.Window.DefaultWidth
			defH = inst.Manifest.Window.DefaultHeight
			chromeless = inst.Manifest.Window.Chromeless
		}
		patches = r.winSession.createWindow(inst.WindowID, inst.InstanceID, inst.Manifest.Element, inst.Manifest.Icon, inst.Manifest.Accent, inst.Manifest.Name, defW, defH, inst.IsRoot(), chromeless)
	}
	// The one "this instance exists" line — without it the registered
	// set can't be reconstructed from the log.
	r.log("app %s up instance=%s win=%d", inst.AppID, inst.InstanceID, inst.WindowID)
	if err := r.declareAppToAllShells(ctx, inst); err != nil {
		r.log("declare %s instance=%s: %v", inst.AppID, inst.InstanceID, err)
	}
	if len(patches) > 0 {
		r.broadcastPatches(patches)
	}
	if inst.WindowID != 0 {
		if err := inst.WriteEvt(wire.NewEvtWindowMapped(inst.WindowID)); err != nil {
			r.log("app %s instance=%s: initial WindowMapped lost: %v", inst.AppID, inst.InstanceID, err)
		}
	}
}

// tearDown is the inverse of bringUp: unregister inst, close every
// channel it owns, drop any pending app-msg watchers, drop the
// persisted app-state, and tell shells the window is gone. Shared by
// all three loop-finished paths. Idempotent for windowless apps.
func (r *Router) tearDown(inst *AppInstance) {
	r.unregisterApp(inst)
	r.closeChannelsForApp(inst, "app exited")
	r.dropAppMsgWatchers(inst.InstanceID)
	r.dropRuntimeStats(inst.InstanceID)
	r.ingress.dropInstance(inst.InstanceID)
	r.winSession.dropAppState(inst.InstanceID)
	if inst.WindowID != 0 {
		r.broadcastPatches(r.winSession.destroyWindow(inst.WindowID))
	}
	// Multi-window: tell shells about every window created via
	// window.create too. closeChannelsForApp above already closed
	// their channels (it matches on b.app == inst, window-agnostic).
	inst.winMu.Lock()
	extra := make([]uint32, 0, len(inst.extraWins))
	for w := range inst.extraWins {
		extra = append(extra, w)
	}
	inst.extraWins = nil
	inst.winMu.Unlock()
	for _, w := range extra {
		r.broadcastPatches(r.winSession.destroyWindow(w))
	}
}

// spawnAndRun spawns the given app, runs its handshake, registers it,
// declares it to attached shells, sends the initial mapped event for
// windowed apps, and runs the app's frame loop in a goroutine. The
// goroutine handles cleanup on exit.
//
// All three caller paths — spawn.request from an app, the control
// socket, and the initial / session bootstraps — go through here.
//
// Singleton short-circuit: if entry's manifest declares
// instancing:"singleton" and an instance is already running, return
// THAT instance instead of starting a second one. This is how
// sentinel addressing converges every dispatch on the same process.
// resolveOpen finds the registered app that handles path's extension via
// each manifest's Opens (wash's mime association). First enabled match wins
// (registry order is stable). Returns nil if the path has no extension or no
// app claims it — the caller logs and drops the open.
func (r *Router) resolveOpen(path string) *Entry {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return nil
	}
	for _, e := range r.reg.Entries() {
		if !e.Enabled() {
			continue
		}
		for _, o := range e.Manifest.Opens {
			if strings.ToLower(o) == ext {
				return e
			}
		}
	}
	return nil
}

func (r *Router) spawnAndRun(ctx context.Context, entry *Entry, kiosk bool, extraArgs ...string) (*AppInstance, error) {
	if entry.Manifest.Instancing == InstancingSingleton {
		if existing := r.singletonInstance(entry.Manifest.ID); existing != nil {
			return existing, nil
		}
	}
	if r.cfg.ControlSocket == "" {
		return nil, fmt.Errorf("spawn %s: control socket disabled (apps dial WASH_DISPLAY)", entry.Manifest.ID)
	}
	// Spawn and register the pid's pending-attach slot atomically under
	// pendingMu. cmd.Start has already returned by the time Spawn does, so
	// a fast child (e.g. a Go background app dialing back instantly) could
	// otherwise be processed by the control-socket attach handler BEFORE
	// the slot exists — falling through to the fresh-attach branch while
	// this spawnAndRun keeps waiting on a channel that never fires, then
	// killing the (healthy) child on the 10s timeout. takePendingAttach
	// contends on this same mutex, so holding it across Start + insert
	// makes the handler block until the slot is in place. (Bit the first
	// browser-connect sweep, which spawns several background apps at once.)
	ch := make(chan attachResult, 1)
	r.pendingMu.Lock()
	sr, err := Spawn(entry.Path, entry.Manifest.ID, r.cfg.ControlSocket, r.spawnEnv(), extraArgs)
	if err != nil {
		r.pendingMu.Unlock()
		return nil, fmt.Errorf("spawn %s: %w", entry.Manifest.ID, err)
	}
	cmd := sr.Cmd
	startedAt := time.Now()
	pid := cmd.Process.Pid
	r.pendingAttach[pid] = &pendingAttach{ch: ch, kiosk: kiosk}
	r.pendingMu.Unlock()
	defer func() {
		// Best-effort: in case of timeout/cancel the channel still
		// hangs around if we leave it. takePendingAttach is safe to
		// call even if it was already taken.
		_, _ = r.takePendingAttach(pid)
	}()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	var inst *AppInstance
	select {
	case res := <-ch:
		if res.err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, fmt.Errorf("attach %s: %w", entry.Manifest.ID, res.err)
		}
		inst = res.inst
		inst.Cmd = cmd
		inst.spawnLog = sr.logBuf
		inst.startedAt = startedAt
		inst.Kiosk = kiosk
	case <-timeout.C:
		tail := sr.LogTail()
		r.log("spawn %s pid=%d attach timeout after 10s. log_tail=%q", entry.Manifest.ID, pid, tail)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("attach timeout for %s (pid %d)", entry.Manifest.ID, pid)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ctx.Err()
	}
	transport := inst.Transport
	r.bringUp(ctx, inst)
	go func() {
		if err := inst.loop(context.Background()); err != nil {
			r.log("app %s loop instance=%s: %v", inst.AppID, inst.InstanceID, err)
		}
		_ = transport.Close()
		_ = cmd.Wait()
		// Inspect exit status before tearing down. tearDown sends
		// the window-delete patch that the shell would otherwise
		// race against the crash event. crashEvent must precede
		// the delete so the shell can mark the window crashed
		// in place rather than dropping it.
		r.maybeBroadcastCrash(inst)
		r.tearDown(inst)
	}()
	return inst, nil
}

// launchOrRaise opens entry for a user "launch" intent — the settings
// "Open Network…" button, a launcher click, a control-socket launch.
// For a windowed single-window app (instancing single/singleton) that
// is already running, it raises the existing window to the foreground
// and returns that instance instead of starting a duplicate. This is
// what makes "Open X" surface the already-open window rather than doing
// nothing (or, for instancing=single, stacking a second window).
// Multi-instance apps always get a fresh spawn. Callers that handle
// background/desktop surfaces specially must do so before calling here.
func (r *Router) launchOrRaise(ctx context.Context, entry *Entry) (*AppInstance, error) {
	if entry.Manifest.Instancing != InstancingMulti {
		if existing := r.instanceByApp(entry.Manifest.ID); existing != nil {
			r.raiseInstanceWindow(existing)
			return existing, nil
		}
	}
	return r.spawnAndRun(ctx, entry, false)
}

// instanceByApp returns a currently-running instance for appID, or nil
// if none. Prefers the singleton slot; otherwise scans the app table
// (instancing=single isn't indexed there). With single/singleton there
// is at most one, so the scan is unambiguous. Caller must NOT hold r.mu.
func (r *Router) instanceByApp(appID string) *AppInstance {
	r.mu.Lock()
	defer r.mu.Unlock()
	if inst := r.singletons[appID]; inst != nil {
		return inst
	}
	for _, inst := range r.apps {
		if inst.Manifest != nil && inst.Manifest.ID == appID {
			return inst
		}
	}
	return nil
}

// raiseInstanceWindow brings inst's primary window to the foreground —
// restores it if minimized, raises it to the top of the z-stack, marks
// it focused, and relays focus/unfocus events to the affected apps. The
// same end state as a user clicking the window's taskbar pill (see
// ShellSession.handleWindowFocus), but router-initiated. No-op for a
// windowless (background/kiosk/desktop) instance.
func (r *Router) raiseInstanceWindow(inst *AppInstance) {
	if inst == nil || inst.WindowID == 0 {
		return
	}
	win := inst.WindowID
	// Un-minimize first so the raise is actually visible.
	r.broadcastPatches(r.winSession.setState(win, wire.WindowStateNormal))
	prev := r.winSession.focusedWindowID()
	patches := r.winSession.focus(win)
	if len(patches) == 0 {
		return
	}
	r.broadcastPatches(patches)
	if prev != 0 && prev != win {
		r.mu.Lock()
		prevInst := r.byWin[prev]
		r.mu.Unlock()
		if prevInst != nil {
			if err := prevInst.WriteEvt(wire.NewEvtWindowUnfocus(prev)); err != nil {
				r.log("raise: unfocus relay win=%d: %v", prev, err)
			}
		}
	}
	if err := inst.WriteEvt(wire.NewEvtWindowFocus(win)); err != nil {
		r.log("raise: focus relay instance=%s win=%d: %v", inst.InstanceID, win, err)
	}
}

// maybeBroadcastCrash inspects the cmd.ProcessState after Wait() has
// returned and, if the exit looks abnormal (non-zero code or
// signal-killed), ships a ShellAppCrashed event to every attached
// shell. A clean exit (code 0) is silent — apps closing themselves
// down don't need a tombstone.
func (r *Router) maybeBroadcastCrash(inst *AppInstance) {
	if inst.Cmd == nil || inst.Cmd.ProcessState == nil {
		return
	}
	// Router-initiated terminations (close handshake, devreload,
	// shutdown) set expectedExit before signalling. Skip those —
	// SIGTERM after the user clicked X is the orderly path, not a
	// crash.
	if inst.expectedExit.Load() {
		return
	}
	ps := inst.Cmd.ProcessState
	code := ps.ExitCode()
	signal := ""
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		signal = ws.Signal().String()
	}
	if code == 0 && signal == "" {
		return
	}
	log := ""
	if inst.spawnLog != nil {
		log = inst.spawnLog.String()
	}
	uptime := ""
	if !inst.startedAt.IsZero() {
		uptime = time.Since(inst.startedAt).Round(time.Second).String()
	}
	r.log("app %s crashed: code=%d signal=%q uptime=%s instance=%s",
		inst.AppID, code, signal, uptime, inst.InstanceID)
	msg := wire.NewShellAppCrashed(inst.InstanceID, inst.AppID, inst.WindowID, code, signal, uptime, log)
	for _, s := range r.shellList() {
		if err := s.WriteCtrl(msg); err != nil {
			r.log("broadcast crash to shell: %v", err)
		}
	}
}

// closeChannelsForWindow tears down every channel rooted at the given
// window id. Called from window.destroy paths.
func (r *Router) closeChannelsForWindow(windowID uint32, reason string) {
	r.channelsMu.Lock()
	var ids []uint32
	for id, b := range r.channels {
		if b.windowID == windowID {
			ids = append(ids, id)
		}
	}
	r.channelsMu.Unlock()
	for _, id := range ids {
		r.closeChannel(id, reason)
	}
}

// shellList returns a snapshot of attached shell sessions.
func (r *Router) shellList() []*ShellSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*ShellSession, 0, len(r.shells))
	for s := range r.shells {
		out = append(out, s)
	}
	return out
}

// headShellOrAny returns the foreground head shell (the most recently
// attached, where new terminal channels should bind) if it's still
// attached, else any attached shell, else nil. Picking the head instead
// of an arbitrary map-order shell is what makes a freshly-opened
// terminal show up on the connection the user is actually looking at
// when several shells are stacked (e.g. reconnect zombies).
func (r *Router) headShellOrAny() *ShellSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.headShell != nil {
		if _, ok := r.shells[r.headShell]; ok {
			return r.headShell
		}
	}
	for s := range r.shells {
		return s
	}
	return nil
}

// isHead reports whether s is the current foreground head shell — the
// authoritative driver of terminal channels (docs/PTY_ROBUST.md, Fix A).
// Reads headShell under r.mu. Callers in the raw-frame dispatch path
// MUST sample this before taking a channelBinding's shellMu: shellMu is
// a leaf lock and r.mu must never be acquired while it is held.
func (s *ShellSession) isHead() bool {
	if s.router == nil {
		return false
	}
	s.router.mu.Lock()
	defer s.router.mu.Unlock()
	return s.router.headShell == s
}

// registerApp inserts inst into the maps. It's the caller's job to
// have populated inst.WindowID (0 = no window).
func (r *Router) registerApp(inst *AppInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apps[inst.InstanceID] = inst
	if inst.WindowID != 0 {
		r.byWin[inst.WindowID] = inst
	}
	if inst.Manifest != nil && inst.Manifest.Instancing == InstancingSingleton {
		r.singletons[inst.Manifest.ID] = inst
	}
}

// unregisterApp removes inst from the maps. Idempotent.
func (r *Router) unregisterApp(inst *AppInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.apps, inst.InstanceID)
	// Drop every window→instance entry pointing at inst: the primary
	// handshake window plus any created via window.create. A scan
	// (not delete(byWin, WindowID)) so multi-window instances leave no
	// dangling routes.
	for w, owner := range r.byWin {
		if owner == inst {
			delete(r.byWin, w)
		}
	}
	if inst.Manifest != nil && inst.Manifest.Instancing == InstancingSingleton {
		if cur, ok := r.singletons[inst.Manifest.ID]; ok && cur == inst {
			delete(r.singletons, inst.Manifest.ID)
		}
	}
}

// singletonInstance returns the currently-running instance for a
// singleton app_id, or nil if none. Caller must NOT hold r.mu.
func (r *Router) singletonInstance(appID string) *AppInstance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.singletons[appID]
}

// restartBackgroundApp cycles a background singleton service: terminate
// its running instance (if any), wait for its loop goroutine to run
// tearDown (which clears the singleton slot and GCs its windows /
// channels / ingress routes), then spawn a fresh one on a router-
// lifetime context. Returns the new instance id, or a wire error code +
// error. Restricted to surface=background targets; the caller's restart
// capability is checked upstream in handleAppRestart. See
// docs/SETTINGS.md §5.
//
// Blocks up to ~20s (terminate grace + respawn attach); callers run it
// off the reader goroutine.
func (r *Router) restartBackgroundApp(appID string) (string, string, error) {
	entry := r.reg.ByID(appID)
	if entry == nil || !entry.Enabled() {
		return "", wire.ErrCodeNotFound, fmt.Errorf("app %q not found", appID)
	}
	if entry.Manifest.Surface != SurfaceBackground {
		return "", wire.ErrCodeForbidden, fmt.Errorf("app %q is not a background service", appID)
	}

	// Terminate the running instance and wait for teardown. The
	// singleton slot must clear before we respawn — spawnAndRun's
	// singleton dedup would otherwise hand back the dying instance.
	// The instance's own loop goroutine runs tearDown on exit (which
	// clears the singleton slot); we poll for that via waitSingletonGone.
	if old := r.singletonInstance(appID); old != nil && old.Cmd != nil && old.Cmd.Process != nil {
		old.expectedExit.Store(true) // a restart SIGTERM is not a crash
		_ = old.Cmd.Process.Signal(stopSignal())
		if !r.waitSingletonGone(appID, 5*time.Second) {
			// Grace expired — force-kill and wait once more.
			_ = old.Cmd.Process.Kill()
			r.waitSingletonGone(appID, 5*time.Second)
		}
	}

	// Claim the background-started slot so a concurrent shell-connect
	// autoboot doesn't race in a second copy. Mirrors
	// EnsureBackgroundAppsRunning; cleared on respawn failure so the
	// next shell connect retries.
	r.backgroundMu.Lock()
	r.backgroundStarted[appID] = true
	r.backgroundMu.Unlock()

	inst, err := r.spawnAndRun(context.Background(), entry, false)
	if err != nil {
		r.backgroundMu.Lock()
		delete(r.backgroundStarted, appID)
		r.backgroundMu.Unlock()
		return "", wire.ErrCodeInternal, fmt.Errorf("respawn %q: %w", appID, err)
	}
	return inst.InstanceID, "", nil
}

// waitSingletonGone polls until the singleton slot for appID is empty
// (the terminated instance's loop goroutine ran tearDown) or the
// timeout elapses. Returns true if the slot is clear. A short poll is
// adequate for a user-initiated restart; cmd.Wait() is owned by the
// instance's own loop goroutine, so there is no channel to select on.
func (r *Router) waitSingletonGone(appID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if r.singletonInstance(appID) == nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// resolveRecipient turns a wire.Recipient into a concrete
// *AppInstance, spawning a singleton on demand when addressed by
// app_id. Returns a structured error code so callers can surface it
// without leaking error formatting.
//
//	recipient.instance_id set  →  direct lookup
//	recipient.app_id set       →  must be a singleton manifest;
//	                               spawned on first reference
//	both / neither set         →  bad_request
func (r *Router) resolveRecipient(ctx context.Context, rec wire.Recipient) (*AppInstance, string, error) {
	if (rec.InstanceID == "") == (rec.AppID == "") {
		return nil, wire.ErrCodeBadRequest, fmt.Errorf("recipient must set exactly one of instance_id or app_id")
	}
	if rec.InstanceID != "" {
		inst := r.appByInstance(rec.InstanceID)
		if inst == nil {
			return nil, wire.ErrCodeNotFound, fmt.Errorf("no instance %q", rec.InstanceID)
		}
		return inst, "", nil
	}
	// Sentinel: only valid for singletons.
	entry := r.reg.ByID(rec.AppID)
	if entry == nil || !entry.Enabled() {
		return nil, wire.ErrCodeNotFound, fmt.Errorf("no app %q", rec.AppID)
	}
	if entry.Manifest.Instancing != InstancingSingleton {
		return nil, wire.ErrCodeForbidden, fmt.Errorf("app %q is not singleton; address by instance_id", rec.AppID)
	}
	if inst := r.singletonInstance(rec.AppID); inst != nil {
		return inst, "", nil
	}
	// Spawn-on-demand. spawnAndRun will short-circuit if a sibling
	// spawn raced us and won, so the worst case is one extra check.
	inst, err := r.spawnAndRun(ctx, entry, false)
	if err != nil {
		return nil, wire.ErrCodeInternal, fmt.Errorf("spawn %s: %w", rec.AppID, err)
	}
	return inst, "", nil
}

// appByInstance returns the AppInstance for an instance id, or nil.
func (r *Router) appByInstance(id string) *AppInstance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.apps[id]
}

// registerShell records a freshly-connected shell.
func (r *Router) registerShell(s *ShellSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shells[s] = struct{}{}
}

func (r *Router) unregisterShell(s *ShellSession) {
	r.mu.Lock()
	delete(r.shells, s)
	if r.headShell == s {
		r.headShell = nil
	}
	r.mu.Unlock()

	// Detach any channels owned by this shell. The channel itself
	// stays alive (the app keeps writing, bytes accumulate in the
	// ring buffer); a future shell that attaches gets a fresh bind
	// + scrollback replay.
	r.channelsMu.Lock()
	bindings := make([]*channelBinding, 0)
	for _, b := range r.channels {
		bindings = append(bindings, b)
	}
	r.channelsMu.Unlock()
	for _, b := range bindings {
		b.shellMu.Lock()
		if b.shell == s {
			b.shell = nil
		}
		b.shellMu.Unlock()
	}
}

// replayBundleToShell sends the cached JS bundle for inst over a
// freshly-allocated, per-shell raw channel. The shell uses kind=
// "bundle" on the bind to recognize the channel as a one-shot
// bundle upload, accumulates bytes until the unbind, and then
// blob-URL-imports the result.
//
// Bundle bytes come from the registry entry for inst's app id (the
// probe envelope captured them at scan time). No-op when the entry
// is missing or its bundle is empty.
func (r *Router) replayBundleToShell(s *ShellSession, inst *AppInstance) {
	entry := r.reg.ByID(inst.AppID)
	if entry == nil || len(entry.Bundle) == 0 {
		return
	}
	// Deliver each instance's bundle to a given shell at most once. The
	// snapshot replay (HandleShell) and declareAppToAllShells can both target
	// the same shell+instance under a timing window; a second delivery re-runs
	// the app's customElements.define() and throws. Atomic check-and-set so
	// only the first caller proceeds.
	s.writeMu.Lock()
	if s.bundleSent == nil {
		s.bundleSent = make(map[string]bool)
	}
	if s.bundleSent[inst.InstanceID] {
		s.writeMu.Unlock()
		return
	}
	s.bundleSent[inst.InstanceID] = true
	s.writeMu.Unlock()

	// Send the gzip copy when it's a win (the shell inflates per the bind's
	// Encoding); else identity. Big app bundles (edit, washamp) shrink a lot.
	raw := entry.Bundle
	payload := raw
	encoding := ""
	if gz := entry.bundleGzip(); gz != nil {
		payload = gz
		encoding = "gzip"
	}
	id := r.allocChannelID()
	if err := s.WriteCtrl(wire.NewShellChannelBindBundle(id, inst.InstanceID, encoding)); err != nil {
		r.log("bundle bind %s: %v", inst.InstanceID, err)
		return
	}
	// Chunk the write so very large bundles don't pin a giant
	// allocation in the WS layer.
	const chunkSize = 256 * 1024
	for off := 0; off < len(payload); off += chunkSize {
		end := off + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		// Interactive class: bundle delivery is a transactional
		// Bind → data → Unbind sequence. Under Bulk, the strict-
		// priority scheduler would let the Interactive Unbind
		// overtake these data frames and the shell would observe
		// "channel closed" before the bytes arrived.
		if err := s.WriteRawFrameClass(id, payload[off:end], wire.ClassInteractive); err != nil {
			r.log("bundle frame %s: %v", inst.InstanceID, err)
			return
		}
	}
	s.statsLink().recordCompression(len(raw), len(payload))
	if err := s.WriteCtrl(wire.NewShellChannelUnbind(id, "bundle complete")); err != nil {
		r.log("bundle unbind %s: %v", inst.InstanceID, err)
	}
}

// reattachChannelsToShell takes ownership of every currently-detached
// channel binding, sending ShellChannelBind + a scrollback replay
// frame so the new shell can rebuild any terminal-style state.
//
// Called once per shell session, immediately after the snapshot.
func (r *Router) reattachChannelsToShell(s *ShellSession) {
	// This shell is now the foreground head: new channels bind here and
	// terminal channels migrate here below.
	r.mu.Lock()
	r.headShell = s
	r.mu.Unlock()

	r.channelsMu.Lock()
	bindings := make([]*channelBinding, 0, len(r.channels))
	for _, b := range r.channels {
		bindings = append(bindings, b)
	}
	r.channelsMu.Unlock()

	for _, b := range bindings {
		b.shellMu.Lock()
		if b.shell == s {
			b.shellMu.Unlock()
			continue
		}
		// Migrate to the new head so a terminal follows the current
		// connection — even when the old owner shell is still live
		// (a stacked/zombie WS). Exception: a live peer (remote-relay)
		// channel stays pinned to its original connection; its pump
		// goroutine writes to that shell and must not be stolen.
		if b.shell != nil && b.peerConn != nil {
			b.shellMu.Unlock()
			continue
		}
		b.shell = s
		// The Bind + realigned replay below is itself a clean resync,
		// so clear any "behind" desync flag (docs/PTY_ROBUST.md, Fix B):
		// a reattaching shell recovers a previously-wedged channel and
		// live forwarding resumes.
		b.behind = false
		var replay []byte
		if b.buf != nil {
			replay = b.buf.Snapshot()
			if b.buf.Truncated() {
				// The ring wrapped while detached: the snapshot
				// starts at an arbitrary byte, possibly mid-rune
				// or mid-escape. Trim to a clean boundary so the
				// replay doesn't open with garbage.
				replay = realignReplay(replay)
			}
		}
		id := b.channelID
		win := b.windowID
		b.shellMu.Unlock()
		if err := s.WriteCtrl(wire.NewShellChannelBind(id, win, b.kind)); err != nil {
			r.log("reattach bind: %v", err)
			continue
		}
		if len(replay) > 0 {
			// Interactive class so the replay arrives in the same
			// transactional window as the Bind that preceded it —
			// see replayBundleToShell for the same rule.
			if err := s.WriteRawFrameClass(id, replay, wire.ClassInteractive); err != nil {
				r.log("reattach replay channel %d: %v", id, err)
			}
		}
	}
}

// resyncChannel recovers a terminal channel that went "behind"
// (docs/PTY_ROBUST.md, Fix B): it sends a channel.resync control so the
// FE resets that terminal to a clean state, then the realigned
// scrollback snapshot, then clears the behind flag so live forwarding
// resumes.
//
// The whole sequence runs under shellMu — and the forward's ring append
// (b.buf.Write) also holds shellMu — so the snapshot is complete as of
// the lock and no live byte can interleave between the snapshot and the
// resumed stream: no gap, no torn stream. All enqueues are non-blocking
// (TrySubmit); if the scheduler can't take them right now the channel
// stays behind and the next credit grant or the watchdog retries. A
// retry that re-sends a reset is harmless (idempotent).
func (r *Router) resyncChannel(b *channelBinding) {
	b.shellMu.Lock()
	defer b.shellMu.Unlock()
	if !b.behind || b.shell == nil || b.peerConn != nil {
		return
	}
	sh := b.shell
	var replay []byte
	if b.buf != nil {
		replay = b.buf.Snapshot()
		if b.buf.Truncated() {
			replay = realignReplay(replay)
		}
	}
	if !sh.tryWriteCtrl(wire.NewShellChannelResync(b.channelID, b.windowID, b.kind)) {
		return // control queue full — stay behind, retry later
	}
	if len(replay) > 0 && !sh.tryWriteRawInteractive(b.channelID, replay) {
		// Reset went out but the snapshot didn't fit; leave behind set so
		// the next grant resends reset + snapshot (re-reset is harmless).
		return
	}
	b.behind = false
}

// declareAppToAllShells announces inst to every attached shell via
// ShellSession.declareInstance, which dedupes against a parallel
// declareExistingAppsTo run on the same shell. Also replays the
// instance's bundle if it's already cached — for new apps spawned
// after the shell connected (e.g. via the launcher).
func (r *Router) declareAppToAllShells(ctx context.Context, inst *AppInstance) error {
	var firstErr error
	for _, s := range r.shellList() {
		if err := s.declareInstance(inst); err != nil && firstErr == nil {
			firstErr = err
		}
		r.replayBundleToShell(s, inst)
	}
	if firstErr != nil {
		return firstErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// broadcastPatches sends a single session.patch frame to every
// connected shell. No-op if patches is empty.
func (r *Router) broadcastPatches(patches []wire.SessionPatch) {
	if len(patches) == 0 {
		return
	}
	msg := wire.NewShellSessionPatch(patches...)
	for _, s := range r.shellList() {
		if err := s.WriteCtrl(msg); err != nil {
			r.log("broadcast patch: %v", err)
		}
	}
}
