package router

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/sirmick/wash/internal/httpsec"
)

// routerCookieName holds the token presented on the first ?token= load
// so subsequent requests (and the WS upgrade) don't carry it in the URL.
const routerCookieName = "wash_router"

// tokenPromptHTML is the 401 body shown when the token gate is on and
// the request lacks a valid token — the operator reopens the URL the
// router logged at startup.
const tokenPromptHTML = `<!doctype html>
<html lang="en"><meta charset="utf-8"><title>wash — token required</title>
<body style="background:#111;color:#eee;font:14px/1.4 system-ui,sans-serif;padding:2em">
<h1>wash router — token required</h1>
<p>This router is protected by a token. Open the URL it printed at
startup (<code>http://&lt;host&gt;:&lt;port&gt;/?token=…</code>), or
restart it with <code>--no-auth</code> for an unauthenticated
listener.</p>
</body>
</html>`

// tokenOK reports whether the request carries the router token via the
// wash_router cookie or a ?token= query param (constant-time compared).
// When the configured token is empty the gate is disabled and every
// request passes — the unix/byte-stream transports rely on this.
func (s *HTTPServer) tokenOK(r *http.Request) bool {
	token := s.router.cfg.AuthToken
	if token == "" {
		return true
	}
	if c, err := r.Cookie(routerCookieName); err == nil &&
		subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1 {
		return true
	}
	if q := r.URL.Query().Get("token"); q != "" &&
		subtle.ConstantTimeCompare([]byte(q), []byte(token)) == 1 {
		return true
	}
	return false
}

// fallbackIndexHTML is the placeholder shell served when no embedded
// assets are present. Production builds always embed the real shell;
// this is only hit by router-only test builds where the //go:embed
// stamp wasn't produced.
const fallbackIndexHTML = `<!doctype html>
<html lang="en"><meta charset="utf-8"><title>wash</title>
<body style="background:#111;color:#eee;font:14px/1.4 system-ui,sans-serif;padding:2em">
<h1>wash — Web Application SHell</h1>
<p>The router is running, but no shell-runtime bundle is embedded in
this build. Run <code>make</code> from the repo root to build the
shell into <code>cmd/wash-router/assets/</code>.</p>
</body>
</html>`

// HTTPServer wires the router into a net/http handler set: GET / and
// GET /assets/... serve the shell runtime; GET /ws upgrades to the
// shell WebSocket transport.
type HTTPServer struct {
	router *Router
	assets http.FileSystem
	mux    *http.ServeMux
}

// NewHTTPServer returns a ready-to-mount handler. assets may be nil.
// The assets FS is also stashed on the Router so the shell-session
// asset-pull handler (TShellAssetRead) can serve from it over the WS
// transport — same source of truth.
func NewHTTPServer(r *Router, assets http.FileSystem) *HTTPServer {
	r.SetAssets(assets)
	s := &HTTPServer{router: r, assets: assets, mux: http.NewServeMux()}
	s.mux.HandleFunc("/ws", s.handleWS)
	s.mux.HandleFunc("/auth/check", s.handleAuthCheck)
	s.mux.HandleFunc("/screenshot", s.handleScreenshot)
	// Generic ingress: /app/<token>/* reverse-proxies to an app-
	// published HTTP/WS backend. More specific than "/", so the mux
	// routes it here. See ingress.go.
	s.mux.HandleFunc("/app/", s.router.handleIngress)
	s.mux.HandleFunc("/", s.handleRoot)
	return s
}

// MaxScreenshotBytes caps the size of one uploaded PNG. Plenty for
// 4K desktops; rejects mistakes / abuse.
const MaxScreenshotBytes = 32 * 1024 * 1024

// handleScreenshot accepts a PNG body and writes it to
// cfg.ScreenshotDir as <RFC3339-ish timestamp>.png. The directory is
// created on first use. Response body is the saved filename (basename
// only — the client never sees the absolute path).
func (s *HTTPServer) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.tokenOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	dir := s.router.cfg.ScreenshotDir
	if dir == "" {
		http.Error(w, "screenshot capture disabled", http.StatusServiceUnavailable)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.router.log("screenshot mkdir %s: %v", dir, err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	body := http.MaxBytesReader(w, r.Body, MaxScreenshotBytes)
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		http.Error(w, "not a png", http.StatusUnsupportedMediaType)
		return
	}
	name := time.Now().UTC().Format("2006-01-02T15-04-05.000Z") + ".png"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		s.router.log("screenshot write %s: %v", path, err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	s.router.log("screenshot saved %s (%d bytes)", path, len(data))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, name)
}

// handleAuthCheck is the FE reconnect preflight: 204 when the request
// is authorized (or the gate is off), 401 otherwise. It never gates
// itself — it's how the shell tells "auth gone, reopen the token URL"
// apart from a transient socket drop. login_url is null here (the raw
// router has no login page to redirect to); wash-login's equivalent
// endpoint supplies "/login".
func (s *HTTPServer) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	if s.tokenOK(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"authenticated":false,"login_url":null}`)
}

func (s *HTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !httpsec.HostAllowed(r.Host, s.bindHost(), s.router.cfg.HostAllowlist) {
		s.router.log("router: rejected host=%q from=%s: not in HostAllowlist", r.Host, r.RemoteAddr)
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	// Baseline hardening headers on everything we serve ourselves, but
	// not on the /app/<token>/ ingress proxy — those responses carry the
	// embedded backend's own framing/CSP policy.
	if !strings.HasPrefix(r.URL.Path, "/app/") {
		httpsec.SetSecurityHeaders(w.Header())
	}
	s.mux.ServeHTTP(w, r)
}

// bindHost is the host part of cfg.Listen (without :port), used by the
// Host allowlist as an always-accepted name.
func (s *HTTPServer) bindHost() string {
	host, _, err := net.SplitHostPort(s.router.cfg.Listen)
	if err != nil {
		return s.router.cfg.Listen
	}
	return host
}

func (s *HTTPServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	if s.router.cfg.AuthToken != "" {
		// First load with ?token=: stamp the token into an HttpOnly
		// cookie and 302 to the same path WITHOUT the query, so the
		// token doesn't linger in history / Referer. (No Secure flag:
		// the raw router serves plain HTTP; the token is the same
		// secret already in the URL, and SameSite=Strict + HttpOnly
		// still apply.)
		if q := r.URL.Query().Get("token"); q != "" && s.tokenOK(r) {
			http.SetCookie(w, &http.Cookie{
				Name:     routerCookieName,
				Value:    q,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			u := *r.URL
			query := u.Query()
			query.Del("token")
			u.RawQuery = query.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusFound)
			return
		}
		if !s.tokenOK(r) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, tokenPromptHTML)
			return
		}
	}
	if s.assets == nil {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fallbackIndexHTML))
		return
	}
	// Iterating fast: tell browsers not to heuristic-cache the
	// shell bundle so a regular reload always picks up the latest
	// build. Add a long-lived cache path later when versioning lands.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.FileServer(s.assets).ServeHTTP(w, r)
}

func (s *HTTPServer) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r) {
		// 401 instead of an upgrade. The FE distinguishes this from a
		// transient drop via the /auth/check preflight.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Same-origin by default. AllowCrossOrigin opts out so a remote
		// router can accept a shell connection from another origin (the
		// remote-apps R2 case — the page is served by router A, this is
		// router B reached over an ssh -L tunnel). docs/REMOTE.md §10.
		InsecureSkipVerify: s.router.cfg.AllowCrossOrigin,
	})
	if err != nil {
		s.router.log("ws accept: %v", err)
		return
	}
	ws.SetReadLimit(int64(MaxWSReadLimit))
	ctx := r.Context()
	t := NewWSTransport(ctx, ws)
	if err := s.router.HandleShell(ctx, t); err != nil && !errors.Is(err, context.Canceled) {
		s.router.log("shell session from=%s: %v", r.RemoteAddr, err)
	}
}

// MaxWSReadLimit caps the size of any single WS binary message. One
// wash frame fits in one message, capped at MaxPayload + header = ~16
// MiB + 8 B. We add a small overhead margin.
const MaxWSReadLimit = (16 * 1024 * 1024) + 1024

// Run starts the HTTP listener on cfg.Listen and serves until ctx
// cancels. It returns the first error from ListenAndServe or shutdown.
func (s *HTTPServer) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.router.cfg.Listen,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	}
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
