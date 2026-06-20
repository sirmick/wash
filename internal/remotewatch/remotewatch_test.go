package remotewatch

import (
	"net"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/fswatch"
)

func TestPathMapRoundTrip(t *testing.T) {
	pm := PathMap{MountPoint: "/mnt/host", RemoteRoot: "/home/user"}
	cases := []struct{ local, remote string }{
		{"/mnt/host", "/home/user"},
		{"/mnt/host/proj", "/home/user/proj"},
		{"/mnt/host/a/b/c.txt", "/home/user/a/b/c.txt"},
	}
	for _, c := range cases {
		if got := pm.ToRemote(c.local); got != c.remote {
			t.Errorf("ToRemote(%q) = %q; want %q", c.local, got, c.remote)
		}
		if got := pm.ToLocal(c.remote); got != c.local {
			t.Errorf("ToLocal(%q) = %q; want %q", c.remote, got, c.local)
		}
	}
}

// TestWatchStreamEndToEnd drives the full B→A path: B's fswatch (here a Feed
// standing in for inotify, for determinism) reports a change on a remote path;
// it must arrive at the local consumer translated into mountpoint space.
func TestWatchStreamEndToEnd(t *testing.T) {
	aConn, bConn := net.Pipe()
	defer aConn.Close()
	defer bConn.Close()

	bFeed := fswatch.NewFeed() // stands in for B's local inotify Manager
	defer bFeed.Close()
	go Serve(bConn, bFeed)

	pm := PathMap{MountPoint: "/mnt/host", RemoteRoot: "/home/user"}
	client := NewClient(aConn, pm)
	defer client.Close()

	sub, err := client.Watch("/mnt/host/proj")
	if err != nil {
		t.Fatalf("client.Watch: %v", err)
	}

	// A change to /home/user/proj/x on B must surface as /mnt/host/proj/x on A.
	got := emitUntilReceived(t, bFeed, sub,
		fswatch.Event{Op: fswatch.OpCreated, Path: "/home/user/proj/x"})
	if got.Op != fswatch.OpCreated || got.Path != "/mnt/host/proj/x" {
		t.Fatalf("got %+v; want created /mnt/host/proj/x", got)
	}
}

// TestClientIsSource confirms the A-side client is a drop-in fswatch.Source.
func TestClientIsSource(t *testing.T) {
	aConn, bConn := net.Pipe()
	defer aConn.Close()
	defer bConn.Close()
	go Serve(bConn, fswatch.NewFeed())

	var src fswatch.Source = NewClient(aConn, PathMap{MountPoint: "/mnt", RemoteRoot: "/"})
	if src == nil {
		t.Fatal("client is not a Source")
	}
	_ = src.Close()
}

// emitUntilReceived repeatedly emits ev on bFeed until it is received on sub or
// the deadline passes. Retrying covers the small async gap between the client's
// watch request and B registering the subscription; emits before registration
// are simply dropped (no subscriber), so duplicates are harmless.
func emitUntilReceived(t *testing.T, bFeed *fswatch.Feed, sub fswatch.Subscription, ev fswatch.Event) fswatch.Event {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		bFeed.Emit(ev)
		select {
		case got, ok := <-sub.Events():
			if !ok {
				t.Fatal("sub events closed unexpectedly")
			}
			return got
		case <-tick.C:
		case <-deadline:
			t.Fatal("timeout waiting for translated event")
			return fswatch.Event{}
		}
	}
}
