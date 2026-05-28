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
	"log"
	"net/http"
	"syscall"
	"time"
)

//go:embed assets/*.html
var assetsFS embed.FS

// Server is the HTTP handler set for wash-login. Construct via
// NewServer, then call ServeHTTP / mount onto your own listener.
type Server struct {
	auth       Authenticator
	signer     *Signer
	ttl        time.Duration
	cookieSec  bool // Secure flag on cookie — false in dev/loopback HTTP
	log        *log.Logger
	tplLogin   *template.Template
	tplWelcome *template.Template
	sessions   SessionRegistry      // nil = /ws handoff disabled (M2-only mode)
	spawner    *Spawner             // nil = /ws handoff disabled
	killer     func(pid int) error  // overrideable for tests; default syscall.Kill(pid, SIGTERM)
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
	killer := cfg.Killer
	if killer == nil {
		killer = func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
	}
	return &Server{
		auth:       cfg.Auth,
		signer:     cfg.Signer,
		ttl:        cfg.TTL,
		cookieSec:  cfg.CookieSecure,
		log:        cfg.Logger,
		tplLogin:   tplLogin,
		tplWelcome: tplWelcome,
		sessions:   cfg.Sessions,
		spawner:    cfg.Spawner,
		killer:     killer,
	}, nil
}

// Handler returns an http.Handler with all wash-login routes mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/auth", s.handleAuth)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/ws", s.handleWS)
	return mux
}

// handleWS is the M3 handoff endpoint. Validates the cookie,
// resolves the target session (auto-attach if exactly one exists;
// auto-spawn if none), then SCM_RIGHTS the browser-facing TCP fd
// to the per-user router's ctl socket. wash-login is not in the
// data path after this returns.
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
	// Look up existing sessions for this uid. Spawn if there are
	// none. For now any count > 0 just picks the first; the picker
	// (M4) makes this a real choice.
	sessions, err := s.sessions.List(payload.UID)
	if err != nil {
		s.log.Printf("ws: list sessions for uid=%d: %v", payload.UID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var target Session
	switch {
	case len(sessions) == 0:
		// Need to spawn. We need an Identity for the spawner — the
		// cookie payload has uid + name; gid + shell aren't in the
		// cookie. Pull them from the configured auth backend's view
		// of this user. For the M3 dev path with --auth-test, the
		// identity we mint here uses gid=this-process's-gid + a
		// stub shell. M5 / shadow-auth resolves these properly via
		// the user database.
		id := Identity{
			UID:   payload.UID,
			GID:   uint32(0),
			Name:  payload.Name,
			Shell: "/bin/sh",
		}
		spawned, err := s.spawner.Spawn(id, payload.Name)
		if err != nil {
			s.log.Printf("ws: spawn for uid=%d: %v", payload.UID, err)
			http.Error(w, "could not start session", http.StatusInternalServerError)
			return
		}
		target = spawned
		s.log.Printf("ws: spawned new session sessid=%s pid=%d for uid=%d", spawned.SessID, spawned.Pid, payload.UID)
	default:
		target = sessions[0]
		s.log.Printf("ws: attaching to existing sessid=%s pid=%d for uid=%d", target.SessID, target.Pid, payload.UID)
	}

	// Hijack BEFORE writing any response. websocket.Accept on the
	// router side will write the 101 once it parses the synthesized
	// request bytes.
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
		// Best-effort: write a 502 directly to the hijacked conn
		// so the browser sees something less mysterious than a
		// dropped connection. We've already lost the http.Server
		// response path.
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\nhandoff failed\n"))
		_ = conn.Close()
		return
	}
}

// handleRoot dispatches based on auth state: authed users get the
// welcome page; unauthed users redirect to /login.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	p, ok := s.identityFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = s.tplWelcome.Execute(w, p)
}

// handleLogin renders the login form. Authed users get bounced to /
// so re-visiting /login after a successful auth lands you back on the
// welcome page rather than asking again.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identityFromRequest(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data := struct{ Error string }{}
	if e := r.URL.Query().Get("err"); e != "" {
		data.Error = e
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
	id, err := s.auth.Authenticate(user, password)
	if err != nil {
		// Log on the server with detail; show a generic message.
		s.log.Printf("auth: user=%q from=%s rejected: %v", user, clientIP(r), err)
		http.Redirect(w, r, "/login?err=Invalid+credentials", http.StatusFound)
		return
	}
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
	s.log.Printf("auth: user=%q uid=%d from=%s ok", id.Name, id.UID, clientIP(r))
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleLogout clears the cookie and, when authed, optionally
// SIGTERMs the user's running router(s). Query knobs:
//
//   /logout                       cookie clear only.
//   /logout?end_session=<sessid>  cookie clear + SIGTERM that sessid.
//   /logout?end_all=true          cookie clear + SIGTERM every router
//                                 owned by the authed uid.
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
// expired, bad signature, malformed). Failures are NOT logged here —
// every page load on a logged-out session would otherwise spam.
func (s *Server) identityFromRequest(r *http.Request) (Payload, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Payload{}, false
	}
	p, err := s.signer.Verify(c.Value)
	if err != nil {
		return Payload{}, false
	}
	return p, true
}

// clientIP returns a usable peer identifier for log lines. nginx /
// other TLS terminators stuff the real client IP into
// X-Forwarded-For; honour it when present so audit lines aren't all
// "127.0.0.1" on production deployments. Trust here is bounded — we
// only log this string; it isn't a security boundary.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	return r.RemoteAddr
}
