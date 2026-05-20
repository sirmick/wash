// wash-router — the wash router and browser shell host.
//
// One static Go binary; multiplexes channels between the browser
// shell (WebSocket) and per-app inherited-fd Unix sockets. Spawns the
// session app on first shell connect; relays everything else.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"os/user"
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

// defaultAppsDir returns the colon-separated default for
// WASH_APPS_DIR (WIRE.md §5). User dir first; system dir second.
func defaultAppsDir() string {
	var userDir string
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		userDir = filepath.Join(u.HomeDir, ".local", "share", "wash", "apps")
	}
	parts := []string{}
	if userDir != "" {
		parts = append(parts, userDir)
	}
	parts = append(parts, "/usr/share/wash/apps")
	return strings.Join(parts, ":")
}

func main() {
	listen := flag.String("listen", "", "host:port to bind (overrides WASH_LISTEN)")
	appsDir := flag.String("apps-dir", "", "colon-separated apps dirs (overrides WASH_APPS_DIR)")
	sessionID := flag.String("session-app-id", "", "id of the desktop-surface app (overrides WASH_SESSION_APP_ID)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("wash-router %s\n", version)
		return
	}

	cfg := router.Config{
		Listen:       firstNonEmpty(*listen, os.Getenv("WASH_LISTEN"), defaultListen),
		AppsDirs:     router.SplitAppsDir(firstNonEmpty(*appsDir, os.Getenv("WASH_APPS_DIR"), defaultAppsDir())),
		SessionAppID: firstNonEmpty(*sessionID, os.Getenv("WASH_SESSION_APP_ID"), defaultSessionAppID),
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

	r := router.NewRouter(cfg, reg, logf)
	srv := router.NewHTTPServer(r, nil) // no embedded shell yet (C5 wires it)

	if !strings.HasPrefix(cfg.Listen, "127.0.0.1:") && !strings.HasPrefix(cfg.Listen, "[::1]:") {
		host, _, err := net.SplitHostPort(cfg.Listen)
		if err == nil && host != "localhost" {
			logf("WARNING: binding to non-loopback address %s; ensure trusted-network exposure", cfg.Listen)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logf("listening on %s (apps dirs: %s; session: %s)", cfg.Listen, strings.Join(cfg.AppsDirs, ":"), cfg.SessionAppID)

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
