package router

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// tlsHandshakeByte is the TLS record-type byte that opens every
// ClientHello. A connection to the HTTPS listener whose first byte is
// anything else is a client speaking plaintext — almost always a
// browser that was pointed at http://<host>:<port> while the router is
// serving HTTPS on that port (issue #18).
const tlsHandshakeByte = 0x16

// sniffTimeout bounds how long a fresh connection may sit silent before
// its first byte arrives (and, on the plaintext path, how long the
// request line may take). Mirrors the server's ReadHeaderTimeout so a
// dribbling client can't pin the sniff goroutine.
const sniffTimeout = 5 * time.Second

// tlsRedirectListener wraps the TCP listener handed to ServeTLS. It
// sniffs the first byte of every accepted connection: TLS handshakes
// pass through to the HTTPS server untouched, while plaintext HTTP
// requests are answered with a redirect to the https:// URL of the
// same host and closed — instead of Go's opaque 400 "Client sent an
// HTTP request to an HTTPS server".
//
// Sniffing happens in a per-connection goroutine so a client that
// connects and sends nothing can never stall Accept (and with it every
// other client's handshake).
type tlsRedirectListener struct {
	ln    net.Listener
	log   func(string, ...any)
	conns chan net.Conn // sniffed TLS connections, handed to Accept
	errs  chan error    // terminal error from the accept loop
	done  chan struct{} // closed by Close
	once  sync.Once
}

func newTLSRedirectListener(ln net.Listener, log func(string, ...any)) *tlsRedirectListener {
	l := &tlsRedirectListener{
		ln:    ln,
		log:   log,
		conns: make(chan net.Conn),
		errs:  make(chan error, 1),
		done:  make(chan struct{}),
	}
	go l.acceptLoop()
	return l
}

func (l *tlsRedirectListener) acceptLoop() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			l.errs <- err // buffered; Accept (or nobody, post-Close) picks it up
			return
		}
		go l.sniff(c)
	}
}

// sniff reads the first byte and routes the connection: TLS handshake
// bytes flow to Accept, anything else is treated as a plaintext HTTP
// request and answered with a redirect.
func (l *tlsRedirectListener) sniff(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(sniffTimeout))
	var first [1]byte
	if _, err := io.ReadFull(c, first[:]); err != nil {
		_ = c.Close()
		return
	}
	pc := &peekedConn{Conn: c, r: io.MultiReader(bytes.NewReader(first[:]), c)}
	if first[0] == tlsHandshakeByte {
		_ = c.SetReadDeadline(time.Time{}) // the http.Server manages its own deadlines
		select {
		case l.conns <- pc:
		case <-l.done:
			_ = c.Close()
		}
		return
	}
	l.serveRedirect(pc)
}

// serveRedirect parses one plaintext HTTP request off the connection
// and answers 307 to the same URL under https://. 307 (not 301) so the
// method and body survive the hop and no browser permanently caches a
// redirect for a port that may later be reconfigured with --http. The
// query string rides along, so an http:// paste of the logged
// ?token=… URL still lands authenticated.
func (l *tlsRedirectListener) serveRedirect(c net.Conn) {
	defer c.Close()
	req, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil {
		return // not HTTP after all; nothing sensible to answer
	}
	host := req.Host
	if host == "" {
		host = l.ln.Addr().String()
	}
	target := "https://" + host + req.URL.RequestURI()
	l.log("router: redirecting plain-HTTP %s %s from %s to https", req.Method, req.URL.Path, c.RemoteAddr())
	resp := http.Response{
		StatusCode: http.StatusTemporaryRedirect,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Location": []string{target}},
		Close:      true,
	}
	_ = resp.Write(c)
}

func (l *tlsRedirectListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.errs:
		return nil, err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *tlsRedirectListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.ln.Close()
}

func (l *tlsRedirectListener) Addr() net.Addr { return l.ln.Addr() }

// peekedConn replays the sniffed first byte ahead of the rest of the
// stream. Everything but Read passes through to the real conn.
type peekedConn struct {
	net.Conn
	r io.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
