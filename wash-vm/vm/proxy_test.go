package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/sirmick/wash/pkg/wire"
)

// TestProxyServesVMWire proves the decided shape (docs/NET.md §8.3): the VM
// serves everything and the proxy is a transparent serial tunnel. A browser WS
// to the proxy bridges to the in-guest wash-router over the virtio-serial data
// plane; on connect the router pushes its real catalog, the session-desktop
// declaration, and the FE bundle — byte-for-byte what a physical wash box serves
// on its LAN. We assert the catalog carries com.wash.net and the desktop app is
// declared. (The browser actually rendering this + round-tripping a model edit
// is the B1e-2 chrome/Playwright gate.)
func TestProxyServesVMWire(t *testing.T) {
	kernel, initramfs := artifacts(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	vm, err := Launch(ctx, Opts{Kernel: kernel, Initramfs: initramfs})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer vm.Close()
	if err := vm.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	p, err := vm.Proxy(ProxyOpts{})
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	defer p.Close()

	wsURL := "ws" + p.URL[len("http"):] + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20) // the session FE bundle rides in one large frame

	// The in-guest front gates the channel (wash-vm/UNIFY.md): on connect it
	// emits login.required, and only forks wash-router (which pushes the
	// catalog) after a login.ok. Send credentials up front — the front reads the
	// proxy's SessionOpen, emits login.required, then reads this and authenticates.
	loginJSON, _ := json.Marshal(map[string]string{"t": "login", "user": "wash", "pass": "wash"})
	var lf bytes.Buffer
	if err := wire.EncodeFrame(&lf, wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: loginJSON}); err != nil {
		t.Fatalf("encode login: %v", err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, lf.Bytes()); err != nil {
		t.Fatalf("ws write login: %v", err)
	}

	// Read the router's connect-time push (after login.ok forks it). We only
	// need the early control frames (catalog, session.snapshot, app.declared);
	// stop once both assertions are satisfied or the budget runs out. The budget
	// covers login + the router cold-start that follows it.
	var sawNetInCatalog, sawDesktop bool
	deadline := time.Now().Add(45 * time.Second)
	for n := 0; n < 24 && !(sawNetInCatalog && sawDesktop); n++ {
		rctx, rcancel := context.WithDeadline(ctx, deadline)
		typ, data, err := c.Read(rctx)
		rcancel()
		if err != nil {
			t.Fatalf("ws read %d: %v\nconsole:\n%s", n, err, vm.ConsoleLog())
		}
		if typ != websocket.MessageBinary {
			t.Fatalf("ws message type = %v", typ)
		}
		fr, err := wire.DecodeFrame(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("decode frame %d: %v", n, err)
		}
		if fr.Channel != 0 {
			continue // raw bundle / data channel — not what we assert on
		}
		var msg struct {
			T        string `json:"t"`
			Surface  string `json:"surface"`
			Element  string `json:"element"`
			Manifest struct {
				ID      string `json:"id"`
				Surface string `json:"surface"`
			} `json:"manifest"`
		}
		_ = json.Unmarshal(fr.Payload, &msg)
		switch msg.T {
		case "catalog":
			if strings.Contains(string(fr.Payload), "com.wash.net") {
				sawNetInCatalog = true
			}
		case "app.declared":
			if msg.Surface == "desktop" || msg.Manifest.Surface == "desktop" || msg.Element == "wash-app-session" {
				sawDesktop = true
			}
		}
	}
	if !sawNetInCatalog {
		t.Fatal("router catalog (served from the VM) did not include com.wash.net")
	}
	if !sawDesktop {
		t.Fatal("router did not declare the session desktop app over the tunnel")
	}
	t.Log("VM served its real catalog (incl. com.wash.net) + desktop over the serial tunnel")
}
