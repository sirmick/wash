package router

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// TestPeerRelaySplice proves the A-side relay (docs/REMOTE.md): a shell
// peer.attaches a registered origin, the router dials its socket, binds a
// peer channel, and splices it verbatim — socket bytes arrive as raw frames
// on the channel, and raw frames on the channel reach the socket. A stands
// in a fake "host B" socket (writes a marker, then echoes), so the test
// exercises the splice without a second router.
func TestPeerRelaySplice(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{NoSession: true}, reg, func(f string, a ...any) { t.Logf("router: "+f, a...) })

	// Fake host B: on connect, send a marker then echo everything.
	sock := filepath.Join(t.TempDir(), "b.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = c.Write([]byte("HELLO_FROM_B"))
				_, _ = io.Copy(c, c) // echo
			}()
		}
	}()

	r.registerPeer("B", "unix", sock)

	shellPair := wiretest.NewPipePair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = r.HandleShell(ctx, shellPair.EndA()) }()
	end := shellPair.EndB()

	// Drain the catalog the router sends on connect.
	if _, ok := readCtrl(t, end).(wire.ShellCatalog); !ok {
		t.Fatalf("expected ShellCatalog first")
	}

	// Attach origin B; expect a peer channel.bind back.
	writeCtrl(t, end, wire.NewShellPeerAttach("B"))

	var ch uint32
	deadline := time.Now().Add(5 * time.Second)
	for ch == 0 {
		if time.Now().After(deadline) {
			t.Fatal("never received peer channel.bind")
		}
		f, err := end.ReadFrame()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if f.Channel != ChannelControl {
			continue
		}
		if m, _ := wire.DecodeCtrl(f.Payload); m != nil {
			if b, ok := m.(wire.ShellChannelBind); ok && b.Kind == wire.ChannelKindPeer {
				if b.Origin != "B" {
					t.Fatalf("bind origin = %q, want B", b.Origin)
				}
				ch = b.ChannelID
			}
		}
	}

	// Socket → channel: B's marker arrives as raw frames on the peer channel.
	got := readRawUntil(t, end, ch, "HELLO_FROM_B")
	if !strings.Contains(got, "HELLO_FROM_B") {
		t.Fatalf("relayed marker missing; got %q", got)
	}

	// Channel → socket: write on the peer channel; the fake B echoes it back.
	if err := end.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ch, Payload: []byte("PING123")}); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	echo := readRawUntil(t, end, ch, "PING123")
	if !strings.Contains(echo, "PING123") {
		t.Fatalf("echo missing; got %q", echo)
	}

	cancel()
	<-done
}

// readRawUntil reads frames, accumulating payloads on channel ch, until the
// accumulation contains want (or it times out).
func readRawUntil(t *testing.T, e wire.FrameTransport, ch uint32, want string) string {
	t.Helper()
	var acc strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(acc.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q on channel %d; got %q", want, ch, acc.String())
		}
		f, err := e.ReadFrame()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if f.Channel == ch {
			acc.Write(f.Payload)
		}
	}
	return acc.String()
}
