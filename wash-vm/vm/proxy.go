package vm

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/sirmick/wash/internal/wire"
)

// Proxy is the inside-out counterpart of wash-vm's in-browser VirtioConsoleSocket
// (docs/NET.md §8.3): it serves the FE assets host-side and bridges a browser
// WebSocket to the guest's serial data plane, one wash wire frame per WS message.
// The guest VM, not the browser, hosts the OS — but the wire is identical.
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

// Close shuts the proxy down (not the VM).
func (p *Proxy) Close() error {
	if p.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return p.srv.Shutdown(ctx)
	}
	return nil
}
