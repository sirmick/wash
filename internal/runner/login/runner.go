// Package loginrun is the wash-login CLI's compiled-in form.
//
// cmd/wash-login is a tiny shim that calls Run with os.Args[1:];
// flag parsing and lifecycle live here. Mirrors the routerrun
// package's split so tests and a future multi-call wash binary can
// drive both runners without leaking flag state.
package loginrun

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/login"
)

const version = "0.0.0"

const (
	defaultListen     = "127.0.0.1:11000"
	defaultSecretPath = "/etc/wash/secret.key"
)

// Run drives wash-login with the given argv (excluding the program
// name). Returns a process exit code — 0 on normal shutdown,
// non-zero on setup / runtime failure.
func Run(args []string) int {
	fs := flag.NewFlagSet("wash-login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	listen := fs.String("listen", defaultListen, "host:port to bind (loopback-only by default; use --insecure-listen to bind elsewhere)")
	insecureListen := fs.Bool("insecure-listen", false, "permit binding to non-loopback addresses on plain HTTP (production deployments should front with nginx/Caddy/Tailscale and keep wash-login on 127.0.0.1)")
	secretKey := fs.String("secret-key", "", fmt.Sprintf("path to the HMAC secret used to sign session cookies (default %s)", defaultSecretPath))
	secretGenerate := fs.Bool("secret-generate", false, "generate the secret-key file if it doesn't exist (mode 0600). Off by default so production installs fail noisily when /etc/wash/secret.key is missing.")
	cookieSecure := fs.Bool("cookie-secure", false, "set the Secure flag on session cookies. Required when a TLS terminator is in front; leave off for plain-HTTP loopback dev.")
	cookieTTL := fs.Duration("cookie-ttl", login.DefaultCookieTTL, "how long a freshly-minted session cookie is valid for")
	authTest := fs.String("auth-test", "", `dev/CI-only test backend: --auth-test "user:password" hard-codes one allowed credential. Resolved uid/gid default to wash-login's own; override with --auth-test-uid / --auth-test-gid.`)
	authTestUID := fs.Uint("auth-test-uid", 0, "uid to attach to a successful --auth-test login (default: this process's uid)")
	authTestGID := fs.Uint("auth-test-gid", 0, "gid to attach to a successful --auth-test login (default: this process's gid)")
	authTestShell := fs.String("auth-test-shell", "/bin/sh", "shell to record on a successful --auth-test login")
	routerBinary := fs.String("router-binary", "", "path to wash-router. Empty means look in the directory of wash-login's own binary, then PATH.")
	appsDir := fs.String("apps-dir", "", "apps-dir forwarded to spawned wash-router processes. Empty means let the router default to its own binary's directory.")
	runRoot := fs.String("run-root", "/run/wash", "root for per-uid runtime state (sessions sockets, spawn flock).")
	idleTimeout := fs.Duration("idle-timeout", 0, "--idle-timeout forwarded to spawned wash-router processes. Zero forwards no flag (router default 30m).")
	maxSessions := fs.Int("max-sessions-per-uid", 8, "cap on concurrent live wash-router processes per user. 0 disables; embedded deployments typically set to 1.")
	noHandoff := fs.Bool("no-handoff", false, "disable the /ws handoff path (M2-only mode for auth-flow inspection). Implied off when --auth-test is set.")
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Printf("wash-login %s\n", version)
		return 0
	}

	logger := log.New(os.Stderr, "wash-login ", log.LstdFlags|log.Lmsgprefix)

	// Loopback-only by default; --insecure-listen acknowledges the
	// risk of plain HTTP across an untrusted segment.
	if !isLoopback(*listen) && !*insecureListen {
		logger.Printf("--listen %s is non-loopback and --insecure-listen is not set", *listen)
		logger.Printf("front wash-login with nginx/Caddy/Tailscale-serve and keep it on 127.0.0.1, or pass --insecure-listen to override")
		return 2
	}
	if !isLoopback(*listen) && *insecureListen && !*cookieSecure {
		logger.Printf("WARNING: --listen %s without --cookie-secure — session cookies will cross plain HTTP", *listen)
	}

	// Auth backend: --auth-test is the only v1 option. shadowAuth
	// lands in a follow-up commit.
	if *authTest == "" {
		logger.Printf("no auth backend configured: pass --auth-test user:password (shadow-file backend ships in a follow-up)")
		return 2
	}
	uid := uint32(*authTestUID)
	if uid == 0 {
		uid = uint32(os.Getuid())
	}
	gid := uint32(*authTestGID)
	if gid == 0 {
		gid = uint32(os.Getgid())
	}
	auth, err := login.NewTestAuth(*authTest, uid, gid, *authTestShell)
	if err != nil {
		logger.Printf("%v", err)
		return 2
	}

	signer, err := login.NewSigner(*secretKey, *secretGenerate)
	if err != nil {
		logger.Printf("secret key: %v", err)
		return 1
	}

	cfg := login.Config{
		Auth:         auth,
		Signer:       signer,
		TTL:          *cookieTTL,
		CookieSecure: *cookieSecure,
		Logger:       logger,
	}
	if !*noHandoff {
		routerBin := resolveRouterBinary(*routerBinary)
		if routerBin == "" {
			logger.Printf("could not locate wash-router; pass --router-binary or use --no-handoff to disable /ws")
			return 2
		}
		sessions := login.NewProcRegistry()
		cfg.Sessions = sessions
		cfg.Spawner = &login.Spawner{
			RouterBinary: routerBin,
			AllowUID:     uint32(os.Getuid()),
			RunRoot:      *runRoot,
			AppsDir:      *appsDir,
			IdleTimeout:  *idleTimeout,
			MaxPerUID:    *maxSessions,
			Sessions:     sessions,
		}
		logger.Printf("handoff enabled: router=%s run-root=%s allow-uid=%d max-sessions-per-uid=%d", routerBin, *runRoot, os.Getuid(), *maxSessions)
	} else {
		logger.Printf("handoff disabled (--no-handoff); /ws will return 503")
	}

	srv, err := login.NewServer(cfg)
	if err != nil {
		logger.Printf("server: %v", err)
		return 1
	}

	logger.Printf("wash-login %s listening on %s (cookie-secure=%v cookie-ttl=%s)",
		version, *listen, *cookieSecure, *cookieTTL)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		logger.Printf("listen %s: %v", *listen, err)
		return 1
	}

	errs := make(chan error, 1)
	go func() { errs <- httpSrv.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		logger.Printf("shutdown complete")
		return 0
	case err := <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("http: %v", err)
			return 1
		}
		return 0
	}
}

// isLoopback reports whether host:port binds only to a local address.
// Defensive parse — net.SplitHostPort is the canonical check.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// resolveRouterBinary looks for wash-router. Explicit override wins;
// otherwise look beside wash-login's own binary (the dev / install
// case where they're colocated), then fall back to PATH.
func resolveRouterBinary(override string) string {
	if override != "" {
		return override
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, "wash-router")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if p, err := exec.LookPath("wash-router"); err == nil {
		return p
	}
	return ""
}
