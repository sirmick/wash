package router

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// serveUnix starts h on a fresh unix socket and returns its path.
func serveUnix(t *testing.T, h http.Handler) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "be.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func newTestFront(t *testing.T) (*Router, *httptest.Server) {
	t.Helper()
	r := NewRouter(Config{}, nil, func(string, ...any) {})
	front := httptest.NewServer(NewHTTPServer(r, nil))
	t.Cleanup(front.Close)
	return r, front
}

func TestIngressProxy_HTTPStripAndHeader(t *testing.T) {
	var gotPath, gotQuery, gotIngress string
	sock := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotIngress = r.Header.Get("X-Wash-Ingress-Path")
		io.WriteString(w, "hello from backend")
	}))

	r, front := newTestFront(t)
	path, token, err := r.ingress.publish("i-1", "unix", sock)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	resp, err := http.Get(front.URL + path + "foo/bar?x=1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotPath != "/foo/bar" {
		t.Errorf("backend path = %q, want /foo/bar (prefix not stripped)", gotPath)
	}
	if gotQuery != "x=1" {
		t.Errorf("backend query = %q, want x=1", gotQuery)
	}
	if want := "/app/" + token; gotIngress != want {
		t.Errorf("X-Wash-Ingress-Path = %q, want %q", gotIngress, want)
	}
	if string(body) != "hello from backend" {
		t.Errorf("body = %q", body)
	}
}

func TestIngressProxy_BareTokenRedirects(t *testing.T) {
	sock := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r, front := newTestFront(t)
	_, token, _ := r.ingress.publish("i-1", "unix", sock)

	// Don't follow redirects.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(front.URL + "/app/" + token)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/app/"+token+"/" {
		t.Errorf("Location = %q, want /app/%s/", loc, token)
	}
}

func TestIngressProxy_UnknownTokenGone(t *testing.T) {
	_, front := newTestFront(t)
	resp, err := http.Get(front.URL + "/app/deadbeefdeadbeef/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410", resp.StatusCode)
	}
}

func TestIngressProxy_DropInstanceRemovesRoute(t *testing.T) {
	sock := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r, front := newTestFront(t)
	path, _, _ := r.ingress.publish("i-1", "unix", sock)

	r.ingress.dropInstance("i-1")

	resp, err := http.Get(front.URL + path + "x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("after dropInstance status = %d, want 410", resp.StatusCode)
	}
}

// TestIngressProxy_WebSocket is the load-bearing case: the workbench
// protocol rides a WebSocket, so the proxy must pass an Upgrade
// through to the unix-socket backend and splice both directions.
func TestIngressProxy_WebSocket(t *testing.T) {
	sock := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, append([]byte("echo:"), data...))
	}))

	r, front := newTestFront(t)
	path, _, _ := r.ingress.publish("i-1", "unix", sock)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := strings.Replace(front.URL, "http://", "ws://", 1) + path + "ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial through ingress: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	if err := c.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if string(data) != "echo:ping" {
		t.Errorf("ws echo = %q, want echo:ping", data)
	}
}
