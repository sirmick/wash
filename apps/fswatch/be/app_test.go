package fswatchsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/fswatch"
	"github.com/sirmick/wash/internal/remotewatch"
	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// connectFswatch binds the service's onReady to a freshly-handshaken Conn and
// returns the router pipe end (for injecting controls and reading pushes) plus
// a cleanup. Mirrors apps/notify/be's connectNotify.
func connectFswatch(t *testing.T) (wire.FrameTransport, func()) {
	t.Helper()
	pp := wiretest.NewPipePair()

	type connResult struct {
		c   *sdk.Conn
		err error
	}
	ch := make(chan connResult, 1)
	go func() {
		c, err := sdk.ConnectWith(pp.EndA(), def)
		ch <- connResult{c, err}
	}()

	if _, err := pp.EndB().ReadFrame(); err != nil {
		t.Fatalf("read identity: %v", err)
	}
	ack, err := wire.EncodeCtrl(wire.NewIdentityAck("i-fswatch", 0))
	if err != nil {
		t.Fatalf("encode ack: %v", err)
	}
	if err := pp.EndB().WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: ack}); err != nil {
		t.Fatalf("write ack: %v", err)
	}

	res := <-ch
	if res.err != nil {
		t.Fatalf("connect: %v", res.err)
	}
	if hub == nil {
		t.Fatal("hub not initialised by onReady")
	}

	go func() { _ = res.c.Run(context.Background()) }()

	cleanup := func() {
		res.c.Close()
		if localMgr != nil {
			localMgr.Close()
		}
		hub, router, localMgr = nil, nil, nil
		mountsMu.Lock()
		mounts = map[string]*mountReg{}
		mountsMu.Unlock()
	}
	return pp.EndB(), cleanup
}

func writeControl(t *testing.T, router wire.FrameTransport, payload map[string]any, fromInst string) {
	t.Helper()
	msg := wire.NewEvtAppMsgFrom(0, payload, wire.Sender{AppID: "com.wash.fm", InstanceID: fromInst})
	b, err := wire.EncodeEvt(msg)
	if err != nil {
		t.Fatalf("encode evt: %v", err)
	}
	if err := router.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: b}); err != nil {
		t.Fatalf("write evt: %v", err)
	}
}

// readEventTo reads frames until an fs_event addressed to inst arrives.
func readEventTo(t *testing.T, router wire.FrameTransport, inst string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, err := router.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if f.Channel != 1 {
			continue
		}
		m, err := wire.DecodeEvt(f.Payload)
		if err != nil {
			continue
		}
		msg, ok := m.(wire.EvtAppMsg)
		if !ok || msg.To == nil || msg.To.InstanceID != inst {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			t.Fatalf("decode app msg: %v", err)
		}
		if data["kind"] == KindEvent {
			return data
		}
	}
	t.Fatalf("no fs_event for %s within %v", inst, timeout)
	return nil
}

// TestServiceStreamsLocalEvents drives the service end to end: a consumer
// subscribes to a local dir, a change there produces a real inotify event, and
// the service pushes an fs_event addressed to that subscriber.
func TestServiceStreamsLocalEvents(t *testing.T) {
	if mgr, err := fswatch.New(); err != nil {
		t.Skipf("no inotify in this env: %v", err)
	} else {
		mgr.Close()
	}

	dir := t.TempDir()
	router, cleanup := connectFswatch(t)
	defer cleanup()

	writeControl(t, router, map[string]any{"kind": KindWatch, "path": dir}, "i-sub")

	// Touch files until an event flows — absorbs the async subscribe dispatch
	// and inotify arming latency.
	stop := make(chan struct{})
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), []byte("x"), 0o644)
			}
		}
	}()
	defer close(stop)

	ev := readEventTo(t, router, "i-sub", 3*time.Second)
	if p, _ := ev["path"].(string); !strings.HasPrefix(p, dir) {
		t.Fatalf("event path %q not under %q", p, dir)
	}
	if op, _ := ev["op"].(string); op == "" {
		t.Fatalf("event missing op: %v", ev)
	}
}

// TestServiceRegisterMountRemoteWatch drives the remote path end to end: the
// supervisor registers a mount, a consumer watches a path under it, and a change
// on the (faked) remote host surfaces at the local mountpoint, path-translated —
// all through the same service the local consumers use.
func TestServiceRegisterMountRemoteWatch(t *testing.T) {
	if mgr, err := fswatch.New(); err != nil {
		t.Skipf("no inotify in this env: %v", err)
	} else {
		mgr.Close()
	}

	// Fake remote host: a Feed served over a pipe, standing in for wash-fswatchd.
	remoteFeed := fswatch.NewFeed()
	defer remoteFeed.Close()
	aEnd, bEnd := net.Pipe()
	defer aEnd.Close()
	defer bEnd.Close()
	go remotewatch.Serve(bEnd, remoteFeed)

	// Hand the service the pipe instead of spawning ssh wash-fswatchd.
	orig := openWatchChannel
	openWatchChannel = func(string) (io.ReadWriteCloser, error) { return aEnd, nil }
	defer func() { openWatchChannel = orig }()

	router, cleanup := connectFswatch(t)
	defer cleanup()

	// Supervisor registers: remote /srv mounted at /mnt/host.
	writeControl(t, router, map[string]any{
		"kind": KindRegisterMount, "host": "h", "remote_root": "/srv", "mount_point": "/mnt/host",
	}, "i-sup")
	// Consumer watches a path under the mount.
	writeControl(t, router, map[string]any{"kind": KindWatch, "path": "/mnt/host/dir"}, "i-sub")

	// Keep emitting the remote change until it propagates through register →
	// client → watch → wire → Serve → translate → Hub → push.
	stop := make(chan struct{})
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				remoteFeed.Emit(fswatch.Event{Op: fswatch.OpCreated, Path: "/srv/dir/x"})
			}
		}
	}()
	defer close(stop)

	ev := readEventTo(t, router, "i-sub", 3*time.Second)
	if p, _ := ev["path"].(string); p != "/mnt/host/dir/x" {
		t.Fatalf("remote event path = %q; want /mnt/host/dir/x (translation failed)", p)
	}
}
