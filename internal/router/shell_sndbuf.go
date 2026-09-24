package router

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"strconv"
	"syscall"
	"time"
)

// Bounding what the kernel queues ahead of a control frame.
//
// The QoS scheduler (qos.go) drains Control before Bulk — but only among
// frames still in ITS queues. Once a frame is handed to the socket it sits
// in the kernel's send buffer, which Linux autotunes up to net.ipv4.tcp_wmem
// max (4 MB by default), strictly FIFO. On a link slower than the router
// can fill it — a VPN, a far-away host — that buffer is where the bytes
// actually wait, and a window-move patch submitted after a burst of bulk
// (an agent's transcript, a pty flood) waits behind all of it: seconds, not
// milliseconds. The scheduler's priority never bites because the writer is
// never blocked on the socket.
//
// So the shell socket gets a fixed, small send buffer. The writer then
// blocks early, the scheduler's ordering applies, and the worst case a
// control frame waits for the wire is one buffer's worth. The cost is a
// throughput ceiling of buffer/RTT — 256 KB at 80 ms is ~25 Mbit/s, above
// what such a link carries anyway; on a LAN it is far above what the
// desktop needs. WASH_SHELL_SNDBUF overrides (bytes; 0 leaves the kernel
// default). Applied to the browser-facing shell socket only, not to asset
// or ingress connections.

// ShellSendBufBytes is the send buffer applied to shell WebSocket sockets.
var ShellSendBufBytes = envInt("WASH_SHELL_SNDBUF", 256<<10)

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

type connCtxKey struct{}

// connContext is an http.Server.ConnContext hook that keeps the accepted
// net.Conn reachable from the request, so the WS upgrade path can reach
// the socket before the websocket library hijacks it.
func connContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connCtxKey{}, c)
}

// boundShellSendBuf applies ShellSendBufBytes to the request's underlying
// TCP socket. Best effort: a conn we cannot unwrap (a test's pipe) is left
// alone, with one log line so a misconfigured deployment is visible.
func boundShellSendBuf(r *http.Request, log func(string, ...any)) {
	if ShellSendBufBytes <= 0 {
		return
	}
	c, _ := r.Context().Value(connCtxKey{}).(net.Conn)
	sc := rawSocket(c)
	if sc == nil {
		if c != nil && log != nil {
			log("shell: sndbuf: cannot reach the socket under %T; kernel default stays", c)
		}
		return
	}
	if err := setSendBuf(sc, ShellSendBufBytes); err != nil && log != nil {
		log("shell: sndbuf=%d: %v", ShellSendBufBytes, err)
	}
}

// rawSocket unwraps the layers a shell conn arrives in (TLS, the handoff
// replay wrapper) down to something that exposes its fd.
func rawSocket(c net.Conn) syscall.Conn {
	for c != nil {
		switch v := c.(type) {
		case *tls.Conn:
			c = v.NetConn()
		case *replayConn:
			c = v.Conn
		case syscall.Conn:
			return v
		default:
			return nil
		}
	}
	return nil
}

// slowCtrlWrite is how long a control frame's socket write may take
// before drainLoop logs it; slowCtrlLogEvery rate-limits that line.
const (
	slowCtrlWrite    = 250 * time.Millisecond
	slowCtrlLogEvery = 5 * time.Second
)
