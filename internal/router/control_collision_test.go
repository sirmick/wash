package router

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The control socket is per-UID by default, and since LIFETIME a user
// routinely has several routers alive at once. Binding it used to be
// unlink-then-listen, which made the newest router the owner of the
// well-known name — and every app the OTHER routers spawned then dialled
// it, failed the registered-binary check, and timed out. This is the
// behaviour that must not come back.

func TestControlSocketDoesNotStealALiveOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wash-1000.sock")

	// Somebody is already listening — stand in for the other router.
	incumbent, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer incumbent.Close()
	go func() {
		for {
			c, err := incumbent.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	r := NewRouter(Config{ControlSocket: path}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.ListenControl(ctx) }()

	// It should have stepped aside onto a path of its own. Observed on
	// the filesystem rather than by reading r.cfg: the router does not
	// write the bound path back into its config (spawns read that field
	// from other goroutines), and what matters here is which FILE exists.
	alt := ResolveControlSocket(path, nil)
	if alt == path {
		t.Fatal("resolver did not step aside from a live socket")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(alt); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(alt); err != nil {
		t.Fatalf("router did not bind its own path %s: %v", alt, err)
	}
	// ...and the incumbent must still be reachable on the original name,
	// which is the whole point: its already-spawned apps dial that path.
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("incumbent's socket no longer answers: %v", err)
	}
	_ = c.Close()

	cancel()
	<-done
	// And tearing down must not delete the incumbent's socket either.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("shutdown removed the other router's socket: %v", err)
	}
}

func TestControlSocketRebindsAStaleOne(t *testing.T) {
	// A socket file with nobody behind it is the ordinary case after an
	// unclean exit, and must NOT push the router onto an alternate path.
	dir := t.TempDir()
	path := filepath.Join(dir, "wash-1000.sock")
	// SetUnlinkOnClose(false): Go removes a unix socket's file on Close by
	// default, and the state under test is precisely the one an unclean
	// exit leaves — the file present with nothing behind it.
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture did not leave a stale socket file: %v", err)
	}

	r := NewRouter(Config{ControlSocket: path}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.ListenControl(ctx) }()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// It must have REUSED the stale name — asking the resolver again now
	// would only tell us the router itself is answering there, which is
	// the point. So: the well-known path answers, and no per-pid sibling
	// was created beside it.
	if c, err := net.DialTimeout("unix", path, time.Second); err != nil {
		t.Errorf("nothing is listening on the stale path the router should have reused: %v", err)
	} else {
		_ = c.Close()
	}
	sibs, _ := filepath.Glob(filepath.Join(dir, "wash-1000.*.sock"))
	if len(sibs) != 0 {
		t.Errorf("router stepped aside from a stale socket, creating %v", sibs)
	}
}

func TestControlSocketCleansUpItsOwn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wash-1000.sock")
	r := NewRouter(Config{ControlSocket: path}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.ListenControl(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("router left its own socket behind: %v", err)
	}
}
