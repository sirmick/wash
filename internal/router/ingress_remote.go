package router

// Remote ingress (issue #15, docs/REMOTE.md §17): serve /app/<token>/ for a
// token minted by a PEER's router, so a remote app's ingress iframe — which
// always loads against the LOCAL origin — resolves here.
//
// Topology: host B's relayed router serves its ingress registry as plain
// HTTP on a dedicated unix socket (--listen-ingress); the com.wash.remote
// supervisor forwards it over the same ssh as the relay socket and includes
// it in EvtPeerRegister. This is a SEPARATE conduit from the relay channel
// on purpose: the relay is a verbatim byte splice (browser ↔ B) that A never
// injects into — the FE reassembles B's wire by byte position, so A-origin
// frames on that channel would corrupt it (peer.go).
//
// Routing: handleIngress, on a local-registry miss, resolves the token
// against each registered peer (GET /ingress/resolve on its ingress socket
// — read-only, so safe to fan out) and reverse-proxies the request to the
// owning peer. The token→origin route is cached; the cache drops on peer
// unregister and on a proxied 410 (the peer expired the token). Tokens stay
// peer-minted: this router never re-mints or rewrites them, so every /app/
// app (vscode, music, radio, washamp) works remotely with zero app changes.

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"time"
)

// resolvePath is the peer-local resolution endpoint: GET
// resolvePath?token=<t> answers 204 when this router's ingress registry owns
// the token, 404 otherwise. Served ONLY on the --listen-ingress unix socket
// (never on a browser-facing front), so reachability is gated by socket
// permissions + the ssh tunnel, like the relay socket itself.
const resolvePath = "/ingress/resolve"

// resolveTimeout bounds one peer resolution probe. The hop is a local unix
// socket into an established ssh tunnel; a healthy peer answers in
// milliseconds, and a wedged tunnel shouldn't stall a browser request long.
const resolveTimeout = 3 * time.Second

// cacheRemote records that token belongs to origin's router.
func (ir *ingressRegistry) cacheRemote(token, origin string) {
	ir.mu.Lock()
	ir.remote[token] = origin
	ir.mu.Unlock()
}

// lookupRemote returns the cached owning origin for token, if any.
func (ir *ingressRegistry) lookupRemote(token string) (string, bool) {
	ir.mu.RLock()
	origin, ok := ir.remote[token]
	ir.mu.RUnlock()
	return origin, ok
}

// dropRemoteToken forgets one cached token→origin route (the peer answered
// 410 — it expired the token). Idempotent.
func (ir *ingressRegistry) dropRemoteToken(token string) {
	ir.mu.Lock()
	delete(ir.remote, token)
	ir.mu.Unlock()
}

// dropRemoteOrigin forgets every token routed to origin (peer unregistered).
func (ir *ingressRegistry) dropRemoteOrigin(origin string) {
	ir.mu.Lock()
	for tok, o := range ir.remote {
		if o == origin {
			delete(ir.remote, tok)
		}
	}
	ir.mu.Unlock()
}

// serveRemoteIngress tries to serve an /app/<token>/ request whose token no
// local backend owns by routing it to the peer that minted it. Returns false
// when no peer owns the token (the caller emits the 410).
func (r *Router) serveRemoteIngress(w http.ResponseWriter, req *http.Request, token string) bool {
	origin, ok := r.ingress.lookupRemote(token)
	if !ok {
		origin, ok = r.resolveRemoteIngress(req.Context(), token)
		if !ok {
			return false
		}
		r.ingress.cacheRemote(token, origin)
	}
	r.peersMu.Lock()
	t, live := r.peers[origin]
	r.peersMu.Unlock()
	if !live || t.ingressProxy == nil {
		// Cached route to a peer that has since unregistered (the purge in
		// unregisterPeer races a request already past lookupRemote, or the
		// peer re-registered without ingress). Drop and report unknown.
		r.ingress.dropRemoteToken(token)
		return false
	}
	t.ingressProxy.ServeHTTP(w, req)
	return true
}

// resolveRemoteIngress asks each ingress-capable peer whether it owns token.
// Sequential fan-out: peers are few (one per connected host) and a probe is
// one round-trip on a local unix socket. First 204 wins.
func (r *Router) resolveRemoteIngress(ctx context.Context, token string) (string, bool) {
	type probe struct {
		origin string
		addr   string
	}
	r.peersMu.Lock()
	probes := make([]probe, 0, len(r.peers))
	for origin, t := range r.peers {
		if t.ingressAddr != "" {
			probes = append(probes, probe{origin: origin, addr: t.ingressAddr})
		}
	}
	r.peersMu.Unlock()
	for _, p := range probes {
		if r.peerOwnsToken(ctx, p.addr, token) {
			return p.origin, true
		}
	}
	return "", false
}

// peerOwnsToken performs one resolution probe against a peer ingress socket.
func (r *Router) peerOwnsToken(ctx context.Context, addr, token string) bool {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://wash-peer"+resolvePath+"?token="+token, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", addr)
		},
	}}
	resp, err := client.Do(req)
	if err != nil {
		r.log("ingress: resolve probe %s: %v", addr, err)
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

// newPeerIngressProxy builds the ReverseProxy for one peer's ingress socket.
// Unlike newIngressProxy it forwards the request path UNCHANGED — the peer's
// own handleIngress strips /app/<token>/ and applies its registry — and it
// watches for a 410 coming back, which means the peer expired the token: the
// cached route is dropped so the next request re-resolves instead of pinning
// a dead token to this peer forever.
func newPeerIngressProxy(origin, addr string, ir *ingressRegistry) *httputil.ReverseProxy {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", addr)
	}
	return &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "wash-peer"
			pr.Out.Host = "wash-peer"
			pr.SetXForwarded()
		},
		Transport: &http.Transport{DialContext: dial},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusGone && resp.Request != nil {
				if tok := tokenFromPath(resp.Request.URL.Path); tok != "" {
					ir.dropRemoteToken(tok)
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			ir.log("ingress: peer %s (%s) unreachable: %v", origin, addr, err)
			http.Error(w, "remote ingress unavailable", http.StatusBadGateway)
		},
	}
}

// NewIngressServer returns the handler a relayed router serves on its
// --listen-ingress unix socket: the shared /app/ ingress body plus the
// peer-resolution endpoint. No auth gate — like --listen-raw, the socket's
// 0600 mode and the ssh tunnel are the access boundary (docs/REMOTE.md §10).
func NewIngressServer(r *Router) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/", r.handleIngress)
	mux.HandleFunc(resolvePath, r.handleIngressResolve)
	return mux
}

// handleIngressResolve answers whether this router's LOCAL ingress registry
// owns a token: 204 yes, 404 no. Deliberately not recursive — a resolve
// never consults this router's own peers (wash never chains, peer.go).
func (r *Router) handleIngressResolve(w http.ResponseWriter, req *http.Request) {
	token := req.URL.Query().Get("token")
	if token != "" && r.ingress.lookup(token) != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, req)
}
