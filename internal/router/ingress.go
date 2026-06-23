package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"

	"github.com/sirmick/wash/internal/wire"
)

// Ingress is wash's generic embed-a-web-app facility, modeled on Home
// Assistant add-on ingress. An app's BE serves an HTTP/WS backend on a
// unix socket (or loopback port) and calls PublishIngress; the router
// mints an opaque capability token, reverse-proxies /app/<token>/* to
// that backend (WebSocket upgrades included), and hands the app a
// public path. The app's FE loads that path in an <iframe>, so the
// browser never has to reach the backend directly — which is what lets
// it work for a remote browser, not just one on the host.
//
// The token IS the capability: it is unguessable, minted router-side,
// delivered to the FE over the trusted app_msg channel, and embedded
// in the iframe src. There is no separate session check today — the
// shell /ws transport is itself still localhost-trust (see
// http.go handleWS) — so the token is the whole access-control story
// for v1. When real session auth lands, an ownership check layers on
// top of the token without changing this contract.
//
// code-server is consumer #1; jupyter/grafana/etc. publish the same
// way. The router knows nothing app-specific.

// ingressBackend is one published HTTP/WS backend behind a token.
type ingressBackend struct {
	instanceID string
	network    string // "unix" | "tcp"
	addr       string // socket path (unix) or host:port (tcp, dialed on loopback)
	path       string // public base path, "/app/<token>/"
	proxy      *httputil.ReverseProxy
}

// ingressRegistry maps capability tokens to backends. Safe for
// concurrent use: the proxy route reads it on every request while
// app BEs publish/unpublish and tearDown GCs by instance.
type ingressRegistry struct {
	log     Logger
	mu      sync.RWMutex
	byToken map[string]*ingressBackend
}

func newIngressRegistry(log Logger) *ingressRegistry {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &ingressRegistry{log: log, byToken: make(map[string]*ingressBackend)}
}

// MintToken exposes mintToken to callers outside the package (the
// router runner mints the listener's auth token with it).
func MintToken() (string, error) { return mintToken() }

// mintToken returns 16 random bytes hex-encoded (128 bits) — wide
// enough that the token is the access boundary on its own.
func mintToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// publish registers a backend for instanceID and returns its public
// path and raw token. network must be "unix" or "tcp"; addr is the
// socket path or host:port the router will dial.
func (ir *ingressRegistry) publish(instanceID, network, addr string) (path, token string, err error) {
	switch network {
	case "unix":
	case "tcp":
		// A tcp backend is proxied token-keyed but otherwise
		// unauthenticated; refuse to relay to anything off loopback.
		if err := requireLoopbackTCP(addr); err != nil {
			return "", "", err
		}
	default:
		return "", "", fmt.Errorf("ingress: unsupported network %q (want unix|tcp)", network)
	}
	if addr == "" {
		return "", "", fmt.Errorf("ingress: empty addr")
	}
	token, err = mintToken()
	if err != nil {
		return "", "", fmt.Errorf("ingress: mint token: %w", err)
	}
	path = "/app/" + token + "/"
	be := &ingressBackend{
		instanceID: instanceID,
		network:    network,
		addr:       addr,
		path:       path,
		proxy:      newIngressProxy(network, addr, strings.TrimSuffix(path, "/"), ir.log),
	}
	ir.mu.Lock()
	ir.byToken[token] = be
	ir.mu.Unlock()
	return path, token, nil
}

// unpublish drops the route named by path ("/app/<token>/"). Idempotent.
func (ir *ingressRegistry) unpublish(path string) {
	token := tokenFromPath(path)
	if token == "" {
		return
	}
	ir.mu.Lock()
	delete(ir.byToken, token)
	ir.mu.Unlock()
}

// dropInstance removes every backend published by instanceID. Called
// from tearDown so a crashed/closed app never leaves a live route.
func (ir *ingressRegistry) dropInstance(instanceID string) {
	ir.mu.Lock()
	for tok, be := range ir.byToken {
		if be.instanceID == instanceID {
			delete(ir.byToken, tok)
		}
	}
	ir.mu.Unlock()
}

func (ir *ingressRegistry) lookup(token string) *ingressBackend {
	ir.mu.RLock()
	be := ir.byToken[token]
	ir.mu.RUnlock()
	return be
}

// tokenFromPath pulls the token out of "/app/<token>" or
// "/app/<token>/...". Returns "" if the shape doesn't match.
func tokenFromPath(p string) string {
	rest := strings.TrimPrefix(p, "/app/")
	if rest == p {
		return ""
	}
	token, _, _ := strings.Cut(rest, "/")
	return token
}

// newIngressProxy builds a ReverseProxy that dials the given backend.
// basePath ("/app/<token>") is echoed in X-Wash-Ingress-Path so a
// backend that needs to know where it's mounted can self-configure.
// The inbound path is stripped to backend-relative by the handler
// before this runs. FlushInterval -1 streams responses immediately
// (SSE / chunked), and the standard *http.Transport gives us free
// WebSocket-upgrade passthrough.
func newIngressProxy(network, addr, basePath string, log Logger) *httputil.ReverseProxy {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			// Placeholder authority — DialContext ignores it and
			// dials the registered backend. Kept stable so the
			// backend sees a consistent Host.
			pr.Out.URL.Host = "wash-ingress"
			pr.Out.Host = "wash-ingress"
			pr.Out.Header.Set("X-Wash-Ingress-Path", basePath)
			pr.SetXForwarded()
		},
		Transport: &http.Transport{DialContext: dial},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log("ingress: backend %s://%s unreachable: %v", network, addr, err)
			http.Error(w, "ingress backend unavailable", http.StatusBadGateway)
		},
	}
}

// handleIngress serves /app/<token>/<rest...>: resolve the token,
// strip the prefix, and reverse-proxy to the backend. A bare
// /app/<token> (no trailing slash) redirects so the embedded app's
// relative asset URLs resolve under the base path.
//
// It lives on the Router (not HTTPServer) because two front-ends mount
// it: the direct-TCP HTTPServer mux (single-user / dev — http.go) and
// the multi-user SCM_RIGHTS handoff served over the ctl socket
// (unix_listener.go serveHandoffHTTP). Both share this one body so the
// proxy semantics can't drift between deployment modes.
func (r *Router) handleIngress(w http.ResponseWriter, req *http.Request) {
	rest := strings.TrimPrefix(req.URL.Path, "/app/")
	if rest == req.URL.Path || rest == "" {
		http.NotFound(w, req)
		return
	}
	token, sub, found := strings.Cut(rest, "/")
	if token == "" {
		http.NotFound(w, req)
		return
	}
	if !found {
		http.Redirect(w, req, "/app/"+token+"/", http.StatusMovedPermanently)
		return
	}
	be := r.ingress.lookup(token)
	if be == nil {
		http.Error(w, "unknown or expired ingress", http.StatusGone)
		return
	}
	// Rewrite to backend-relative path. Clearing RawPath lets
	// net/http recompute the encoded form from Path.
	req.URL.Path = "/" + sub
	req.URL.RawPath = ""
	be.proxy.ServeHTTP(w, req)
}

// handleIngressPublish is the router side of the publish RPC.
func (inst *AppInstance) handleIngressPublish(m wire.EvtIngressPublish) error {
	path, token, err := inst.router.ingress.publish(inst.InstanceID, m.Network, m.Addr)
	if err != nil {
		return inst.WriteEvt(wire.NewEvtIngressErr(m.ReqID, wire.ErrCodeBadRequest, err.Error()))
	}
	inst.router.log("ingress publish instance=%s %s://%s -> %s", inst.InstanceID, m.Network, m.Addr, path)
	return inst.WriteEvt(wire.NewEvtIngressPublished(m.ReqID, path, token))
}
