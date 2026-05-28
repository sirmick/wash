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
	"time"
)

//go:embed assets/*.html
var assetsFS embed.FS

// Server is the HTTP handler set for wash-login. Construct via
// NewServer, then call ServeHTTP / mount onto your own listener.
type Server struct {
	auth     Authenticator
	signer   *Signer
	ttl      time.Duration
	cookieSec bool // Secure flag on cookie — false in dev/loopback HTTP
	log      *log.Logger
	tplLogin   *template.Template
	tplWelcome *template.Template
}

// Config drives Server construction.
type Config struct {
	Auth     Authenticator
	Signer   *Signer
	TTL      time.Duration
	// CookieSecure controls the cookie's Secure flag. Set false for
	// plain-HTTP deployments (loopback dev) and true behind any TLS
	// terminator (nginx, Caddy, Tailscale-serve).
	CookieSecure bool
	Logger       *log.Logger
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
	return &Server{
		auth:       cfg.Auth,
		signer:     cfg.Signer,
		ttl:        cfg.TTL,
		cookieSec:  cfg.CookieSecure,
		log:        cfg.Logger,
		tplLogin:   tplLogin,
		tplWelcome: tplWelcome,
	}, nil
}

// Handler returns an http.Handler with all wash-login routes mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/auth", s.handleAuth)
	mux.HandleFunc("/logout", s.handleLogout)
	return mux
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

// handleLogout clears the cookie regardless of state. Accepting GET
// makes simple browser navigation work (e.g. a future "Log out" link
// from inside wash-session) without requiring JS. Routers' SIGTERM
// logic lands in M3 — for now this is cookie-only.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSec,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
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
