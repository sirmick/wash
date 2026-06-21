package remotewatch

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/fswatch"
)

// waitForPath emits ev on feed repeatedly until the translated wantLocal event
// arrives on sub (ignoring any stale buffered events), or the deadline passes.
func waitForPath(t *testing.T, feed *fswatch.Feed, sub fswatch.Subscription, ev fswatch.Event, wantLocal string) {
	t.Helper()
	deadline := time.After(8 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		feed.Emit(ev)
		select {
		case got, ok := <-sub.Events():
			if !ok {
				t.Fatal("sub closed unexpectedly")
			}
			if got.Path == wantLocal {
				return
			}
		case <-tick.C:
		case <-deadline:
			t.Fatalf("timeout waiting for %s", wantLocal)
		}
	}
}

// TestClientReconnectsAndResubscribes is the watch-path chaos case: drop the
// client's channel and a later change still arrives — proving the client
// re-dialed AND re-sent its active watch so B re-subscribed.
func TestClientReconnectsAndResubscribes(t *testing.T) {
	remoteFeed := fswatch.NewFeed() // B-side source, shared across reconnects
	defer remoteFeed.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go Serve(conn, remoteFeed) // a fresh Server per connection, same Feed
		}
	}()

	var mu sync.Mutex
	var curConn net.Conn
	dials := 0
	dial := func() (io.ReadWriteCloser, error) {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			return nil, err
		}
		mu.Lock()
		curConn = c
		dials++
		mu.Unlock()
		return c, nil
	}

	pm := PathMap{MountPoint: "/mnt/host", RemoteRoot: "/srv"}
	client := NewReconnectingClient(dial, pm)
	defer client.Close()

	sub, err := client.Watch("/mnt/host/dir")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}

	// Works before the drop.
	waitForPath(t, remoteFeed, sub, fswatch.Event{Op: fswatch.OpCreated, Path: "/srv/dir/x"}, "/mnt/host/dir/x")

	// Kill the client's connection.
	mu.Lock()
	cc := curConn
	mu.Unlock()
	cc.Close()

	// After reconnect + re-subscribe, a new change still arrives.
	waitForPath(t, remoteFeed, sub, fswatch.Event{Op: fswatch.OpModified, Path: "/srv/dir/y"}, "/mnt/host/dir/y")

	mu.Lock()
	d := dials
	mu.Unlock()
	if d < 2 {
		t.Fatalf("expected a re-dial after the drop, got %d dials", d)
	}
}

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

// TestClientNoSpuriousReconnect guards the inotify-leak failure mode: a stable
// connection must NOT re-dial (each re-dial spawns a fresh wash-fswatchd on B,
// leaking inotify instances). Watch a path, sit idle, assert dials stays at 1.
func TestClientNoSpuriousReconnect(t *testing.T) {
	remoteFeed := fswatch.NewFeed()
	defer remoteFeed.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go Serve(conn, remoteFeed)
		}
	}()
	var mu sync.Mutex
	dials := 0
	dial := func() (io.ReadWriteCloser, error) {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			return nil, err
		}
		mu.Lock()
		dials++
		mu.Unlock()
		return c, nil
	}
	client := NewReconnectingClient(dial, PathMap{MountPoint: "/mnt", RemoteRoot: "/srv"})
	defer client.Close()
	sub, _ := client.Watch("/mnt/dir")
	defer sub.Close()
	// Confirm it's connected + delivering, then sit idle.
	waitForPath(t, remoteFeed, sub, fswatch.Event{Op: fswatch.OpCreated, Path: "/srv/dir/x"}, "/mnt/dir/x")
	time.Sleep(2 * time.Second)
	mu.Lock()
	d := dials
	mu.Unlock()
	if d != 1 {
		t.Fatalf("stable connection re-dialed %d times (want 1) — spurious reconnect leaks wash-fswatchd", d)
	}
}
