package router

import (
	"context"
	cryptorand "crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/sirmick/wash/pkg/wire"
)

// TestWSWriteTimeout — a client that accepts the connection but never
// reads must not block a frame write forever (docs/PTY_ROBUST.md, Fix C).
// With a per-write deadline, WriteFrame returns an error instead of
// pinning the shell's single drainLoop and hanging every terminal on
// that connection; the shell then tears down and the FE reconnects.
func TestWSWriteTimeout(t *testing.T) {
	// Server: accept, then never read — the client's writes back up.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusInternalError, "")
		<-r.Context().Done() // hold the conn open, read nothing
	}))
	defer srv.Close()

	ctx := context.Background()
	cc, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close(websocket.StatusNormalClosure, "")

	tr := &WSTransport{ctx: ctx, ws: cc, writeTimeout: 100 * time.Millisecond}

	// Incompressible payload so the (uncompressed) buffers actually fill.
	payload := make([]byte, 128*1024)
	_, _ = cryptorand.Read(payload)

	// The server never reads, so after the socket buffers fill a write
	// stalls and must hit the deadline. Bound the whole loop so a missing
	// deadline fails the test instead of hanging it.
	overall := time.After(15 * time.Second)
	for {
		select {
		case <-overall:
			t.Fatal("no write hit the deadline within 15s — per-write timeout not enforced")
		default:
		}
		if err := tr.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 5, Payload: payload}); err != nil {
			return // a write hit the deadline (or the conn errored) — as intended
		}
	}
}
