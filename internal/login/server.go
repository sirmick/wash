package login

// HTTP server for wash-login.
//
// Routes:
//
//   GET  /          → if cookie valid, the post-auth landing page;
//                     else redirect to /login.
//   GET  /login     → login form (login.html).
//   POST /auth      → validate (user, password), set cookie, 302 /.
//   GET  /logout    → clear cookie, 302 /login.
//   POST /logout    → same as GET (browsers tend to fetch GET for
//                     redirects after a POST; we accept both so a
//                     form-button logout works without JS).
//
// All non-asset routes require POST for state-changing actions; the
// signed cookie is SameSite=Strict + HttpOnly so CSRF via the
// browser's ambient credentials is blocked.

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/httpsec"
)

//go:embed assets/*.html
var assetsFS embed.FS

// shellAssetsFS holds the shell-runtime files (index.html, shell.js,
// icons.svg, vendor/*) that wash-login serves under / for authed
// users. Copy-staged from internal/runner/router/assets by the
// Makefile's $(LOGIN_SHELL_STAMP) rule. Tagged all: so the brotli
// .br siblings come along too.
//
//go:embed all:assets/shell
var shellAssetsFS embed.FS

// Server is the HTTP handler set for wash-login. Construct via
// NewServer, then call ServeHTTP / mount onto your own listener.
type Server struct {
	auth          Authenticator
	signer        *Signer
	ttl           time.Duration
	cookieSec     bool // Secure flag on cookie — false in dev/loopback HTTP
	log           *log.Logger
	tplLogin      *template.Template
	tplWelcome    *template.Template
	tplPicker     *template.Template
	tplNewSession *template.Template
	sessions      SessionRegistry     // nil = /ws handoff disabled (M2-only mode)
	spawner       *Spawner            // nil = /ws handoff disabled
	killer        func(pid int) error // overrideable for tests; default syscall.Kill(pid, SIGTERM)
	users         UserLister          // nil = no user list on login form
	showUsers     bool                // false = always omit list even if users != nil
	authLimit     *rateLimiter        // /auth failure throttle (never nil)
	trustedXFF    []*net.IPNet        // peers whose X-Forwarded-For we believe
	hostAllow     []string            // Host-header allowlist; empty = permissive
}

// Config drives Server construction.
type Config struct {
	Auth   Authenticator
	Signer *Signer
	TTL    time.Duration
	// CookieSecure controls the cookie's Secure flag. Set false for
	// plain-HTTP deployments (loopback dev) and true behind any TLS
	// terminator (nginx, Caddy, Tailscale-serve).
	CookieSecure bool
	Logger       *log.Logger
	// Sessions + Spawner enable the M3 /ws handoff path. Both
	// non-nil ⇒ /ws is live. Either nil ⇒ /ws returns 503 (M2 mode
	// for the auth-flow tests).
	Sessions SessionRegistry
	Spawner  *Spawner
	// Killer is the function used for /logout?end_session and
	// ?end_all=true SIGTERM. Production passes nil ⇒ syscall.Kill
	// with SIGTERM. Tests override to capture pids without
	// touching real processes.
	Killer func(pid int) error
	// Users powers the user-list tiles on the login page. nil
	// hides the list (text-only login). ShowUsers gates display
	// even when Users is non-nil — sysadmins who want to suppress
	// the user enumeration pass --user-list=hide.
	Users     UserLister
	ShowUsers bool
	// MaxAuthFails / AuthWindow tune the /auth failure throttle.
	// Zero values fall back to defaultMaxAuthFails / defaultAuthWindow.
	MaxAuthFails int
	AuthWindow   time.Duration
	// TrustedProxies are the peer networks (the TLS terminator in
	// front) whose X-Forwarded-For header we believe — for both audit
	// logging and the per-IP rate-limit key. Empty ⇒ XFF is ignored
	// and RemoteAddr is used, so a direct client can't spoof its way
	// into another IP's rate-limit bucket.
	TrustedProxies []*net.IPNet
	// HostAllowlist, when non-empty, restricts which Host header values
	// the login front accepts — a DNS-rebinding defense. Empty (default)
	// is permissive: every Host passes, so existing deployments are
	// unaffected. Loopback names are always accepted; add the public
	// hostname(s) of the login front here to switch on enforcement.
	HostAllowlist []string
}

// NewServer returns a fully-wired Server. The Auth and Signer must
// be non-nil; TTL defaults to DefaultCookieTTL.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Auth == nil {
		return nil, errors.New("Auth is required")
	}
	if cfg.Signer == nil {
		return nil, errors.New("Signer is required")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultCookieTTL
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	tplLogin, err := template.ParseFS(assetsFS, "assets/login.html")
	if err != nil {
		return nil, fmt.Errorf("parse login.html: %w", err)
	}
	tplWelcome, err := template.ParseFS(assetsFS, "assets/welcome.html")
	if err != nil {
		return nil, fmt.Errorf("parse welcome.html: %w", err)
	}
	tplPicker, err := template.ParseFS(assetsFS, "assets/picker.html")
	if err != nil {
		return nil, fmt.Errorf("parse picker.html: %w", err)
	}
	tplNewSession, err := template.ParseFS(assetsFS, "assets/new-session.html")
	if err != nil {
		return nil, fmt.Errorf("parse new-session.html: %w", err)
	}
	killer := cfg.Killer
	if killer == nil {
		killer = func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
	}
	return &Server{
		auth:          cfg.Auth,
		signer:        cfg.Signer,
		ttl:           cfg.TTL,
		cookieSec:     cfg.CookieSecure,
		log:           cfg.Logger,
		tplLogin:      tplLogin,
		tplWelcome:    tplWelcome,
		tplPicker:     tplPicker,
		tplNewSession: tplNewSession,
		sessions:      cfg.Sessions,
		spawner:       cfg.Spawner,
		killer:        killer,
		users:         cfg.Users,
		showUsers:     cfg.ShowUsers,
		authLimit:     newRateLimiter(cfg.MaxAuthFails, cfg.AuthWindow),
		trustedXFF:    cfg.TrustedProxies,
		hostAllow:     cfg.HostAllowlist,
	}, nil
}

// Handler returns an http.Handler with all wash-login routes mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/auth", s.handleAuth)
	mux.HandleFunc("/auth/check", s.handleAuthCheck)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/ws/s/", s.handleWSSpecific)
	// Generic ingress: /app/<token>/* belongs to an embedded web app
	// (code-server et al.) published on the user's per-router. The
	// router binds no TCP in multi-user mode, so we forward the browser
	// fd to it with the same SCM_RIGHTS handoff used for /ws; the router
	// serves /app/ over that conn (router.serveHandoffHTTP). More
	// specific than "/", so the mux routes it here.
	mux.HandleFunc("/app/", s.handleAppIngress)
	mux.HandleFunc("/sessions", s.handleSessionsPage)
	mux.HandleFunc("/sessions/new", s.handleSessionsNew)
	mux.HandleFunc("/sessions/end", s.handleSessionsEnd)
	// Shell-runtime assets that bootstrap the desktop after auth.
	// Copy-staged from the router's embedded set; served verbatim
	// here so browsers get them from wash-login (the only HTTP
	// origin in multi-user deployments — per-user routers are
	// Unix-socket only). Authed users get the shell page at /.
	if sub, err := fs.Sub(shellAssetsFS, "assets/shell"); err == nil {
		// no-cache: these bundles are unversioned and change on every
		// wash upgrade. Without it a browser keeps a stale module past an
		// upgrade — e.g. a cached /vendor/wash-ui.js missing a freshly
		// added export — and apps that import it die with a module
		// mismatch. Mirrors the router's own shell-asset headers
		// (router/http.go handleRoot).
		shellHandler := noCacheStatic(http.FileServer(http.FS(sub)))
		for _, prefix := range []string{"/shell.js", "/icons.svg", "/wash-logo.svg", "/vendor/"} {
			mux.Handle(prefix, shellHandler)
		}
	}
	return s.harden(mux)
}

// harden wraps the login mux with the Host-header allowlist (a DNS-
// rebinding defense, permissive by default) and the baseline security
// headers — skipping the headers on /app/<token>/ so the embedded
// backend keeps its own framing/CSP policy.
func (s *Server) harden(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpsec.HostAllowed(r.Host, "", s.hostAllow) {
			s.log.Printf("login: reject host=%q from=%s reason=not-in-allowlist", r.Host, r.RemoteAddr)
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/app/") {
			httpsec.SetSecurityHeaders(w.Header())
		}
		next.ServeHTTP(w, r)
	})
}

// noCacheStatic wraps a static-asset handler so browsers always
// revalidate the unversioned shell-runtime bundles, picking up a new
// build on the next navigation instead of serving a stale, incoherent
// module set after an upgrade.
func noCacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		h.ServeHTTP(w, r)
	})
}

// handleWS resolves the target session (auto-attach if exactly one
// exists; auto-spawn if none; redirect to picker if there are
// many) and SCM_RIGHTS-hands the browser-facing TCP fd to the
// per-user router's ctl socket. wash-login is not in the data path
// after this returns.
//
// If Sessions / Spawner are not configured (M2 mode), returns 503.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil || s.spawner == nil {
		http.Error(w, "session handoff not configured", http.StatusServiceUnavailable)
		return
	}
	payload, ok := s.identityFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	sessions, err := s.sessions.List(payload.UID)
	if err != nil {
		s.log.Printf("ws: list sessions for uid=%d: %v", payload.UID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var target Session
	switch {
	case len(sessions) == 0:
		spawned, err := s.spawnFor(payload, payload.Name)
		if err != nil {
			s.log.Printf("ws: spawn for uid=%d: %v", payload.UID, err)
			http.Error(w, "could not start session", http.StatusInternalServerError)
			return
		}
		target = spawned
		s.log.Printf("ws: spawned new session sessid=%s pid=%d for uid=%d", spawned.SessID, spawned.Pid, payload.UID)
	case len(sessions) == 1:
		target = sessions[0]
		s.log.Printf("ws: attaching to existing sessid=%s pid=%d for uid=%d", target.SessID, target.Pid, payload.UID)
	default:
		// More than one session → punt to the picker. The browser
		// is mid-WS-upgrade right now; redirect mid-handshake. The
		// shell.js / chrome doesn't issue a top-level Upgrade
		// anyway — it embeds an <a> link to /sessions when needed.
		http.Redirect(w, r, "/sessions", http.StatusFound)
		return
	}

	s.handoffToSession(w, r, target)
}

// handleWSSpecific implements /ws/s/<sessid>/ — direct attach to a
// named session. The sessid must belong to the authed uid; unknown
// or foreign sessids 404. No auto-spawn here — the picker is the
// only place that mints new sessions explicitly.
func (s *Server) handleWSSpecific(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil || s.spawner == nil {
		http.Error(w, "session handoff not configured", http.StatusServiceUnavailable)
		return
	}
	payload, ok := s.identityFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Strip /ws/s/ prefix and any trailing slash to extract sessid.
	rest := strings.TrimPrefix(r.URL.Path, "/ws/s/")
	sessid := strings.TrimSuffix(rest, "/")
	if sessid == "" || strings.Contains(sessid, "/") {
		http.NotFound(w, r)
		return
	}
	sessions, err := s.sessions.List(payload.UID)
	if err != nil {
		s.log.Printf("ws/s: list sessions for uid=%d: %v", payload.UID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, sess := range sessions {
		if sess.SessID == sessid {
			s.log.Printf("ws/s: attaching to sessid=%s pid=%d for uid=%d", sessid, sess.Pid, payload.UID)
			s.handoffToSession(w, r, sess)
			return
		}
	}
	// Unknown sessid for this uid — 404 (don't leak whether it
	// exists for a different uid).
	http.NotFound(w, r)
}

// handleAppIngress proxies a generic-ingress request (/app/<token>/...)
// to the authenticated user's per-router. Ingress backends (code-server
// and friends) publish on a specific router and are reached via
// /app/<token>/; the router binds no TCP in multi-user mode, so we hand
// the browser fd to it with the same SCM_RIGHTS handoff used for /ws and
// let the router serve /app/ over that conn (router.serveHandoffHTTP).
//
// Auth is required even though the 128-bit token is itself a capability:
// the iframe always carries the session cookie (same origin), so demand
// it as defense-in-depth and to keep pre-auth traffic off the per-user
// routers entirely.
//
// v1 handles the single-session case — the common one. The opaque token
// can't be mapped to a specific session here, so with several live
// sessions we can't know which router owns it; that needs a
// session-namespaced ingress path and is left as a follow-up.
func (s *Server) handleAppIngress(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil || s.spawner == nil {
		http.Error(w, "session handoff not configured", http.StatusServiceUnavailable)
		return
	}
	payload, ok := s.identityFromRequest(r)
	if !ok {
		// An iframe can't meaningfully follow a 302 to the login page,
		// so 401 rather than redirect.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sessions, err := s.sessions.List(payload.UID)
	if err != nil {
		s.log.Printf("app: list sessions for uid=%d: %v", payload.UID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	switch len(sessions) {
	case 0:
		http.Error(w, "no active session", http.StatusBadGateway)
	case 1:
		s.handoffToSession(w, r, sessions[0])
	default:
		s.log.Printf("app: ingress with %d sessions for uid=%d not supported yet", len(sessions), payload.UID)
		http.Error(w, "ingress is not supported with multiple sessions yet", http.StatusServiceUnavailable)
	}
}

// handoffToSession is the common tail end of /ws and /ws/s/<sessid>/:
// hijack the conn, SCM_RIGHTS the fd to the named session's ctl
// socket, surface a 502 on the hijacked conn if anything fails.
func (s *Server) handoffToSession(w http.ResponseWriter, r *http.Request, target Session) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacker not supported", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		s.log.Printf("ws hijack: %v", err)
		return
	}
	if err := Handoff(r, conn, brw, target.Sock); err != nil {
		s.log.Printf("ws handoff to %s: %v", target.Sock, err)
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\nhandoff failed\n"))
		_ = conn.Close()
		return
	}
}

// spawnFor materialises an Identity from the cookie payload and
// runs the Spawner. Used by /ws auto-spawn and /sessions/new.
// gid + shell aren't in the cookie; we stub them with the wash-login
// process's gid + /bin/sh, which is correct for the --auth-test
// dev path. Real shadow-auth resolves these from the user database
// at auth time and the picker spawn re-derives from a richer cookie
// payload — out of v1 scope.
func (s *Server) spawnFor(payload Payload, name string) (Session, error) {
	id := Identity{
		UID:   payload.UID,
		GID:   uint32(0),
		Name:  payload.Name,
		Shell: "/bin/sh",
	}
	return s.spawner.Spawn(id, name)
}

// pickerView is the template data for picker.html. Time fields are
// formatted server-side because the template only does string
// interpolation.
type pickerView struct {
	Name     string
	UID      uint32
	Sessions []pickerSessionRow
	Error    string
	MaxCap   int
	AtCap    bool
}

type pickerSessionRow struct {
	SessID string
	Name   string
	Sock   string
	Pid    int
}

// handleSessionsPage renders the picker. Lists all sessions owned
// by the authed uid; each row gets [Attach] and [End] buttons,
// plus a "+ new" form at the bottom and a separate "Log out
// everywhere" form.
func (s *Server) handleSessionsPage(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		http.Error(w, "sessions not configured", http.StatusServiceUnavailable)
		return
	}
	payload, ok := s.identityFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	sessions, err := s.sessions.List(payload.UID)
	if err != nil {
		s.log.Printf("sessions list: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rows := make([]pickerSessionRow, 0, len(sessions))
	for _, sess := range sessions {
		rows = append(rows, pickerSessionRow{
			SessID: sess.SessID,
			Name:   sess.Name,
			Sock:   sess.Sock,
			Pid:    sess.Pid,
		})
	}
	view := pickerView{
		Name:     payload.Name,
		UID:      payload.UID,
		Sessions: rows,
		Error:    r.URL.Query().Get("err"),
	}
	if s.spawner != nil && s.spawner.MaxPerUID > 0 {
		view.MaxCap = s.spawner.MaxPerUID
		view.AtCap = len(rows) >= s.spawner.MaxPerUID
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = s.tplPicker.Execute(w, view)
}

// handleSessionsNew handles GET (render the name-prompt page) and
// POST (spawn the session, redirect to /?s=<sessid>).
//
// GET path is reached either when a user clicked "New session" on
// the login form (handleAuth redirects here after creds check), or
// when they followed an explicit link from /sessions.
//
// POST spawns via spawnFor; on success the browser is sent to
// /?s=<sessid> so handleRoot serves shell.html and shell.js opens
// /ws/s/<sessid>/ directly (no auto-pick race).
func (s *Server) handleSessionsNew(w http.ResponseWriter, r *http.Request) {
	if s.spawner == nil || s.sessions == nil {
		http.Error(w, "spawn not configured", http.StatusServiceUnavailable)
		return
	}
	payload, ok := s.identityFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		data := struct {
			Name        string
			UID         uint32
			DefaultName string
		}{
			Name:        payload.Name,
			UID:         payload.UID,
			DefaultName: time.Now().Format("2006-01-02 15:04"),
		}
		_ = s.tplNewSession.Execute(w, data)
		return
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		if name == "" {
			name = time.Now().Format("2006-01-02 15:04")
		}
		if !validSessionName(name) {
			http.Redirect(w, r, "/sessions?err="+url.QueryEscape("invalid session name"), http.StatusFound)
			return
		}
		spawned, err := s.spawnFor(payload, name)
		if err != nil {
			if errors.Is(err, ErrSessionCap) {
				http.Redirect(w, r, "/sessions?err="+url.QueryEscape("session cap reached — end an existing session first"), http.StatusFound)
				return
			}
			s.log.Printf("sessions/new spawn uid=%d: %v", payload.UID, err)
			http.Redirect(w, r, "/sessions?err="+url.QueryEscape("could not start session"), http.StatusFound)
			return
		}
		s.log.Printf("sessions/new: spawned sessid=%s pid=%d name=%q for uid=%d", spawned.SessID, spawned.Pid, name, payload.UID)
		http.Redirect(w, r, "/?s="+url.QueryEscape(spawned.SessID), http.StatusFound)
		return
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSessionsEnd SIGTERMs the named sessid IF it belongs to the
// authed uid, then redirects back to /sessions.
func (s *Server) handleSessionsEnd(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		http.Error(w, "sessions not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload, ok := s.identityFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	sessid := r.PostForm.Get("sessid")
	if sessid == "" {
		http.Redirect(w, r, "/sessions?err="+url.QueryEscape("missing sessid"), http.StatusFound)
		return
	}
	s.endSession(payload.UID, sessid)
	http.Redirect(w, r, "/sessions", http.StatusFound)
}

// validSessionName enforces the picker's name rules: 1-64 chars,
// only printable ASCII (no NUL, control chars, slashes, or unicode
// surprises that would make the argv-in-/proc-cmdline display
// ambiguous).
func validSessionName(n string) bool {
	if len(n) == 0 || len(n) > 64 {
		return false
	}
	for _, r := range n {
		if r < 0x20 || r > 0x7e {
			return false
		}
		if r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

// handleRoot serves the shell-runtime page for authed users.
// Unauthed → /login. Authed visitors take one of three paths:
//
//   - ?s=<sessid> set → serve shell.html. shell.js reads ?s= and
//     opens /ws/s/<sessid>/, attaching to that named session.
//   - No ?s=, ≥2 sessions exist → redirect to /sessions picker so
//     the user picks one explicitly (we can't auto-resolve and a
//     /ws auto-attempt would 302 mid-WS-upgrade, breaking it).
//   - No ?s=, 0 or 1 sessions → serve shell.html. shell.js opens
//     /ws, which auto-spawns (count=0) or auto-attaches (count=1).
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	payload, ok := s.identityFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if r.URL.Query().Get("s") == "" && s.sessions != nil {
		if list, err := s.sessions.List(payload.UID); err == nil && len(list) >= 2 {
			http.Redirect(w, r, "/sessions", http.StatusFound)
			return
		}
	}
	// Serve shell index.html. If the embedded asset is missing
	// (test build without web-shell), fall back to the picker
	// redirect so the user has somewhere to go.
	body, err := shellAssetsFS.ReadFile("assets/shell/index.html")
	if err != nil {
		http.Redirect(w, r, "/sessions", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	_, _ = w.Write(body)
}

// handleLogin renders the login form. Authed users get bounced to /
// so re-visiting /login after a successful auth lands you back on the
// picker rather than asking again. The form is templated with:
//
//   - Error string from ?err=...
//   - PreselectUser from ?user=... so clicking a user tile (which
//     navigates to /login?user=alice) lands the cursor on the
//     password field with the username pre-filled.
//   - Users []User when a UserLister is configured and ShowUsers
//     is set — rendered as clickable tiles above the form.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identityFromRequest(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	data := struct {
		Error         string
		PreselectUser string
		Users         []User
	}{
		Error:         r.URL.Query().Get("err"),
		PreselectUser: r.URL.Query().Get("user"),
	}
	if s.users != nil && s.showUsers {
		if list, err := s.users.List(); err == nil {
			data.Users = list
		} else {
			s.log.Printf("user list: %v", err)
		}
	}
	_ = s.tplLogin.Execute(w, data)
}

// handleAuth validates submitted credentials. On success: set cookie
// + redirect to /. On failure: bounce back to /login?err=... with a
// generic message — never distinguish "no such user" from "wrong
// password" in the user-visible text.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	user := r.PostForm.Get("user")
	password := r.PostForm.Get("password")
	if user == "" || password == "" {
		http.Redirect(w, r, "/login?err=Username+and+password+required", http.StatusFound)
		return
	}
	ip := s.clientIP(r)
	userKey, ipKey := "u:"+user, "ip:"+ip
	// Throttle before the credentials ever reach the Authenticator: a
	// locked-out attempt costs no PAM call and leaks no timing.
	if !s.authLimit.allowed(userKey, ipKey) {
		retry := s.authLimit.retryAfter(userKey, ipKey)
		s.log.Printf("auth: user=%q from=%s rate-limited (retry in %s)", user, ip, retry.Round(time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Round(time.Second).Seconds())))
		http.Redirect(w, r, "/login?err=Too+many+attempts.+Try+again+later.", http.StatusSeeOther)
		return
	}
	id, err := s.auth.Authenticate(user, password)
	if err != nil {
		// Count the failure against both the username and the client IP,
		// then show a generic message (detail goes to the server log).
		s.authLimit.fail(userKey, ipKey)
		s.log.Printf("auth: user=%q from=%s rejected: %v", user, ip, err)
		http.Redirect(w, r, "/login?err=Invalid+credentials", http.StatusFound)
		return
	}
	// Success clears any accumulated failures for this user + IP.
	s.authLimit.reset(userKey, ipKey)
	payload := Payload{
		UID:     id.UID,
		Name:    id.Name,
		Expires: time.Now().Add(s.ttl).Unix(),
	}
	cookie, err := s.signer.Sign(payload)
	if err != nil {
		s.log.Printf("sign cookie: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    cookie,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSec,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Unix(payload.Expires, 0),
	})
	s.log.Printf("auth: user=%q uid=%d from=%s ok", id.Name, id.UID, ip)

	// Dispatch on the action button the user clicked. "new" → name
	// prompt page → /sessions/new. "signin" (default; also what
	// "Enter from password input" submits) → count-based redirect:
	//   0 / 1 sessions: serve the shell at /
	//   ≥2 sessions: send to /sessions picker so the user picks
	switch r.PostForm.Get("action") {
	case "new":
		http.Redirect(w, r, "/sessions/new", http.StatusFound)
		return
	default:
		dst := "/"
		if s.sessions != nil {
			if list, err := s.sessions.List(payload.UID); err == nil && len(list) >= 2 {
				dst = "/sessions"
			}
		}
		http.Redirect(w, r, dst, http.StatusFound)
	}
}

// handleAuthCheck is the FE reconnect preflight: 204 when the session
// cookie is valid, 401 otherwise. The shell hits this when its WS
// handshake is refused, to tell an expired cookie ("go to login_url")
// apart from a transient router drop ("keep reconnecting"). It never
// redirects — a 302 here would be opaque to the fetch() caller, which
// is the whole bug we're routing around.
func (s *Server) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identityFromRequest(r); ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"authenticated":false,"login_url":"/login"}`)
}

// handleLogout clears the cookie and, when authed, optionally
// SIGTERMs the user's running router(s). Query knobs:
//
//	/logout                       cookie clear only.
//	/logout?end_session=<sessid>  cookie clear + SIGTERM that sessid.
//	/logout?end_all=true          cookie clear + SIGTERM every router
//	                              owned by the authed uid.
//
// end_session validates that the named sessid actually belongs to
// the authed uid before signaling — a logged-in user can't terminate
// someone else's session by guessing sessids.
//
// Accepting GET makes simple browser navigation work (e.g.
// wash-session's "Log out" menu item is a top-level navigation,
// not a POST). SameSite=Strict on the cookie blocks the
// cross-site CSRF angle.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	payload, authed := s.identityFromRequest(r)
	// Clear the cookie first so even an error in the SIGTERM path
	// still logs the browser out.
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSec,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})

	if authed && s.sessions != nil {
		q := r.URL.Query()
		switch {
		case q.Get("end_all") == "true":
			s.endAllSessions(payload.UID)
		case q.Get("end_session") != "":
			s.endSession(payload.UID, q.Get("end_session"))
		}
	}

	http.Redirect(w, r, "/login", http.StatusFound)
}

// endSession SIGTERMs the named sessid IF it's currently running
// and owned by uid. Silently no-ops if the sessid doesn't match a
// live session — a user retrying logout on a stale URL shouldn't
// get an error, and we don't want to leak existence information.
func (s *Server) endSession(uid uint32, sessid string) {
	sessions, err := s.sessions.List(uid)
	if err != nil {
		s.log.Printf("logout: list sessions uid=%d: %v", uid, err)
		return
	}
	for _, sess := range sessions {
		if sess.SessID == sessid {
			if err := s.killer(sess.Pid); err != nil {
				s.log.Printf("logout: SIGTERM pid=%d sessid=%s: %v", sess.Pid, sessid, err)
				return
			}
			s.log.Printf("logout: SIGTERM sessid=%s pid=%d uid=%d", sessid, sess.Pid, uid)
			return
		}
	}
}

// endAllSessions SIGTERMs every running session owned by uid.
func (s *Server) endAllSessions(uid uint32) {
	sessions, err := s.sessions.List(uid)
	if err != nil {
		s.log.Printf("logout: list sessions uid=%d: %v", uid, err)
		return
	}
	for _, sess := range sessions {
		if err := s.killer(sess.Pid); err != nil {
			s.log.Printf("logout: SIGTERM pid=%d sessid=%s: %v", sess.Pid, sess.SessID, err)
			continue
		}
		s.log.Printf("logout: SIGTERM sessid=%s pid=%d uid=%d (end_all)", sess.SessID, sess.Pid, uid)
	}
}

// identityFromRequest pulls and verifies the session cookie. Returns
// the payload + true on success, zero + false on any failure (missing,
// expired, bad signature, malformed). A missing cookie is NOT logged —
// every page load on a logged-out session would otherwise spam. A
// cookie that is present but fails verification IS logged (reason +
// client IP, never the value): that's either expiry or tampering, and
// both deserve a trail.
func (s *Server) identityFromRequest(r *http.Request) (Payload, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Payload{}, false
	}
	p, err := s.signer.Verify(c.Value)
	if err != nil {
		s.log.Printf("auth: cookie rejected from=%s path=%s: %v", s.clientIP(r), r.URL.Path, err)
		return Payload{}, false
	}
	return p, true
}

// clientIP returns the peer IP used for both audit log lines and the
// per-IP rate-limit bucket. A TLS terminator in front stuffs the real
// client address into X-Forwarded-For, but a *direct* client can set
// that header too — so we only believe XFF when the immediate peer
// (RemoteAddr) is in the configured trusted-proxy set. Otherwise an
// attacker would rotate a forged XFF to dodge per-IP throttling. We
// take the left-most XFF entry (the original client) and fall back to
// RemoteAddr's host when there's no trusted proxy or the header is
// absent.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && s.peerTrusted(host) {
		first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
		if first != "" {
			return first
		}
	}
	return host
}

// peerTrusted reports whether host (a bare IP) is in the trusted-proxy
// set whose X-Forwarded-For we honour.
func (s *Server) peerTrusted(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range s.trustedXFF {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
