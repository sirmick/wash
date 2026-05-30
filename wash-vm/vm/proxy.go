package vm

import (
	"bytes"
	"context"
	"encoding/base64"
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

	bridge sync.Mutex // the single serial data plane serves one WS bridge at a time
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
	return p, nil
}

func (p *Proxy) handleWS(w http.ResponseWriter, r *http.Request) {
	// Localhost trust model (ARCHITECTURE.md): the proxy and browser are
	// same-origin on the loopback; skip the origin check.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()

	p.bridge.Lock()
	defer p.bridge.Unlock()
	ctx := r.Context()
	errc := make(chan error, 2)

	// guest data plane → WS: one wash frame per WS message.
	go func() {
		for {
			fr, err := p.vm.dataT.ReadFrame()
			if err != nil {
				errc <- err
				return
			}
			var b bytes.Buffer
			if err := wire.EncodeFrame(&b, fr); err != nil {
				errc <- err
				return
			}
			if err := c.Write(ctx, websocket.MessageBinary, b.Bytes()); err != nil {
				errc <- err
				return
			}
		}
	}()

	// WS → guest data plane.
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			fr, err := wire.DecodeFrame(bytes.NewReader(data))
			if err != nil {
				errc <- err
				return
			}
			if err := p.vm.dataT.WriteFrame(fr); err != nil {
				errc <- err
				return
			}
		}
	}()

	<-errc
	// Unblock the serial reader (blocked in ReadFrame on the shared data conn)
	// so it exits before the next bridge starts; then restore the deadline.
	_ = p.vm.data.SetReadDeadline(time.Now())
	<-errc
	_ = p.vm.data.SetReadDeadline(time.Time{})
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
