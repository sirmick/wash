package router

// Remote ingress (issue #15): /app/<token>/ requests whose token was minted
// by a PEER router must route to it. These tests stand up two real Routers —
// A (the desktop front the browser talks to) and B (the relayed peer) — with
// B's ingress registry served on a unix socket exactly as --listen-ingress
// does, and drive A's HTTP front end to end.

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// newRemotePair wires the A↔B topology: B publishes a backend, B's ingress
// handler is served on a unix socket (the --listen-ingress stand-in), and A
// registers B as an ingress-capable peer (what EvtPeerRegister/--peer-ingress
// do). Returns A's front, B, and B's published path+token.
func newRemotePair(t *testing.T, backend http.Handler) (a *Router, front *httptest.Server, b *Router, path, token string) {
	t.Helper()
	beSock := serveUnix(t, backend)
	b = NewRouter(Config{}, nil, func(string, ...any) {})
	path, token, err := b.ingress.publish("i-b", "unix", beSock)
	if err != nil {
		t.Fatalf("publish on B: %v", err)
	}
	bIngressSock := serveUnix(t, NewIngressServer(b))
	a, front = newTestFront(t)
	a.AddPeer("remoteB", "", "", bIngressSock)
	return a, front, b, path, token
}

func TestRemoteIngress_RoutesToPeer(t *testing.T) {
	var gotPath string
	_, front, _, path, _ := newRemotePair(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, "hello from B")
	}))

	resp, err := http.Get(front.URL + path + "foo?x=1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, body)
	}
	if string(body) != "hello from B" {
		t.Errorf("body = %q", body)
	}
	// B's own handleIngress must have done the prefix strip — A forwards
	// the path unchanged and never interprets the token.
	if gotPath != "/foo" {
		t.Errorf("backend path = %q, want /foo", gotPath)
	}
}

func TestRemoteIngress_CachesTokenRoute(t *testing.T) {
	a, front, _, path, token := newRemotePair(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	if _, ok := a.ingress.lookupRemote(token); ok {
		t.Fatalf("token cached before any request")
	}
	if resp, err := http.Get(front.URL + path); err == nil {
		resp.Body.Close()
	}
	origin, ok := a.ingress.lookupRemote(token)
	if !ok || origin != "remoteB" {
		t.Errorf("after request lookupRemote = (%q,%v), want (remoteB,true)", origin, ok)
	}
}

func TestRemoteIngress_UnknownTokenStillGone(t *testing.T) {
	_, front, _, _, _ := newRemotePair(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	resp, err := http.Get(front.URL + "/app/deadbeefdeadbeef/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410", resp.StatusCode)
	}
}

// TestRemoteIngress_PeerExpiryDropsCache: when B expires a token (app
// closed), the proxied 410 must both surface to the client and evict A's
// cached route, so the token isn't pinned to B forever.
func TestRemoteIngress_PeerExpiryDropsCache(t *testing.T) {
	a, front, b, path, token := newRemotePair(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	// Prime the cache.
	if resp, err := http.Get(front.URL + path); err == nil {
		resp.Body.Close()
	}
	b.ingress.dropInstance("i-b")

	resp, err := http.Get(front.URL + path)
	if err != nil {
		t.Fatalf("GET after expiry: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410 proxied from B", resp.StatusCode)
	}
	if _, ok := a.ingress.lookupRemote(token); ok {
		t.Errorf("cached route survived B's 410")
	}
}

// TestRemoteIngress_UnregisterPurgesRoutes: a departing peer takes its
// cached token routes with it (and requests fall back to 410 locally,
// without probing a dead socket forever).
func TestRemoteIngress_UnregisterPurgesRoutes(t *testing.T) {
	a, front, _, path, token := newRemotePair(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	if resp, err := http.Get(front.URL + path); err == nil {
		resp.Body.Close()
	}
	a.unregisterPeer("remoteB")
	if _, ok := a.ingress.lookupRemote(token); ok {
		t.Fatalf("cache survived peer unregister")
	}
	resp, err := http.Get(front.URL + path)
	if err != nil {
		t.Fatalf("GET after unregister: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410", resp.StatusCode)
	}
}

// TestRemoteIngress_WebSocket is the vscode-workbench case end to end:
// browser → A's front → B's ingress socket → B's proxy → unix backend, with
// a WebSocket upgrade spliced through BOTH reverse-proxy hops.
func TestRemoteIngress_WebSocket(t *testing.T) {
	_, front, _, path, _ := newRemotePair(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, append([]byte("remote-echo:"), data...))
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := strings.Replace(front.URL, "http://", "ws://", 1) + path + "ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial through remote ingress: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	if err := c.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if string(data) != "remote-echo:ping" {
		t.Errorf("ws echo = %q, want remote-echo:ping", data)
	}
}

// TestIngressResolveEndpoint pins the peer-resolution contract B serves on
// its --listen-ingress socket: 204 for an owned token, 404 otherwise.
func TestIngressResolveEndpoint(t *testing.T) {
	beSock := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	b := NewRouter(Config{}, nil, func(string, ...any) {})
	_, token, err := b.ingress.publish("i-b", "unix", beSock)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	ingressSock := serveUnix(t, NewIngressServer(b))

	client := unixHTTPClient(ingressSock)
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"?token=" + token, http.StatusNoContent},
		{"?token=deadbeefdeadbeef", http.StatusNotFound},
		{"", http.StatusNotFound},
	} {
		resp, err := client.Get("http://wash-peer" + resolvePath + tc.query)
		if err != nil {
			t.Fatalf("GET resolve%s: %v", tc.query, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("resolve%s = %d, want %d", tc.query, resp.StatusCode, tc.want)
		}
	}
}

// unixHTTPClient dials the given unix socket for every request.
func unixHTTPClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
}
