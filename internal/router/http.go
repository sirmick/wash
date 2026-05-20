package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/coder/websocket"
)

// fallbackIndexHTML is the placeholder shell served when no embedded
// assets are present. Real shell-runtime assets are wired up in
// commit C5; until then this loads no JS and explains the state.
const fallbackIndexHTML = `<!doctype html>
<html lang="en"><meta charset="utf-8"><title>wash</title>
<body style="background:#111;color:#eee;font:14px/1.4 system-ui,sans-serif;padding:2em">
<h1>wash — Web Application SHell</h1>
<p>The router is running, but no shell-runtime bundle is embedded in
this build. Continue through commit C5 to embed the real Solid+Vite
runtime under <code>cmd/wash-router/assets/</code>.</p>
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
func NewHTTPServer(r *Router, assets http.FileSystem) *HTTPServer {
	s := &HTTPServer{router: r, assets: assets, mux: http.NewServeMux()}
	s.mux.HandleFunc("/ws", s.handleWS)
	s.mux.HandleFunc("/screenshot", s.handleScreenshot)
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

func (s *HTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *HTTPServer) handleRoot(w http.ResponseWriter, r *http.Request) {
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
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// localhost-only build means the same-origin guard is fine
		// for v0.0; tighten in v0.1.
		InsecureSkipVerify: false,
	})
	if err != nil {
		s.router.log("ws accept: %v", err)
		return
	}
	ws.SetReadLimit(int64(MaxWSReadLimit))
	ctx := r.Context()
	t := NewWSTransport(ctx, ws)
	if err := s.router.HandleShell(ctx, t); err != nil && !errors.Is(err, context.Canceled) {
		s.router.log("shell session: %v", err)
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
