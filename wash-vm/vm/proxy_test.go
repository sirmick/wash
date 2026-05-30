package vm

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/sirmick/wash/internal/wire"
)

func TestProxyServesAndBridges(t *testing.T) {
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

	// A static asset dir to serve host-side (vite-like).
	static := t.TempDir()
	if err := os.WriteFile(filepath.Join(static, "index.html"), []byte("<title>wash-net</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := vm.Proxy(ProxyOpts{Static: static})
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	defer p.Close()

	// 1) FE assets served host-side.
	resp, err := http.Get(p.URL + "/index.html")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body, []byte("wash-net")) {
		t.Fatalf("static asset not served: %q", body)
	}

	// 2) Browser WS ⟷ guest data plane: one wash frame per message, round-tripped
	//    through the guest over serial and back.
	wsURL := "ws" + p.URL[len("http"):] + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer c.CloseNow()

	want := wire.Frame{Flags: wire.FlagEnd, Channel: 42, Payload: []byte("hello from the browser")}
	var enc bytes.Buffer
	if err := wire.EncodeFrame(&enc, want); err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, enc.Bytes()); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v\nconsole:\n%s", err, vm.ConsoleLog())
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("ws message type = %v", typ)
	}
	got, err := wire.DecodeFrame(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode echoed frame: %v", err)
	}
	if got.Channel != want.Channel || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("echo mismatch: got ch=%d %q, want ch=%d %q", got.Channel, got.Payload, want.Channel, want.Payload)
	}
	t.Logf("round-tripped a wash frame through the guest: ch=%d %q", got.Channel, got.Payload)
}
