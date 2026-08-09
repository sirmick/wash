package router

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
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

	r.registerPeer("B", "unix", sock, "")

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

	r.registerPeer("B", "unix", sock, "")

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

// TestPeerRelayMaxSizeFrameSurvives is the regression for REVIEW-DATAPATH
// F10. Host B sends a maximum-size frame (16 MiB payload → 16 MiB + 8 bytes
// on the wire). Wrapped whole as one relay-frame payload it would exceed
// MaxPayload, fail EncodeFrame, and tear the relay channel down; a marker
// frame sent right after would then never arrive. With the A-side split the
// oversized frame is chunked across relay frames, B's bytes are delivered
// intact, and the relay stays up so the marker still arrives.
func TestPeerRelayMaxSizeFrameSurvives(t *testing.T) {
	reg := NewRegistry()
	r := NewRouter(Config{NoSession: true}, reg, func(f string, a ...any) { t.Logf("router: "+f, a...) })

	// A maximum legal B payload (exactly MaxPayload bytes), filled with a
	// position-dependent pattern so a reassembly bug can't pass by accident.
	maxPayload := make([]byte, wire.MaxPayload)
	for i := range maxPayload {
		maxPayload[i] = byte(i * 7)
	}
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
				_ = bt.WriteFrame(wire.Frame{Flags: wire.FlagEnd | byte(wire.ClassBulk)<<1, Channel: 0, Payload: maxPayload})
				_ = bt.WriteFrame(bframe(wire.ClassInteractive, "AFTER_MAX"))
				<-make(chan struct{}) // hold the conn open
			}()
		}
	}()

	r.registerPeer("B", "unix", sock, "")

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

	// Reassemble B's byte stream from the relay-channel frames (as the FE's
	// relay-socket does) and decode B-frames out of it. The split max frame
	// spans several relay frames; the marker proves the relay wasn't torn down.
	type decoded struct {
		class   wire.Class
		payload []byte
	}
	got := make(chan decoded, 4)
	go func() {
		var buf []byte
		for {
			f, err := end.ReadFrame()
			if err != nil {
				return
			}
			if f.Channel != ch {
				continue
			}
			buf = append(buf, f.Payload...)
			for {
				bf, derr := wire.DecodeFrame(bytes.NewReader(buf))
				if derr != nil {
					break // incomplete B-frame — wait for more relay bytes
				}
				n := 8 + len(bf.Payload)
				buf = buf[n:]
				got <- decoded{class: bf.Class(), payload: bf.Payload}
			}
		}
	}()

	// First B-frame: the max-size payload, intact.
	select {
	case d := <-got:
		if len(d.payload) != wire.MaxPayload {
			t.Fatalf("max frame: len %d, want %d", len(d.payload), wire.MaxPayload)
		}
		if !bytes.Equal(d.payload, maxPayload) {
			t.Fatal("max frame payload not delivered intact (split pieces reordered)")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("max-size frame never arrived — relay torn down by oversize wrap")
	}
	// Second B-frame: the marker, proving the relay survived.
	select {
	case d := <-got:
		if d.class != wire.ClassInteractive || string(d.payload) != "AFTER_MAX" {
			t.Fatalf("marker: class %v payload %q, want interactive AFTER_MAX", d.class, d.payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("marker never arrived — relay torn down after the max frame")
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
