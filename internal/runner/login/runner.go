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
	defaultListen      = "0.0.0.0:11000"
	systemSecretPath   = "/etc/wash/secret.key"
	userSecretSubpath  = "wash/secret.key"
)

// defaultSecretPath picks /etc/wash/secret.key when wash-login runs
// with write access to /etc/wash (production install as
// root/wash-system) and falls back to $XDG_CONFIG_HOME/wash/secret.key
// (or ~/.config/wash/secret.key) for unprivileged dev / personal use.
// Either way --secret-generate defaults to true, so the file is
// minted on first run rather than failing OOTB.
func defaultSecretPath() string {
	if canWriteTo(filepath.Dir(systemSecretPath)) {
		return systemSecretPath
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, userSecretSubpath)
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", userSecretSubpath)
	}
	return systemSecretPath
}

// defaultRunRoot picks /run/wash when wash-login can write to it
// (production install as root / wash-system, tmpfiles.d-provisioned)
// and falls back to $XDG_RUNTIME_DIR/wash (typical per-user session
// runtime dir on logind systems) or /tmp/wash-<uid> as the universal
// fallback. Same OOTB principle as defaultSecretPath: never refuse
// to start over a path the unprivileged dev user can't create.
func defaultRunRoot() string {
	if canWriteTo("/run/wash") || canWriteTo("/run") {
		return "/run/wash"
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" && canWriteTo(xdg) {
		return filepath.Join(xdg, "wash")
	}
	return fmt.Sprintf("/tmp/wash-%d", os.Getuid())
}

// canWriteTo reports whether the calling uid can create files
// under dir. Used for the "where do I land my secret key by
// default" decision. A missing dir but writable parent counts as
// writable — we'll MkdirAll on demand.
func canWriteTo(dir string) bool {
	for d := dir; d != "" && d != "/"; d = filepath.Dir(d) {
		st, err := os.Stat(d)
		if err == nil {
			if !st.IsDir() {
				return false
			}
			// Cheapest probe: try to open a tempfile.
			f, err := os.CreateTemp(d, ".washprobe-*")
			if err != nil {
				return false
			}
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
			return true
		}
		if !os.IsNotExist(err) {
			return false
		}
	}
	return false
}

// Run drives wash-login with the given argv (excluding the program
// name). Returns a process exit code — 0 on normal shutdown,
// non-zero on setup / runtime failure.
func Run(args []string) int {
	fs := flag.NewFlagSet("wash-login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	listen := fs.String("listen", defaultListen, "host:port to bind. Default is "+defaultListen+" — wash-login is a network service by design.")
	insecureListen := fs.Bool("insecure-listen", false, "deprecated; no-op now that non-loopback binds are allowed by default. Kept for compatibility.")
	secretKey := fs.String("secret-key", "", fmt.Sprintf("path to the HMAC secret used to sign session cookies. Empty picks %s when writable, else $XDG_CONFIG_HOME/wash/secret.key (or ~/.config/wash/secret.key).", systemSecretPath))
	secretGenerate := fs.Bool("secret-generate", true, "generate the secret-key file if it doesn't exist (mode 0600). Default on for OOTB; pass --secret-generate=false to fail noisily on production installs that expect a pre-provisioned key.")
	cookieSecure := fs.Bool("cookie-secure", false, "set the Secure flag on session cookies. Required when a TLS terminator is in front; leave off for plain-HTTP loopback dev.")
	cookieTTL := fs.Duration("cookie-ttl", login.DefaultCookieTTL, "how long a freshly-minted session cookie is valid for")
	authTest := fs.String("auth-test", "", `dev/CI-only test backend: --auth-test "user:password" hard-codes one allowed credential. When set, overrides the default su+pty backend.`)
	authTestUID := fs.Uint("auth-test-uid", 0, "uid to attach to a successful --auth-test login (default: this process's uid)")
	authTestGID := fs.Uint("auth-test-gid", 0, "gid to attach to a successful --auth-test login (default: this process's gid)")
	authTestShell := fs.String("auth-test-shell", "/bin/sh", "shell to record on a successful --auth-test login")
	suPath := fs.String("su-path", "", "path to the su binary used for unix authentication. Empty searches /bin and /usr/bin.")
	passwdPath := fs.String("passwd-path", "/etc/passwd", "path to the passwd file used for the user list + uid lookup.")
	userList := fs.String("user-list", "show", `show|hide the user list on the login form. "hide" suppresses enumeration on sensitive deployments.`)
	minUID := fs.Uint("user-list-min-uid", 1000, "minimum uid to include in the login form's user list.")
	maxUID := fs.Uint("user-list-max-uid", 60000, "maximum uid to include in the login form's user list.")
	routerBinary := fs.String("router-binary", "", "path to wash-router. Empty means look in the directory of wash-login's own binary, then PATH.")
	appsDir := fs.String("apps-dir", "", "apps-dir forwarded to spawned wash-router processes. Empty means let the router default to its own binary's directory.")
	runRoot := fs.String("run-root", "", "root for per-uid runtime state (sessions sockets, spawn flock). Empty picks /run/wash when writable (production install), else $XDG_RUNTIME_DIR/wash (typical user session) or /tmp/wash-<uid>.")
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

	// Non-loopback default: wash-login is a network service. Warn
	// loudly when there's no TLS terminator in front so credentials
	// + cookies don't cross plain HTTP on an untrusted segment.
	if !isLoopback(*listen) && !*cookieSecure {
		logger.Printf("WARNING: --listen %s on plain HTTP — credentials + session cookies cross the wire unencrypted.", *listen)
		logger.Printf("         Front wash-login with nginx/Caddy/Tailscale-serve for TLS and pass --cookie-secure,")
		logger.Printf("         or restrict --listen to 127.0.0.1 and tunnel (SSH -L / WireGuard).")
	}
	_ = *insecureListen // deprecated, retained for arg-compatibility

	// Always construct a passwd-backed UserLister; even the
	// --auth-test path uses it for the user-list UX (when not
	// disabled). NSS is intentionally avoided per the "no NSS,
	// no fancy syscalls" direction in docs/MULTIUSER.md — wash-login
	// reads /etc/passwd as plain text.
	lister := &login.PasswdLister{
		Path:   *passwdPath,
		MinUID: uint32(*minUID),
		MaxUID: uint32(*maxUID),
	}

	// Auth backend selection:
	//   --auth-test set     ⇒ test backend (CI / dev)
	//   otherwise           ⇒ su -c true via PTY (production)
	var auth login.Authenticator
	if *authTest != "" {
		uid := uint32(*authTestUID)
		if uid == 0 {
			uid = uint32(os.Getuid())
		}
		gid := uint32(*authTestGID)
		if gid == 0 {
			gid = uint32(os.Getgid())
		}
		a, err := login.NewTestAuth(*authTest, uid, gid, *authTestShell)
		if err != nil {
			logger.Printf("%v", err)
			return 2
		}
		auth = a
		logger.Printf("auth: --auth-test (CI / dev backend)")
	} else {
		a, err := login.NewSuAuth(*suPath, lister.Lookup)
		if err != nil {
			logger.Printf("auth: %v", err)
			return 1
		}
		auth = a
		logger.Printf("auth: su via PTY")
	}

	keyPath := *secretKey
	if keyPath == "" {
		keyPath = defaultSecretPath()
	}
	signer, err := login.NewSigner(keyPath, *secretGenerate)
	if err != nil {
		logger.Printf("secret key: %v", err)
		return 1
	}
	logger.Printf("secret key: %s", keyPath)

	showUsers := *userList != "hide"

	cfg := login.Config{
		Auth:         auth,
		Signer:       signer,
		TTL:          *cookieTTL,
		CookieSecure: *cookieSecure,
		Logger:       logger,
		Users:        lister,
		ShowUsers:    showUsers,
	}
	if !*noHandoff {
		routerBin := resolveRouterBinary(*routerBinary)
		if routerBin == "" {
			logger.Printf("could not locate wash-router; pass --router-binary or use --no-handoff to disable /ws")
			return 2
		}
		runRootPath := *runRoot
		if runRootPath == "" {
			runRootPath = defaultRunRoot()
		}
		sessions := login.NewProcRegistry()
		cfg.Sessions = sessions
		cfg.Spawner = &login.Spawner{
			RouterBinary: routerBin,
			AllowUID:     uint32(os.Getuid()),
			RunRoot:      runRootPath,
			AppsDir:      *appsDir,
			IdleTimeout:  *idleTimeout,
			MaxPerUID:    *maxSessions,
			Sessions:     sessions,
		}
		logger.Printf("handoff enabled: router=%s run-root=%s allow-uid=%d max-sessions-per-uid=%d", routerBin, runRootPath, os.Getuid(), *maxSessions)
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
