package router

import (
	"fmt"
	"net"
	"strings"
)

// requireLoopbackTCP rejects an ingress tcp backend addr that the router
// would dial off the loopback interface. A published backend is otherwise
// proxied token-keyed but unauthenticated, so a non-loopback addr would
// let the router relay to an arbitrary host on the network. Apps are
// expected to serve on a unix socket or a loopback port; the in-repo
// publishers all use unix sockets, so this only ever fires on misuse.
//
// The check is literal — "localhost" or a loopback IP literal — and never
// performs a DNS lookup at publish time.
func requireLoopbackTCP(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("ingress: tcp addr %q must be host:port: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("ingress: tcp addr %q missing port", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("ingress: tcp addr host %q must be a loopback IP or \"localhost\"", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("ingress: tcp addr %q is not loopback — refusing to proxy", addr)
	}
	return nil
}
