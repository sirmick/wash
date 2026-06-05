package vm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// console scrollback handed to a freshly-connected browser so it sees recent
// output (the shell prompt, the last command) instead of a blank screen.
const consoleScrollback = 64 * 1024

// Console exposes a booted VM's serial line to browsers: an xterm.js page at /
// and a raw-bytes WebSocket at /ws. It takes over the serial conn (the read side
// stops being marker-based Run()), so attach it AFTER any Run()-driven setup is
// done. Unlike the agent-VM Proxy this is a plain serial line, not the wash wire
// protocol: every browser shares the one console (all see the same output, any
// can type) — that's what a real serial port is.
type Console struct {
	URL string

	w   *OpenWRT
	srv *http.Server

	mu      sync.Mutex
	clients map[chan []byte]struct{}
	backlog []byte
}

// ServeConsole starts the console HTTP/WS server on addr (use host:0 for an
// ephemeral port; the chosen URL is on the returned Console). It binds the
// listener, takes over the serial read loop, and serves until ctx is cancelled.
func (w *OpenWRT) ServeConsole(ctx context.Context, addr, title string) (*Console, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &Console{w: w, clients: map[chan []byte]struct{}{}}
	c.URL = "http://" + ln.Addr().String() + "/"

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", c.handleWS)
	mux.HandleFunc("/", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(rw, strings.ReplaceAll(consolePage, "__TITLE__", htmlEscape(title)))
	})
	c.srv = &http.Server{Handler: mux}

	go c.srv.Serve(ln)
	go func() { <-ctx.Done(); c.srv.Close() }()
	go c.readPump(ctx)

	// Nudge the shell so a fresh viewer immediately sees a prompt (the boot/setup
	// output was already consumed by Run() before the pump took over).
	_, _ = c.w.conn.Write([]byte("\n"))
	return c, nil
}

// readPump owns the serial conn after takeover: it reads forever and fans each
// chunk out to every connected browser, keeping a bounded scrollback.
func (c *Console) readPump(ctx context.Context) {
	_ = c.w.conn.SetReadDeadline(time.Time{}) // blocking reads; Run() is done
	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := c.w.conn.Read(buf)
		if n > 0 {
			c.broadcast(append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			return
		}
	}
}

func (c *Console) broadcast(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.backlog = append(c.backlog, b...)
	if len(c.backlog) > consoleScrollback {
		c.backlog = c.backlog[len(c.backlog)-consoleScrollback:]
	}
	for ch := range c.clients {
		select {
		case ch <- b:
		default: // a slow browser drops bytes, never stalls the serial reader
		}
	}
}

func (c *Console) handleWS(rw http.ResponseWriter, r *http.Request) {
	// Localhost/LAN demo trust model (same as the agent-VM proxy): skip origin.
	conn, err := websocket.Accept(rw, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()

	ch := make(chan []byte, 256)
	c.mu.Lock()
	backlog := append([]byte(nil), c.backlog...)
	c.clients[ch] = struct{}{}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.clients, ch)
		close(ch)
		c.mu.Unlock()
	}()

	// Writer goroutine: the sole caller of conn.Write — scrollback first, then the
	// live fan-out, preserving order (both captured under the same lock above).
	go func() {
		if len(backlog) > 0 {
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = conn.Write(wctx, websocket.MessageBinary, backlog)
			cancel()
		}
		for b := range ch {
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageBinary, b)
			cancel()
			if err != nil {
				return
			}
		}
	}()

	// Browser keystrokes → the serial line.
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if _, err := c.w.conn.Write(data); err != nil {
			return
		}
	}
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// consolePage is a minimal xterm.js terminal wired to /ws (binary frames both
// ways). xterm.js is pulled from a CDN to keep the demo dependency-free.
const consolePage = `<!doctype html>
<html><head><meta charset="utf-8"><title>__TITLE__</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.min.css">
<script src="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.min.js"></script>
<style>
  html,body{margin:0;height:100%;background:#000;font-family:ui-monospace,monospace}
  #bar{background:#101418;color:#5fd75f;padding:6px 12px;font-size:13px;border-bottom:1px solid #1f2630}
  #term{height:calc(100vh - 31px);padding:4px}
</style></head>
<body>
  <div id="bar">__TITLE__</div>
  <div id="term"></div>
  <script>
    const term = new Terminal({fontSize:13, cursorBlink:true, theme:{background:'#000000'}});
    term.open(document.getElementById('term'));
    const ws = new WebSocket((location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/ws');
    ws.binaryType = 'arraybuffer';
    ws.onmessage = e => term.write(new Uint8Array(e.data));
    ws.onclose = () => term.write('\r\n\x1b[31m[console disconnected]\x1b[0m\r\n');
    term.onData(d => { if (ws.readyState === 1) ws.send(new TextEncoder().encode(d)); });
    window.addEventListener('load', () => term.focus());
  </script>
</body></html>`
