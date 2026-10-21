package washmount

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/sirmick/wash/internal/sftptest"
)

func newTestRoot(dial func() (*sftp.Client, error), timeout time.Duration) *sftpRoot {
	return &sftpRoot{dial: dial, ownsClient: true, opTimeout: timeout, sem: make(chan struct{}, 4)}
}

// TestRunDoesNotBlockOnDial: a dial that hangs (DNS, a stuck agent, a half-open
// link) must not park the FUSE handler past the op deadline — nor the other
// handlers that queue behind the client mutex.
func TestRunDoesNotBlockOnDial(t *testing.T) {
	release := make(chan struct{})
	root := newTestRoot(func() (*sftp.Client, error) {
		<-release
		return nil, errors.New("released")
	}, 200*time.Millisecond)
	defer close(release)

	start := time.Now()
	errs := make(chan syscall.Errno, 2)
	for i := 0; i < 2; i++ {
		go func() {
			errs <- root.run(context.Background(), "test", func(*sftp.Client) error { return nil })
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case e := <-errs:
			if e != syscall.EIO {
				t.Fatalf("run = %v; want EIO", e)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("run blocked on the dial past the op deadline")
		}
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("ops took %s; the dial must be bounded by the op timeout", took)
	}
}

// TestDialBackoff: after a failed dial, ops within the backoff window fail at
// once with the recorded error instead of each forking a fresh dial.
func TestDialBackoff(t *testing.T) {
	var dials atomic.Int32
	want := errors.New("host down")
	root := newTestRoot(func() (*sftp.Client, error) {
		dials.Add(1)
		return nil, want
	}, time.Second)

	for i := 0; i < 5; i++ {
		if e := root.run(context.Background(), "test", func(*sftp.Client) error { return nil }); e != syscall.EIO {
			t.Fatalf("op %d = %v; want EIO", i, e)
		}
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("dials = %d; want 1 (backoff must suppress re-dials)", n)
	}
	root.clientMu.Lock()
	if root.dialBackoff != minDialBackoff || root.lastDialErr != want {
		t.Fatalf("backoff=%s err=%v; want %s / %v", root.dialBackoff, root.lastDialErr, minDialBackoff, want)
	}
	root.nextDial = time.Now() // expire the window
	root.clientMu.Unlock()
	root.run(context.Background(), "test", func(*sftp.Client) error { return nil })
	if n := dials.Load(); n != 2 {
		t.Fatalf("dials after window = %d; want 2", n)
	}
	root.clientMu.Lock()
	defer root.clientMu.Unlock()
	if root.dialBackoff != 2*minDialBackoff {
		t.Fatalf("backoff after second failure = %s; want %s", root.dialBackoff, 2*minDialBackoff)
	}
}

// TestConcurrentOpsShareOneDial: N ops arriving with no live client trigger a
// single dial, not N ssh forks.
func TestConcurrentOpsShareOneDial(t *testing.T) {
	client, cleanup, err := sftptest.New()
	if err != nil {
		t.Fatalf("sftp test server: %v", err)
	}
	defer cleanup()
	var dials atomic.Int32
	gate := make(chan struct{})
	root := newTestRoot(func() (*sftp.Client, error) {
		dials.Add(1)
		<-gate
		return client, nil
	}, 5*time.Second)
	root.ownsClient = false

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			root.run(context.Background(), "test", func(*sftp.Client) error { return nil })
			done <- struct{}{}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	for i := 0; i < 8; i++ {
		<-done
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("dials = %d; want 1", n)
	}
}

// TestStaleHandleReopensOnNewClient: an fd opened before a drop is served by
// re-opening the path on the reconnected client, never by running the dead
// handle (whose connection-lost error would get the NEW client marked dead).
func TestStaleHandleReopensOnNewClient(t *testing.T) {
	addr, stop, err := sftptest.NewServer()
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	defer stop()
	dir := t.TempDir()
	path := dir + "/f.txt"

	a, sshA, err := sftptest.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	fa, err := a.OpenFile(path, syscall.O_RDWR|syscall.O_CREAT)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fa.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	h := &fileHandle{f: fa, owner: a, flags: reopenFlags(syscall.O_RDWR | syscall.O_CREAT)}

	sshA.Close() // the drop
	b, sshB, err := sftptest.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer sshB.Close()

	// The root should never markDead b for an op routed through h.
	root := newTestRoot(func() (*sftp.Client, error) { return b, nil }, 5*time.Second)
	root.ownsClient = false
	root.cur = b
	buf := make([]byte, 16)
	var n int
	if e := root.run(context.Background(), "read", func(cl *sftp.Client) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		f, err := h.get(cl, path)
		if err != nil {
			return err
		}
		n, err = f.ReadAt(buf, 0)
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}); e != 0 && e != syscall.EIO {
		t.Fatalf("read via stale handle = %v", e)
	}
	if string(buf[:n]) != "before" {
		t.Fatalf("read %q; want %q", buf[:n], "before")
	}
	if h.owner != b {
		t.Fatal("handle was not re-owned by the new client")
	}
	root.clientMu.Lock()
	cur := root.cur
	root.clientMu.Unlock()
	if cur != b {
		t.Fatal("the live client was marked dead by a stale-handle op")
	}
}
