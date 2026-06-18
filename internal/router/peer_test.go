package router

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// bframe builds one host-B wire frame with an explicit class (channel 0,
// payload = p) — what flows inside the relay.
func bframe(c wire.Class, p string) wire.Frame {
	return wire.Frame{Flags: wire.FlagEnd | byte(c)<<1, Channel: 0, Payload: []byte(p)}
}

// TestPeerRelaySplice proves the A-side relay (docs/REMOTE.md §2/§7): a shell
// peer.attaches a registered origin, the router dials its socket, binds a
// peer channel, and forwards B's wire frame-by-frame — preserving each
// frame's CLASS (header-aware, payload-opaque) and the payload bytes
// verbatim. A stands in a fake "host B" that speaks framed wire.
func TestPeerRelaySplice(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{NoSession: true}, reg, func(f string, a ...any) { t.Logf("router: "+f, a...) })

	// Fake host B: send one Interactive + one Bulk frame, then echo frames.
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
				bt := wire.NewStreamTransport(c)
				_ = bt.WriteFrame(bframe(wire.ClassInteractive, "HELLO_FROM_B"))
				_ = bt.WriteFrame(bframe(wire.ClassBulk, "BULKDATA"))
				for {
					f, err := bt.ReadFrame()
					if err != nil {
						return
					}
					_ = bt.WriteFrame(f) // echo
				}
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

	if _, ok := readCtrl(t, end).(wire.ShellCatalog); !ok {
		t.Fatalf("expected ShellCatalog first")
	}
	writeCtrl(t, end, wire.NewShellPeerAttach("B"))

	// Wait for the peer channel.bind.
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
				ch = b.ChannelID
			}
		}
	}

	// Each relay frame on ch carries ONE B-frame, at B's own class. Read the
	// two marker frames and assert class + verbatim payload are preserved.
	want := []struct {
		class   wire.Class
		payload string
	}{
		{wire.ClassInteractive, "HELLO_FROM_B"},
		{wire.ClassBulk, "BULKDATA"},
	}
	for i, w := range want {
		rf := nextOnChannel(t, end, ch)
		if rf.Class() != w.class {
			t.Errorf("relay frame %d: class %v, want %v (class not preserved)", i, rf.Class(), w.class)
		}
		bf, derr := wire.DecodeFrame(bytes.NewReader(rf.Payload))
		if derr != nil {
			t.Fatalf("relay frame %d: payload is not a B-frame: %v", i, derr)
		}
		if string(bf.Payload) != w.payload {
			t.Errorf("relay frame %d: payload %q, want %q (not verbatim)", i, bf.Payload, w.payload)
		}
		if bf.Class() != w.class {
			t.Errorf("relay frame %d: inner B-frame class %v, want %v", i, bf.Class(), w.class)
		}
	}

	// Channel → socket: write a B-frame on the peer channel; B echoes it back.
	var pb bytes.Buffer
	if err := wire.EncodeFrame(&pb, bframe(wire.ClassInteractive, "PING123")); err != nil {
		t.Fatalf("encode ping: %v", err)
	}
	if err := end.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: ch, Payload: pb.Bytes()}); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	echo := nextOnChannel(t, end, ch)
	bf, derr := wire.DecodeFrame(bytes.NewReader(echo.Payload))
	if derr != nil || string(bf.Payload) != "PING123" {
		t.Fatalf("echo: got %q (err %v), want PING123", echo.Payload, derr)
	}

	cancel()
	<-done
}

// TestPeerRelayNoHeadOfLineBlocking is the regression for the creditless
// verbatim relay (docs/REMOTE.md §7). Host B sends more Bulk bytes than the
// old per-channel credit window (64 KiB) WITHOUT the FE ever granting credit,
// then an Interactive frame. Under the old design the pump blocked in a credit
// Reserve on the second bulk frame and never even read the interactive one —
// B's terminal froze behind a download. Creditless, the pump never blocks
// reading B's socket, so the interactive frame is forwarded (and, being higher
// priority, arrives promptly) even though no credit was issued.
func TestPeerRelayNoHeadOfLineBlocking(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{NoSession: true}, reg, func(f string, a ...any) { t.Logf("router: "+f, a...) })

	// Fake host B: two 50 KiB Bulk frames (100 KiB > the 64 KiB window the
	// relay channel used to carry), then a small Interactive marker.
	big := strings.Repeat("X", 50*1024)
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
				bt := wire.NewStreamTransport(c)
				_ = bt.WriteFrame(bframe(wire.ClassBulk, big))
				_ = bt.WriteFrame(bframe(wire.ClassBulk, big))
				_ = bt.WriteFrame(bframe(wire.ClassInteractive, "AFTER_BULK"))
				<-make(chan struct{}) // hold the conn open
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

	if _, ok := readCtrl(t, end).(wire.ShellCatalog); !ok {
		t.Fatalf("expected ShellCatalog first")
	}
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
				ch = b.ChannelID
			}
		}
	}

	// The FE issues NO channel.credit. Watch relay frames for the interactive
	// marker. If credit gated the relay, the pump would be parked on the second
	// bulk frame's Reserve and the marker would never be forwarded. Read in a
	// goroutine + select-on-timeout so that regression fails fast here rather
	// than hanging on a blocked ReadFrame until the go-test deadline.
	found := make(chan struct{})
	go func() {
		for {
			f, err := end.ReadFrame()
			if err != nil {
				return
			}
			if f.Channel != ch {
				continue
			}
			bf, derr := wire.DecodeFrame(bytes.NewReader(f.Payload))
			if derr == nil && bf.Class() == wire.ClassInteractive && string(bf.Payload) == "AFTER_BULK" {
				close(found)
				return
			}
		}
	}()
	select {
	case <-found:
	case <-time.After(5 * time.Second):
		t.Fatal("interactive marker never arrived — relay head-of-line-blocked on bulk")
	}

	cancel()
	<-done
}

// nextOnChannel reads frames until one arrives on channel ch.
func nextOnChannel(t *testing.T, e wire.FrameTransport, ch uint32) wire.Frame {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for a frame on channel %d", ch)
		}
		f, err := e.ReadFrame()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if f.Channel == ch {
			return f
		}
	}
}
