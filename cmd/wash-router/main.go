// wash-router — the wash router and browser shell host.
//
// One static Go binary; multiplexes channels between the browser
// shell (WebSocket) and per-app inherited-fd Unix sockets. Spawns the
// session app on first shell connect; relays everything else.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/router"
)

const version = "0.0.0"

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

func main() {
	listen := flag.String("listen", "", "host:port to bind (overrides WASH_LISTEN)")
	appsDir := flag.String("apps-dir", "", "colon-separated apps dirs (overrides WASH_APPS_DIR)")
	sessionID := flag.String("session-app-id", "", "id of the desktop-surface app (overrides WASH_SESSION_APP_ID)")
	noSession := flag.Bool("no-session", false, "do not spawn the session app (kiosk / e2e)")
	initialApp := flag.String("initial-app", "", "spawn this app full-screen on first shell connect (kiosk)")
	showHidden := flag.Bool("show-hidden", false, "include manifest.hidden apps in the catalog (e2e / debug)")
	dev := flag.Bool("dev", false, "watch apps dir + router binary; auto-kill instances and broadcast shell.reload on change")
	fsRoot := flag.String("fs-root", "", "filesystem sandbox shipped to every app in the handshake (overrides WASH_FS_ROOT; legacy WASH_FM_ROOT honored if neither set)")
	controlSocket := flag.String("control-socket", "", "Unix socket for wash-launch (default: /tmp/wash-<uid>.sock; \"none\" disables)")
	screenshotDir := flag.String("screenshot-dir", "", "directory for POST /screenshot uploads (overrides WASH_SCREENSHOT_DIR; default: /tmp/wash-screenshots; \"none\" disables)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	bi := gatherBuildInfo()
	if *showVersion {
		fmt.Printf("wash-router %s\n", bi)
		return
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
		logger.Fatalf("scan: %v", err)
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
		logger.Fatalf("embedded assets: %v", err)
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

	r := router.NewRouter(cfg, reg, logf)
	srv := router.NewHTTPServer(r, assets)

	if !strings.HasPrefix(cfg.Listen, "127.0.0.1:") && !strings.HasPrefix(cfg.Listen, "[::1]:") {
		host, _, splitErr := net.SplitHostPort(cfg.Listen)
		if splitErr == nil && host != "localhost" {
			logf("WARNING: binding to non-loopback address %s; ensure trusted-network exposure", cfg.Listen)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logf("listening on %s (apps dirs: %s; session: %s)", cfg.Listen, strings.Join(cfg.AppsDirs, ":"), cfg.SessionAppID)
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

	if err := srv.Run(ctx); err != nil {
		logger.Fatalf("http: %v", err)
	}
	logf("shutdown complete")
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
