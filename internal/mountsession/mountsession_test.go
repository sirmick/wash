package mountsession

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/fswatch"
	"github.com/sirmick/wash/internal/remotewatch"
	"github.com/sirmick/wash/internal/sftptest"
)

// TestSessionDataAndWatch exercises both channels of a wash-to-wash mount in one
// shot: data over SFTP (a real FUSE mount) and watch over the wash stream backed
// by real inotify on the "remote" side. The watch case deliberately makes a
// change directly in the backing store (a third-party / remote-origin write that
// local inotify on the mount could never see) and asserts it surfaces at the
// mountpoint — the whole reason the watch channel exists.
func TestSessionDataAndWatch(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skipf("no /dev/fuse: %v", err)
	}

	backing := t.TempDir() // the "remote" filesystem
	mnt := t.TempDir()     // where it appears locally

	// B side: real inotify over the backing dir, served across a pipe.
	mgr, err := fswatch.New()
	if err != nil {
		t.Skipf("no inotify watcher: %v", err)
	}
	defer mgr.Close()
	aConn, bConn := net.Pipe()
	defer aConn.Close()
	defer bConn.Close()
	go remotewatch.Serve(bConn, fswatch.LocalSource(mgr))

	// A side: SFTP data channel + a Router the watch registers into.
	client, cleanup, err := sftptest.New()
	if err != nil {
		t.Fatalf("sftp test server: %v", err)
	}
	defer cleanup()
	router := fswatch.NewRouter(nil)

	sess, err := Open(Options{
		MountPoint: mnt,
		RemoteRoot: backing,
		SFTP:       client,
		Watch:      aConn,
		Router:     router,
	})
	if err != nil {
		t.Skipf("session open (FUSE unavailable?): %v", err)
	}
	defer sess.Close()

	// --- data channel: write via the mount, verify it lands in the backing store
	if err := os.WriteFile(filepath.Join(mnt, "d.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write via mount: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(backing, "d.txt")); err != nil || string(got) != "hi" {
		t.Fatalf("backing readback = %q, %v; want hi", got, err)
	}

	// --- watch channel: a direct change in the backing store must surface at
	// the mountpoint through the remote inotify -> stream -> router -> feed path.
	if err := os.Mkdir(filepath.Join(backing, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir backing/sub: %v", err)
	}
	sub, err := router.Watch(filepath.Join(mnt, "sub"))
	if err != nil {
		t.Fatalf("router.Watch: %v", err)
	}
	defer sub.Close()

	ev := waitForEvent(t, sub, func() {
		// Write directly into the backing dir — a remote-origin change.
		_ = os.WriteFile(filepath.Join(backing, "sub", "new.txt"), []byte("x"), 0o644)
	})
	want := filepath.Join(mnt, "sub", "new.txt")
	if ev.Path != want {
		t.Fatalf("watch event path = %q; want %q", ev.Path, want)
	}
}

// waitForEvent repeatedly invokes trigger until an event arrives on sub or the
// deadline passes. Retrying absorbs both the async watch-registration handshake
// and inotify arming latency; re-triggering the same path just yields further
// modify events, and we take the first that arrives.
func waitForEvent(t *testing.T, sub fswatch.Subscription, trigger func()) fswatch.Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		trigger()
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatal("sub events closed unexpectedly")
			}
			return ev
		case <-tick.C:
		case <-deadline:
			t.Fatal("timeout waiting for watch event to propagate")
			return fswatch.Event{}
		}
	}
}
