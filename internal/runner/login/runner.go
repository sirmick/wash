// Package loginrun is the wash-login CLI's compiled-in form.
//
// cmd/wash-login is a tiny shim that calls Run with os.Args[1:];
// flag parsing and lifecycle live here. Mirrors the routerrun
// package's split so tests and a future multi-call wash binary can
// drive both runners without leaking flag state.
package loginrun

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"github.com/sirmick/wash/internal/version"
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
	"github.com/sirmick/wash/internal/mdns"
	"github.com/sirmick/wash/internal/tlsutil"
)

const (
	defaultListen     = "0.0.0.0:10000"
	systemSecretPath  = "/etc/wash/secret.key"
	userSecretSubpath = "wash/secret.key"
	// advertiseSSHPort is the SSH port we announce over mDNS as the
	// remote-apps connect target. The conventional 22; matches the
	// per-session router's advertisement so the two are byte-identical.
	advertiseSSHPort = 22
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

// defaultLoginTLSCertKey is where the self-signed HTTPS cert/key are
// cached when the operator didn't pass --tls-cert/--tls-key. It sits
// beside the secret key (/etc/wash/tls for a privileged production
// install, else $XDG_CONFIG_HOME/wash/tls or ~/.config/wash/tls) so a
// restart reuses the same cert and the browser's one-time trust decision
// keeps holding. Never per-pid — the cert must be stable.
func defaultLoginTLSCertKey() (certPath, keyPath string) {
	var dir string
	switch {
	case canWriteTo(filepath.Dir(systemSecretPath)):
		dir = filepath.Join(filepath.Dir(systemSecretPath), "tls")
	case os.Getenv("XDG_CONFIG_HOME") != "":
		dir = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wash", "tls")
	case os.Getenv("HOME") != "":
		dir = filepath.Join(os.Getenv("HOME"), ".config", "wash", "tls")
	default:
		dir = filepath.Join(filepath.Dir(systemSecretPath), "tls")
	}
	return filepath.Join(dir, "login-cert.pem"), filepath.Join(dir, "login-key.pem")
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
	cookieSecure := fs.Bool("cookie-secure", false, "force the Secure flag on session cookies. Implied when wash-login terminates its own TLS (the default); set it explicitly when a TLS terminator is in front and --http is used. Leave off only for plain-HTTP loopback dev.")
	noTLS := fs.Bool("http", false, "serve plain HTTP instead of the default self-signed HTTPS. Browser secure-context features (clipboard copy/paste) need HTTPS, so this is only for a TLS-terminating front (nginx/Caddy/Tailscale-serve) or trusted-loopback dev.")
	tlsCert := fs.String("tls-cert", "", "PEM certificate for the HTTPS listener. Empty ⇒ a cached self-signed cert under /etc/wash/tls (when writable) or $XDG_CONFIG_HOME/wash/tls, minted on first run and reused across restarts. Ignored with --http.")
	tlsKey := fs.String("tls-key", "", "PEM private key matching --tls-cert. Empty ⇒ the cached self-signed key. Ignored with --http.")
	allowInsecureCookie := fs.Bool("allow-insecure-cookie", false, "permit the production auth backend to bind a non-loopback address WITHOUT --cookie-secure. Off by default: an unguarded public bind ships session cookies + passwords over plain HTTP. Only pass this when something else (a tunnel you trust) terminates TLS but can't set Secure.")
	trustedProxies := fs.String("trusted-proxies", "", "comma-separated CIDRs of TLS terminators whose X-Forwarded-For header is believed (for audit logs + the per-IP /auth rate-limit key). Empty ⇒ XFF ignored, RemoteAddr used.")
	maxAuthFails := fs.Int("auth-max-fails", 0, "failed /auth attempts (per username and per client IP) within --auth-window before lockout. Zero uses the built-in default (5).")
	authWindow := fs.Duration("auth-window", 0, "sliding window over which failed /auth attempts are counted. Zero uses the built-in default (15m).")
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
		fmt.Printf("wash-login %s\n", version.Version)
		return 0
	}

	logger := log.New(os.Stderr, "wash-login ", log.LstdFlags|log.Lmsgprefix)

	// wash-login terminates its own self-signed TLS by default, so
	// cookies + credentials are encrypted and get the Secure flag.
	// --http opts out for a front-terminated / loopback deploy, which
	// re-arms the plain-HTTP safety checks below.
	tlsEnabled := !*noTLS
	secureCookie := *cookieSecure || tlsEnabled

	// Non-loopback bind on plain HTTP means credentials + session
	// cookies cross the wire unencrypted. For the production su
	// backend that's a hard error — refuse to start rather than ship
	// a silently-insecure service. This only applies to --http (no
	// local TLS): the default HTTPS listener is always safe. The dev/CI
	// --auth-test backend is exempt (e2e binds non-loopback freely), and
	// --allow-insecure-cookie is the escape hatch for a trusted tunnel.
	if !tlsEnabled && !isLoopback(*listen) && !secureCookie {
		switch {
		case *authTest != "":
			logger.Printf("WARNING: --listen %s on plain HTTP (--auth-test dev backend) — cookies cross the wire unencrypted.", *listen)
		case *allowInsecureCookie:
			logger.Printf("WARNING: --listen %s on plain HTTP with --allow-insecure-cookie — credentials + cookies cross the wire unencrypted.", *listen)
		default:
			logger.Printf("refusing to bind %s on plain HTTP: session cookies + passwords would cross the wire unencrypted.", *listen)
			logger.Printf("  Drop --http to serve the built-in self-signed HTTPS,")
			logger.Printf("  front wash-login with nginx/Caddy/Tailscale-serve for TLS and pass --cookie-secure,")
			logger.Printf("  restrict --listen to 127.0.0.1 and tunnel (SSH -L / WireGuard),")
			logger.Printf("  or pass --allow-insecure-cookie if a trusted tunnel already terminates TLS.")
			return 2
		}
	}
	_ = *insecureListen // deprecated, retained for arg-compatibility

	// Parse trusted-proxy CIDRs up front so a typo fails fast.
	trusted, err := parseTrustedProxies(*trustedProxies)
	if err != nil {
		logger.Printf("--trusted-proxies: %v", err)
		return 2
	}

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
		Auth:           auth,
		Signer:         signer,
		TTL:            *cookieTTL,
		CookieSecure:   secureCookie,
		Logger:         logger,
		Users:          lister,
		ShowUsers:      showUsers,
		MaxAuthFails:   *maxAuthFails,
		AuthWindow:     *authWindow,
		TrustedProxies: trusted,
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

	// Load the self-signed cert (or the operator's --tls-cert) before we
	// announce readiness, so a cert failure aborts before the listener.
	var tlsCfg *tls.Config
	if tlsEnabled {
		certPath, keyPath := *tlsCert, *tlsKey
		if certPath == "" && keyPath == "" {
			certPath, keyPath = defaultLoginTLSCertKey()
		}
		cert, terr := tlsutil.LoadOrGenerate(certPath, keyPath, logger.Printf)
		if terr != nil {
			logger.Printf("tls: %v", terr)
			return 1
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	scheme := "https"
	if !tlsEnabled {
		scheme = "http"
	}
	logger.Printf("wash-login %s listening on %s://%s (cookie-secure=%v cookie-ttl=%s)",
		version.Version, scheme, *listen, secureCookie, *cookieTTL)

	// Advertise this box on the LAN so a peer's wash-connect can discover it
	// even with nobody logged in. The per-session router (com.wash.remote)
	// also advertises, but only once a user has a live session — login is
	// always up, so without this an idle multi-user box is invisible.
	// Advertise-only: browsing for peers happens in each user's connect
	// window. Best-effort — a socket failure is logged, never fatal — and
	// honours WASH_DISCOVERY_NO_ADVERTISE via HostServiceInfo. Two identical
	// advertisers on one box (login + an active session's router) is fine:
	// mDNS expects multiple responders and a peer dedups by instance.
	if adv := mdns.HostServiceInfo(advertiseSSHPort); adv != nil {
		if mdnsSrv, err := mdns.New(mdns.Options{Advertise: adv, Logf: logger.Printf}); err != nil {
			logger.Printf("discovery: mdns advertise failed: %v (continuing without it)", err)
		} else {
			logger.Printf("discovery: advertising host=%q ips=%v on _wash._tcp.local", adv.Instance, adv.IPv4)
			defer mdnsSrv.Close()
		}
	} else {
		logger.Printf("discovery: NOT advertising (no routable IPv4, or WASH_DISCOVERY_NO_ADVERTISE set)")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         tlsCfg,
	}

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		logger.Printf("listen %s: %v", *listen, err)
		return 1
	}

	errs := make(chan error, 1)
	// ServeTLS with empty cert/key paths uses httpSrv.TLSConfig, which we
	// populated above with the self-signed (or operator-supplied) cert.
	go func() {
		if tlsCfg != nil {
			errs <- httpSrv.ServeTLS(listener, "", "")
		} else {
			errs <- httpSrv.Serve(listener)
		}
	}()
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

// parseTrustedProxies turns a comma-separated CIDR list into
// *net.IPNet values. Empty input is valid (no trusted proxies). A
// bare IP is accepted as a /32 or /128.
func parseTrustedProxies(s string) ([]*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			if ip := net.ParseIP(part); ip != nil {
				if ip.To4() != nil {
					part += "/32"
				} else {
					part += "/128"
				}
			}
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", part, err)
		}
		out = append(out, n)
	}
	return out, nil
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
