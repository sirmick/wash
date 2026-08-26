package router

import (
	"context"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// writeEvtFrameClass is writeEvtFrame with an explicit QoS class — what
// the SDK's SendAppMsgToBulk puts on the wire.
func writeEvtFrameClass(t *testing.T, e wire.FrameTransport, m any, class wire.Class) {
	t.Helper()
	b, err := wire.EncodeEvt(m)
	if err != nil {
		t.Fatalf("encode evt: %v", err)
	}
	f := wire.Frame{Flags: wire.FlagEnd, Channel: ChannelEvent, Payload: b}.WithClass(class)
	if err := e.WriteFrame(f); err != nil {
		t.Fatalf("write evt: %v", err)
	}
}

// TestCrossInstanceAppMsgKeepsSenderClass — a service that marks its
// stream Bulk (agentd's transcript fan-out) must still be Bulk when it
// reaches the recipient. The relay used to re-stamp every cross-app
// message Interactive, which put a streaming reply in front of the
// window moves the human was making while it streamed.
func TestCrossInstanceAppMsgKeepsSenderClass(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterEntry(&Entry{Path: "/unused/wash-notify", Manifest: notifyManifest()})
	reg.RegisterEntry(&Entry{Path: "/unused/wash-about", Manifest: aboutManifest()})
	r := NewRouter(Config{}, reg, nil)

	// Recipient first, so resolveRecipient takes the already-running
	// singleton path rather than trying to spawn a binary.
	recvPair := wiretest.NewPipePair()
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		_ = r.HandleApp(context.Background(), recvPair.EndA(), notifyManifest(), nil)
	}()
	recvInst := connectApp(t, recvPair, NotifyAppID)

	sendPair := wiretest.NewPipePair()
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		_ = r.HandleApp(context.Background(), sendPair.EndA(), aboutManifest(), nil)
	}()
	_ = connectApp(t, sendPair, "com.wash.about")
	// Drain the EvtWindowMapped the router ships post-bringUp.
	_, _ = sendPair.EndB().ReadFrame()

	for _, tc := range []struct {
		name string
		sent wire.Class
	}{
		{"bulk", wire.ClassBulk},
		{"interactive", wire.ClassInteractive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeEvtFrameClass(t, sendPair.EndB(), wire.NewEvtAppMsgTo(
				wire.Recipient{InstanceID: recvInst},
				map[string]any{"kind": "transcript_event"},
			), tc.sent)

			f := readAppMsgFrame(t, recvPair, 2*time.Second)
			if f.Class() != tc.sent {
				t.Fatalf("relayed class=%v, want %v", f.Class(), tc.sent)
			}
		})
	}

	sendPair.Close()
	recvPair.Close()
	waitClose(t, sendDone)
	waitClose(t, recvDone)
}

// readAppMsgFrame waits for the next inbound app_msg FRAME (not just its
// decoded form — the class bits live in the header, which is the whole
// point here) on the given pipe.
func readAppMsgFrame(t *testing.T, pp *wiretest.PipePair, timeout time.Duration) wire.Frame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, err := pp.EndB().ReadFrame()
		if err != nil {
			t.Fatalf("read app_msg: %v", err)
		}
		if f.Channel != ChannelEvent {
			continue
		}
		m, err := wire.DecodeEvt(f.Payload)
		if err != nil {
			t.Fatalf("decode evt: %v", err)
		}
		if _, ok := m.(wire.EvtAppMsg); !ok {
			continue
		}
		return f
	}
	t.Fatalf("no app_msg frame within %v", timeout)
	return wire.Frame{}
}
