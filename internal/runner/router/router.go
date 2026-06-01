// Package routerrun is the wash-router CLI's compiled-in form.
//
// Both the standalone shim (cmd/wash-router/main.go) and the
// multi-call dispatcher (cmd/wash, argv[0]="wash-router" or `wash
// router …` subcommand) call Run with the remaining argv slice.
// Run owns its own flag set so the multi-call host can drive it
// without leaking flag state.
//
// Package is named routerrun (not "router") to avoid colliding with
// the internal/router import it depends on. Directory stays
// internal/runner/router for symmetry with internal/runner/launch.
package routerrun

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/router"
	"github.com/sirmick/wash/internal/wire"
)

const version = "0.8.0"

const (
	defaultListen       = "0.0.0.0:11000"
	defaultSessionAppID = "com.wash.session"
)

// buildInfo is the version/commit/build-date trio the router logs at
// startup and exports to spawned apps via WASH_ROUTER_* env vars
// (consumed by wash-session for the desktop banner). Commit/Built
// come from runtime/debug.ReadBuildInfo when available — `go build`
// and `go install` populate vcs.* automatically. For non-vcs builds
// (Makefile-staged dev binaries with -trimpath) Commit may be empty;
// the FE renders just the version in that case.
type buildInfo struct {
	Version string
	Commit  string // short git hash, e.g. "5370839"
	Built   string // ISO-8601 of the commit time; empty if unknown
}

func gatherBuildInfo() buildInfo {
	bi := buildInfo{Version: version}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return bi
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				bi.Commit = s.Value[:7]
			} else {
				bi.Commit = s.Value
			}
		case "vcs.time":
			bi.Built = s.Value
		}
	}
	return bi
}

func (bi buildInfo) String() string {
	parts := []string{"v" + bi.Version}
	if bi.Commit != "" {
		parts = append(parts, bi.Commit)
	}
	if bi.Built != "" {
		parts = append(parts, bi.Built)
	}
	return strings.Join(parts, " ")
}

//go:embed all:assets
var assetsFS embed.FS

// resolvedExe returns the EvalSymlinks-resolved path of the running
// wash-router binary, or "" on failure. The dev-reload watcher and
// the apps-dir default both want the canonicalised path so their
// later comparisons match.
func resolvedExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// defaultAppsDir returns the directory of the wash-router binary
// itself — so `out/wash-router` finds its siblings in `out/` with
// no further config.
func defaultAppsDir() string {
	exe := resolvedExe()
	if exe == "" {
		return "."
	}
	return filepath.Dir(exe)
}

// shellAssets exposes the embedded shell-runtime directory rooted at
// the "assets" subtree as the HTTP server's filesystem.
func shellAssets() (http.FileSystem, error) {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return nil, err
	}
	return http.FS(sub), nil
}

// Run drives the wash-router with the given argv (excluding the
// program name). Returns a process exit code — 0 on normal
// shutdown, non-zero on setup / runtime failure. Callers use
// os.Exit on the returned value.
//
// All flag parsing happens against a local FlagSet so the
// multi-call host's argv[0]-dispatch can drive Run without
// touching the global flag state.
func Run(args []string) int {
	fs := flag.NewFlagSet("wash-router", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", "", "host:port to bind (overrides WASH_LISTEN)")
	appsDir := fs.String("apps-dir", "", "colon-separated apps dirs (overrides WASH_APPS_DIR)")
	sessionID := fs.String("session-app-id", "", "id of the desktop-surface app (overrides WASH_SESSION_APP_ID)")
	noSession := fs.Bool("no-session", false, "do not spawn the session app (kiosk / e2e)")
	initialApp := fs.String("initial-app", "", "spawn this app full-screen on first shell connect (kiosk)")
	showHidden := fs.Bool("show-hidden", false, "include manifest.hidden apps in the catalog (e2e / debug)")
	dev := fs.Bool("dev", false, "watch apps dir + router binary; auto-kill instances and broadcast shell.reload on change")
	fsRoot := fs.String("fs-root", "", "filesystem sandbox shipped to every app in the handshake (overrides WASH_FS_ROOT; legacy WASH_FM_ROOT honored if neither set)")
	controlSocket := fs.String("control-socket", "", "Unix socket for wash-launch (default: /tmp/wash-<uid>.sock; \"none\" disables)")
	screenshotDir := fs.String("screenshot-dir", "", "directory for POST /screenshot uploads (overrides WASH_SCREENSHOT_DIR; default: /tmp/wash-screenshots; \"none\" disables)")
	transport := fs.String("transport", "ws", `shell transport: "ws" (default; HTTP/WebSocket listener) or "virtio-console:<path>" / "serial:<path>" for a single-shell raw byte-stream — used in the v86 online demo where the kernel has no TCP/IP and the FE talks to the router over a virtio-console port.`)
	readyPath := fs.String("ready-path", "", `after the shell transport is open, write "WASH_READY\n" to this path. v86 demo: --ready-path=/dev/vport0p1 so the outer JS knows the BE is live.`)
	sessionPreopened := fs.Bool("session-preopened", false, `the leading SessionOpen frame was already consumed by an upstream login front (wash-vm/guest), so synthesize the first viewer session immediately instead of waiting for SessionOpen. Subsequent SessionOpen/SessionClose (reconnects) flow normally. Only meaningful with a non-ws --transport.`)
	listenUnix := fs.String("listen-unix", "", `multi-user ctl socket path. When set, the router does not bind --listen; instead it listens on this Unix socket for SCM_RIGHTS handoffs from wash-login (see docs/MULTIUSER.md). Mutually exclusive with --transport=virtio-console / --transport=serial / --transport=fd.`)
	name := fs.String("name", "", "human-readable session name; surfaced in stat RPC and /proc/<pid>/cmdline. Informational; immutable.")
	idleTimeout := fs.Duration("idle-timeout", 0, "self-exit after this duration with no attached shell. Zero disables; default 30m when --listen-unix is set.")
	allowUID := fs.Uint("allow-uid", 0, "uid whose SCM_RIGHTS handoffs the --listen-unix listener accepts (SO_PEERCRED-verified). Zero defaults to the router's own uid.")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		// flag already printed the message.
		return 2
	}

	bi := gatherBuildInfo()
	if *showVersion {
		fmt.Printf("wash-router %s\n", bi)
		return 0
	}

	cs := *controlSocket
	if cs == "" {
		cs = fmt.Sprintf("/tmp/wash-%d.sock", os.Getuid())
	}
	if cs == "none" {
		cs = ""
	}

	sd := firstNonEmpty(*screenshotDir, os.Getenv("WASH_SCREENSHOT_DIR"), "/tmp/wash-screenshots")
	if sd == "none" {
		sd = ""
	}

	rawRoot := firstNonEmpty(*fsRoot, os.Getenv("WASH_FS_ROOT"), os.Getenv("WASH_FM_ROOT"))
	normRoot := rawRoot
	if rawRoot != "" {
		if abs, err := filepath.Abs(rawRoot); err == nil {
			normRoot = filepath.Clean(abs)
		}
	}

	idleTimeoutVal := *idleTimeout
	if *listenUnix != "" && idleTimeoutVal == 0 {
		idleTimeoutVal = 30 * time.Minute
	}
	allowUIDVal := uint32(*allowUID)
	if *listenUnix != "" && allowUIDVal == 0 {
		allowUIDVal = uint32(os.Getuid())
	}

	cfg := router.Config{
		Listen:        firstNonEmpty(*listen, os.Getenv("WASH_LISTEN"), defaultListen),
		AppsDirs:      router.SplitAppsDir(firstNonEmpty(*appsDir, os.Getenv("WASH_APPS_DIR"), defaultAppsDir())),
		SessionAppID:  firstNonEmpty(*sessionID, os.Getenv("WASH_SESSION_APP_ID"), defaultSessionAppID),
		NoSession:     *noSession,
		InitialAppID:  *initialApp,
		ShowHidden:    *showHidden,
		ControlSocket: cs,
		ScreenshotDir: sd,
		Dev:           *dev || os.Getenv("WASH_DEV") != "",
		FSRoot:        normRoot,
		ListenUnix:    *listenUnix,
		Name:          *name,
		IdleTimeout:   idleTimeoutVal,
		AllowUID:      allowUIDVal,
	}

	logger := log.New(os.Stderr, "wash-router ", log.LstdFlags|log.Lmsgprefix)
	logf := func(format string, args ...any) { logger.Printf(format, args...) }

	reg := router.NewRegistry()
	// Trust gate for reservedIDs (e.g. com.wash.priv). Two opt-in
	// paths: --dev opts the apps dirs into trusted by virtue of "this
	// is a dev box," and WASH_TRUSTED_APPS_DIRS lets e2e tests +
	// non-dev integration setups declare specific dirs as trusted
	// without inheriting --dev's fsnotify + auto-restart behavior.
	// Production runs (no --dev, no env) require reserved-id binaries
	// to be uid-0 owned.
	var trustedDirs []string
	if cfg.Dev {
		trustedDirs = append(trustedDirs, cfg.AppsDirs...)
	}
	if v := os.Getenv("WASH_TRUSTED_APPS_DIRS"); v != "" {
		trustedDirs = append(trustedDirs, router.SplitAppsDir(v)...)
	}
	if len(trustedDirs) > 0 {
		reg.SetTrustedDirs(trustedDirs)
	}
	scanCtx, scanCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := reg.Scan(scanCtx, cfg.AppsDirs); err != nil {
		scanCancel()
		logger.Printf("scan: %v", err)
		return 1
	}
	scanCancel()

	if len(reg.Entries()) == 0 {
		logf("no apps found in %s", strings.Join(cfg.AppsDirs, ":"))
	}
	for _, e := range reg.Entries() {
		if e.Enabled() {
			logf("registered %s (surface=%s)", e.Manifest.ID, e.Manifest.Surface)
		} else {
			id := "?"
			if e.Manifest != nil {
				id = e.Manifest.ID
			}
			logf("disabled %s (%s): %s", e.Path, id, e.Reason)
		}
	}

	assets, err := shellAssets()
	if err != nil {
		logger.Printf("embedded assets: %v", err)
		return 1
	}

	// Build info goes into the router's process env so every spawned
	// app inherits WASH_ROUTER_VERSION / _COMMIT / _BUILT / _DEV via
	// the os.Environ() copy in internal/router/spawn.go. wash-session
	// reads these and ships them in the system.info app_msg the
	// desktop banner renders.
	logf("wash-router %s (dev=%v)", bi, cfg.Dev)
	_ = os.Setenv("WASH_ROUTER_VERSION", bi.Version)
	if bi.Commit != "" {
		_ = os.Setenv("WASH_ROUTER_COMMIT", bi.Commit)
	}
	if bi.Built != "" {
		_ = os.Setenv("WASH_ROUTER_BUILT", bi.Built)
	}
	if cfg.Dev {
		_ = os.Setenv("WASH_ROUTER_DEV", "1")
	}
	if cfg.Name != "" {
		_ = os.Setenv("WASH_SESSION_NAME", cfg.Name)
	}

	r := router.NewRouter(cfg, reg, logf)
	// Make the embedded shell asset FS available for TShellAssetRead
	// regardless of transport. NewHTTPServer also calls SetAssets
	// (redundant when transport=ws, but the ws branch may be skipped
	// when we run over fd:3 / unix sockets).
	r.SetAssets(assets)

	// Parse transport selection up-front so we error before any
	// listener / fs work happens on a bad flag.
	transportScheme, transportPath, err := parseTransport(*transport)
	if err != nil {
		logger.Printf("--transport: %v", err)
		return 2
	}

	// Multi-user mode (--listen-unix) is mutually exclusive with the
	// non-ws transports (virtio-console / serial / fd) which carry a
	// single byte-stream from a kernel-owned device, not a TCP-fd to
	// hand off. See docs/MULTIUSER.md.
	if cfg.ListenUnix != "" && transportScheme != "ws" {
		logger.Printf("--listen-unix is mutually exclusive with --transport=%s", transportScheme)
		return 2
	}

	var srv *router.HTTPServer
	switch {
	case cfg.ListenUnix != "":
		// Multi-user mode: no TCP listener; wash-login hands off
		// upgraded WS fds via SCM_RIGHTS on the ctl socket.
	case transportScheme == "ws":
		srv = router.NewHTTPServer(r, assets)
		if !strings.HasPrefix(cfg.Listen, "127.0.0.1:") && !strings.HasPrefix(cfg.Listen, "[::1]:") {
			host, _, splitErr := net.SplitHostPort(cfg.Listen)
			if splitErr == nil && host != "localhost" {
				logf("WARNING: binding to non-loopback address %s; ensure trusted-network exposure", cfg.Listen)
			}
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	switch {
	case cfg.ListenUnix != "":
		logf("ctl socket %s (name=%q allow-uid=%d idle=%s apps=%s session=%s)",
			cfg.ListenUnix, cfg.Name, cfg.AllowUID, cfg.IdleTimeout,
			strings.Join(cfg.AppsDirs, ":"), cfg.SessionAppID)
	case transportScheme == "ws":
		logf("listening on %s (apps dirs: %s; session: %s)", cfg.Listen, strings.Join(cfg.AppsDirs, ":"), cfg.SessionAppID)
	default:
		logf("shell transport %s://%s (apps dirs: %s; session: %s)", transportScheme, transportPath, strings.Join(cfg.AppsDirs, ":"), cfg.SessionAppID)
	}
	if cfg.FSRoot != "" {
		logf("fs sandbox root: %s", cfg.FSRoot)
	}
	if cfg.ControlSocket != "" {
		go func() {
			if err := r.ListenControl(ctx); err != nil {
				logf("control socket: %v", err)
			}
		}()
	}

	// Dev mode: watch binaries + apps dir for change events.
	// resolveRouterExe pulls the resolved-symlink path so the
	// watcher's later EvalSymlinks comparison matches.
	if cfg.Dev {
		r.StartDevReload(resolvedExe())
	}

	switch {
	case cfg.ListenUnix != "":
		// Idle reaper runs in parallel with the listener. When it
		// returns nil (idle threshold hit), we cancel the parent
		// context so the listener tears down its accept loop and
		// HandleShell goroutines drain naturally.
		listenCtx, listenCancel := context.WithCancel(ctx)
		go func() {
			if err := r.ReapWhenIdle(listenCtx, cfg.IdleTimeout); err == nil {
				listenCancel()
			}
		}()
		if err := r.RunUnixListener(listenCtx); err != nil {
			listenCancel()
			logger.Printf("listen-unix: %v", err)
			return 1
		}
		listenCancel()
	case transportScheme == "ws":
		if err := srv.Run(ctx); err != nil {
			logger.Printf("http: %v", err)
			return 1
		}
	default:
		if err := runShellOverStream(ctx, r, transportScheme, transportPath, *readyPath, *sessionPreopened, logf); err != nil {
			logger.Printf("%s transport: %v", transportScheme, err)
			return 1
		}
	}
	logf("shutdown complete")
	return 0
}

// parseTransport splits the --transport flag value into its scheme and
// path components. Schemes recognized:
//
//   - "ws"                                — default, HTTP/WebSocket listener.
//   - "virtio-console:/dev/vport0p2"      — v86 online demo, single shell over a virtio-console port.
//   - "serial:/dev/ttyS2"                 — legacy serial fallback if virtio-console is unavailable.
//
// Returns an error for unknown schemes or missing paths on non-ws
// schemes.
func parseTransport(s string) (scheme, path string, err error) {
	if s == "" || s == "ws" {
		return "ws", "", nil
	}
	i := strings.Index(s, ":")
	if i < 0 {
		return "", "", fmt.Errorf("expected <scheme>:<path>, got %q", s)
	}
	scheme = s[:i]
	path = s[i+1:]
	switch scheme {
	case "ws":
		return "", "", fmt.Errorf("ws scheme does not take a path")
	case "virtio-console", "serial", "fd":
		// fd:<N> takes an integer; supervisor pre-opens the device
		// and we wrap the inherited fd. Sidesteps a TinyEMU kernel
		// hang on first virtio-console RDWR open from inside wash.
	default:
		return "", "", fmt.Errorf("unsupported scheme %q (expected virtio-console, serial, fd, or ws)", scheme)
	}
	if path == "" {
		return "", "", fmt.Errorf("%s: path is empty", scheme)
	}
	return scheme, path, nil
}

// runShellOverStream opens path as a bidirectional byte stream, wraps
// it in a wash StreamTransport, and runs HandleShell over it. After
// the device is open and before HandleShell starts (which blocks for
// the lifetime of the shell session), it writes the WASH_READY token
// to readyPath if non-empty.
//
// readyPath errors are logged but non-fatal — a broken readiness
// channel shouldn't prevent the shell from running. In the v86 demo
// the outer JS treats the absence of a READY token as "still booting,"
// which the user sees as the boot xterm staying visible longer.
func runShellOverStream(ctx context.Context, r *router.Router, scheme, path, readyPath string, sessionPreopened bool, logf func(string, ...any)) error {
	// fd:N inherits an already-open fd (supervisor's `exec 3<>/dev/hvc2`)
	// and uses RAW syscall.Read/Write — never wrapping it in *os.File —
	// so Go's runtime poller doesn't register the fd (no
	// fcntl(F_SETFL,O_NONBLOCK), no epoll_ctl). On Linux's HVC tty
	// subsystem, that registration wedges khvcd and blocks I/O on every
	// other /dev/hvcN. Direct blocking syscalls keep us out of the
	// shared tty paths entirely.
	var rwc io.ReadWriteCloser
	switch scheme {
	case "fd":
		n, err := strconv.Atoi(path)
		if err != nil {
			return fmt.Errorf("transport fd:%s: %w", path, err)
		}
		logf("raw-syscall transport on inherited fd=%d", n)
		rwc = &rawFD{fd: n}
	default:
		logf("open %s O_RDWR|O_NONBLOCK", path)
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		logf("open %s succeeded fd=%d", path, f.Fd())
		rwc = f
	}
	defer rwc.Close()
	if readyPath != "" {
		if err := writeReadyToken(readyPath); err != nil {
			logf("ready signal to %s: %v", readyPath, err)
		}
	}
	// The serial is one byte-stream shared across every browser for the VM's
	// whole life — it has no per-browser connection lifecycle of its own. The
	// splitter re-manufactures one: the host proxy delimits each browser with
	// SessionOpen/SessionClose frames, and the splitter turns each span into a
	// fresh virtual connection. We then call HandleShell once per browser,
	// exactly as a TCP/WS listener would — the router core stays oblivious to
	// the transport. The persistent apps (session desktop, background
	// services) are spawned by HandleShell on the first session and survive
	// across viewers (HandleShell's Ensure*Running calls are idempotent and
	// never tear apps down on disconnect), so a reconnecting browser just
	// reattaches. The port stays open for the splitter's life, so the host
	// side is never orphaned.
	raw := wire.NewStreamTransport(rwc)
	split := newSessionSplitter(raw, logf)
	// --session-preopened: an upstream login front (wash-vm/guest) authenticated
	// the browser and already consumed the leading SessionOpen off this stream
	// before forking us. Synthesize the first viewer NOW — before run() starts,
	// so the active viewer is set before any router-protocol data frame (the
	// FE's first asset.read) is read and would otherwise be dropped. Subsequent
	// SessionClose/SessionOpen (the browser reconnecting) flow through run()
	// normally, so a re-attach doesn't require another login.
	if sessionPreopened {
		logf("session-preopened: synthesizing initial viewer (login front consumed SessionOpen)")
		split.beginSession()
	}
	go split.run()

	for {
		v := split.nextSession(ctx)
		if v == nil {
			// Serial gone or ctx cancelled — the router process exits and the
			// guest init respawn loop catches a genuine crash.
			if err := split.doneErr; err != nil &&
				!errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		}
		logf("viewer session: open")
		if err := r.HandleShell(ctx, v); err != nil &&
			!errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			logf("viewer session ended: %v", err)
		} else {
			logf("viewer session: closed")
		}
	}
}

// rawFD performs blocking I/O directly via syscall.Read/Write on a raw
// kernel fd. Unlike *os.File, it never registers the fd with Go's
// runtime poller, so the fd's open flags stay exactly as the caller
// set them (no automatic O_NONBLOCK, no epoll registration).
type rawFD struct {
	fd int
}

func (r *rawFD) Read(p []byte) (int, error) {
	n, err := syscall.Read(r.fd, p)
	if err != nil {
		return n, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (r *rawFD) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		n, err := syscall.Write(r.fd, p[written:])
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
		written += n
	}
	return written, nil
}

func (r *rawFD) Close() error {
	return syscall.Close(r.fd)
}

// writeReadyToken writes "WASH_READY\n" to path. The token format is
// stable: the outer JS watcher in the v86 demo matches the exact
// string. Open with O_WRONLY|O_APPEND so we don't truncate (a future
// caller could legitimately want to write multiple readiness signals
// over the same channel).
func writeReadyToken(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write([]byte("WASH_READY\n"))
	return err
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
