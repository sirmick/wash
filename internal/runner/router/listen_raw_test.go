package routerrun

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/router"
	"github.com/sirmick/wash/internal/wire"
)

// TestRunRawListenerServesWire proves --listen-raw (host B's relay endpoint,
// docs/REMOTE.md): a raw connection to the listening socket gets the shell
// wire directly — HandleShell sends its catalog frame on connect, with no
// HTTP/WebSocket framing in between. This is the byte stream A's relay
// channel splices to.
func TestRunRawListenerServesWire(t *testing.T) {
	reg := router.NewRegistry()
	r := router.NewRouter(router.Config{NoSession: true}, reg, func(f string, a ...any) {
		t.Logf("router: "+f, a...)
	})

	sock := filepath.Join(t.TempDir(), "raw.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lnErr := make(chan error, 1)
	go func() {
		lnErr <- runRawListener(ctx, r, "unix", sock, func(f string, a ...any) { t.Logf("ln: "+f, a...) })
	}()

	// Dial once the listener is up (it removes+binds the socket async).
	var conn net.Conn
	for i := 0; i < 200; i++ {
		c, err := net.Dial("unix", sock)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("listener never accepted a connection")
	}
	defer conn.Close()

	// First frame on connect is ShellCatalog (empty here) — proof the raw
	// wire is served, no WS handshake required.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	f, err := wire.NewStreamTransport(conn).ReadFrame()
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	msg, err := wire.DecodeCtrl(f.Payload)
	if err != nil {
		t.Fatalf("decode ctrl: %v", err)
	}
	if _, ok := msg.(wire.ShellCatalog); !ok {
		t.Fatalf("expected ShellCatalog first, got %T", msg)
	}

	cancel()
	select {
	case err := <-lnErr:
		if err != nil {
			t.Errorf("listener returned error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("listener did not stop after ctx cancel")
	}
}
