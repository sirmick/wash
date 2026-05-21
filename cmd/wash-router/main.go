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
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/router"
)

const version = "0.0.0"

const (
	defaultListen       = "127.0.0.1:7681"
	defaultSessionAppID = "com.wash.session"
)

//go:embed all:assets
var assetsFS embed.FS

// defaultAppsDir returns the directory of the wash-router binary
// itself — so `out/wash-router` finds its siblings in `out/`
// with no further config. The system / user share dirs we used to
// fall back to (~/.local/share/wash/apps, /usr/share/wash/apps)
// can be wired back when packaging matters; for now the goal is a
// single-step dev story: build the binaries, run wash-router from
// its own dir.
func defaultAppsDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	// Resolve symlinks so symlinked installs still find the real
	// neighbors. Best-effort: if EvalSymlinks fails, fall back to
	// the literal path.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
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
	controlSocket := flag.String("control-socket", "", "Unix socket for wash-launch (default: /tmp/wash-<uid>.sock; \"none\" disables)")
	screenshotDir := flag.String("screenshot-dir", "", "directory for POST /screenshot uploads (overrides WASH_SCREENSHOT_DIR; default: /tmp/wash-screenshots; \"none\" disables)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("wash-router %s\n", version)
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

	cfg := router.Config{
		Listen:        firstNonEmpty(*listen, os.Getenv("WASH_LISTEN"), defaultListen),
		AppsDirs:      router.SplitAppsDir(firstNonEmpty(*appsDir, os.Getenv("WASH_APPS_DIR"), defaultAppsDir())),
		SessionAppID:  firstNonEmpty(*sessionID, os.Getenv("WASH_SESSION_APP_ID"), defaultSessionAppID),
		NoSession:     *noSession,
		InitialAppID:  *initialApp,
		ShowHidden:    *showHidden,
		ControlSocket: cs,
		ScreenshotDir: sd,
	}

	logger := log.New(os.Stderr, "wash-router ", log.LstdFlags|log.Lmsgprefix)
	logf := func(format string, args ...any) { logger.Printf(format, args...) }

	reg := router.NewRegistry()
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
	if cfg.ControlSocket != "" {
		go func() {
			if err := r.ListenControl(ctx); err != nil {
				logf("control socket: %v", err)
			}
		}()
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
