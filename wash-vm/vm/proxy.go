package vm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/sirmick/wash/internal/wire"
)

// Proxy is the inside-out counterpart of wash-vm's in-browser VirtioConsoleSocket
// (docs/NET.md §8.3). It serves the minimal host chrome at / (Static), tunnels a
// browser WebSocket ⟷ the guest serial DATA plane at /ws (one wash wire frame
// per message — wire-compatible with the shell's transport), and streams the
// guest console (LOG plane) at /console. The wash UI itself is served BY the VM:
// the chrome fetches shell.js over the wire (asset.read) and the in-guest router
// pushes the catalog + app bundles. The browser can't tell it isn't a real box.
type Proxy struct {
	vm  *VM
	ln  net.Listener
	srv *http.Server
	URL string

	// Takeover bridge management. One browser is bridged onto the serial at a
	// time; a newer browser EVICTS the current one (docs/NET.md — "latest
	// viewer drives the box"). acquireMu serialises takeovers; current points
	// at the active bridge (nil when none).
	acquireMu sync.Mutex
	bridgeMu  sync.Mutex
	current   *bridge

	// sinkMu guards sink, the encoded-frame handler for the currently-attached
	// browser (nil when none). A single persistent read-pump (readPump) owns
	// the serial frame reader for the proxy's whole life and hands each frame
	// to sink. It must never be torn down per-browser: DecodeFrame reads
	// straight off the conn (no buffer), so unblocking a per-bridge reader
	// would risk desyncing the shared stream. Because the guest now pushes
	// nothing until a SessionOpen (a browser is present), the stream is idle —
	// not backing up — between viewers.
	sinkMu sync.Mutex
	sink   func([]byte)
}

// bridge is one browser's slot on the serial. stop is closed to evict it
// (a newer browser took over); done is closed once it has fully torn down
// (SessionClose written, sink cleared) so the next browser's SessionOpen is
// strictly ordered after this one's SessionClose.
type bridge struct {
	stop chan struct{}
	done chan struct{}
}

// ProxyOpts configures the proxy. Static, if set, is a directory served at /.
type ProxyOpts struct {
	Static string
	Addr   string // default 127.0.0.1:0
}

// Proxy starts the HTTP/WS proxy in front of the VM. Call after WaitReady so the
// guest data plane is open.
func (vm *VM) Proxy(opts ProxyOpts) (*Proxy, error) {
	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	p := &Proxy{vm: vm, ln: ln, URL: "http://" + ln.Addr().String()}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", p.handleWS)
	mux.HandleFunc("/console", p.handleConsole)
	if opts.Static != "" {
		mux.Handle("/", http.FileServer(http.Dir(opts.Static)))
	}
	p.srv = &http.Server{Handler: mux}
	go p.srv.Serve(ln)
	go p.readPump()
	return p, nil
}

// readPump owns the serial frame reader for the proxy's lifetime: it reads one
// wash frame at a time and forwards it (re-encoded) to the currently-attached
// browser, or drops it when none is attached. Because it is never torn down
// per-browser it can't be interrupted mid-frame (no stream desync) and it keeps
// draining the guest so its non-blocking serial write never wedges. It exits
// when the data conn closes (Proxy.Close / VM shutdown).
func (p *Proxy) readPump() {
	for {
		fr, err := p.vm.dataT.ReadFrame()
		if err != nil {
			return // serial closed — proxy shutting down
		}
		var b bytes.Buffer
		if err := wire.EncodeFrame(&b, fr); err != nil {
			continue // malformed frame — skip, stay in sync
		}
		// Hold sinkMu across the (non-blocking) sink call so we order against a
		// detaching bridge that nils the sink and closes its channel under the
		// same lock — otherwise we could send on a closed channel.
		p.sinkMu.Lock()
		if p.sink != nil {
			p.sink(b.Bytes())
		}
		p.sinkMu.Unlock()
	}
}

// acquireBridge makes this browser the active viewer, evicting whoever held
// the serial before it. It blocks until the previous browser has fully torn
// down (its SessionClose is on the wire), so the guest sees a clean
// close→open ordering. Takeovers are serialised by acquireMu.
func (p *Proxy) acquireBridge() *bridge {
	p.acquireMu.Lock()
	defer p.acquireMu.Unlock()

	p.bridgeMu.Lock()
	old := p.current
	p.bridgeMu.Unlock()
	if old != nil {
		close(old.stop) // evict the current viewer
		<-old.done      // wait for its SessionClose + cleanup
	}

	me := &bridge{stop: make(chan struct{}), done: make(chan struct{})}
	p.bridgeMu.Lock()
	p.current = me
	p.bridgeMu.Unlock()
	return me
}

func (p *Proxy) releaseBridge(me *bridge) {
	p.bridgeMu.Lock()
	if p.current == me {
		p.current = nil
	}
	p.bridgeMu.Unlock()
	close(me.done)
}

// writeCtrl sends a JSON control message to the guest on channel 0.
func (p *Proxy) writeCtrl(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return p.vm.dataT.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: payload})
}

func (p *Proxy) handleWS(w http.ResponseWriter, r *http.Request) {
	// Localhost trust model (ARCHITECTURE.md): the proxy and browser are
	// same-origin on the loopback; skip the origin check.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()

	// Become the active viewer (takeover). The browser's WS open/close is the
	// connection lifecycle the serial lacks; we translate it into SessionOpen/
	// SessionClose frames so the guest runs a fresh HandleShell per browser
	// (docs/NET.md §8.3). releaseBridge runs LAST (deferred first); SessionClose
	// runs before it, so the next browser's SessionOpen is ordered after.
	me := p.acquireBridge()
	defer p.releaseBridge(me)

	ctx := r.Context()
	// Cancel the WS read when this browser is evicted by a newer one.
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	go func() {
		select {
		case <-me.stop:
			rcancel()
		case <-rctx.Done():
		}
	}()

	if err := p.writeCtrl(wire.NewSessionOpen()); err != nil {
		return
	}
	defer p.writeCtrl(wire.NewSessionClose())

	// Register this browser as the read-pump's sink. The sink hands frames to a
	// per-bridge writer goroutine via a buffered channel and NEVER blocks: the
	// read-pump owns the serial framing, so blocking here would stall it on one
	// slow browser. A full buffer drops frames for this browser, not the stream.
	// The writer goroutine is the sole caller of c.Write.
	out := make(chan []byte, 256)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for b := range out {
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageBinary, b)
			cancel()
			if err != nil {
				return
			}
		}
	}()
	sink := func(b []byte) {
		select {
		case out <- b:
		default: // browser too slow — drop this frame, keep the framing moving
		}
	}
	p.sinkMu.Lock()
	p.sink = sink
	p.sinkMu.Unlock()
	defer func() {
		p.sinkMu.Lock()
		p.sink = nil
		p.sinkMu.Unlock()
		close(out)
		<-writerDone
	}()

	// WS → guest data plane. One WS message = one wash frame; WriteFrame always
	// emits a whole frame, so this direction can't desync the serial. Exits on
	// browser disconnect (c.Read error) or eviction (rctx cancelled).
	for {
		_, data, err := c.Read(rctx)
		if err != nil {
			return
		}
		fr, err := wire.DecodeFrame(bytes.NewReader(data))
		if err != nil {
			return
		}
		if err := p.vm.dataT.WriteFrame(fr); err != nil {
			return
		}
	}
}

// handleConsole streams the guest console (ttyS0 — the LOG plane) to the chrome's
// Console tab over Server-Sent Events. Each chunk is base64-framed (the console
// carries raw bytes / ANSI that would otherwise collide with SSE's line framing)
// and the FE decodes + appends. It tails the file: read to EOF, then poll for
// growth, so late-attaching browsers still get the boot log from the top.
func (p *Proxy) handleConsole(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(p.vm.logPath)
	if err != nil {
		http.Error(w, "console unavailable", http.StatusServiceUnavailable)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ctx := r.Context()
	buf := make([]byte, 8192)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := f.Read(buf)
		if n > 0 {
			if _, werr := fmt.Fprintf(w, "data: %s\n\n", base64.StdEncoding.EncodeToString(buf[:n])); werr != nil {
				return
			}
			flusher.Flush()
		}
		if err == io.EOF {
			select {
			case <-ctx.Done():
				return
			case <-time.After(150 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			return
		}
	}
}

// Close shuts the proxy down (not the VM).
func (p *Proxy) Close() error {
	if p.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return p.srv.Shutdown(ctx)
	}
	return nil
}
