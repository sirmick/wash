package washmount

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/pkg/sftp"
	"github.com/sirmick/wash/internal/sftptest"
)

// TestRunBoundsConcurrency verifies the semaphore caps in-flight SFTP ops, so a
// burst can't swamp the single channel.
func TestRunBoundsConcurrency(t *testing.T) {
	client, cleanup, err := sftptest.New()
	if err != nil {
		t.Fatalf("sftp test server: %v", err)
	}
	defer cleanup()

	root := &sftpRoot{
		dial:      func() (*sftp.Client, error) { return client, nil },
		opTimeout: 5 * time.Second,
		sem:       make(chan struct{}, 2), // cap 2
	}

	var cur, max int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			root.run(context.Background(), "test", func(*sftp.Client) error {
				n := atomic.AddInt32(&cur, 1)
				for { // record the high-water mark
					m := atomic.LoadInt32(&max)
					if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
						break
					}
				}
				<-release
				atomic.AddInt32(&cur, -1)
				return nil
			})
		}()
	}
	time.Sleep(250 * time.Millisecond) // let all six pile into the semaphore
	if got := atomic.LoadInt32(&max); got > 2 {
		t.Fatalf("max concurrent ops = %d; want <= 2 (semaphore not bounding)", got)
	}
	close(release)
	wg.Wait()
}

// TestMountReconnectsAfterDrop is the Tier-2 chaos case: a real FUSE mount whose
// SFTP connection is killed mid-life must self-heal — no wedge, ops recover via
// a re-dial on the next request, and the mount still unmounts cleanly.
func TestMountReconnectsAfterDrop(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skipf("no /dev/fuse: %v", err)
	}
	backing := t.TempDir()
	mnt := t.TempDir()
	if err := os.WriteFile(filepath.Join(backing, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	addr, stopServer, err := sftptest.NewServer()
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	defer stopServer()

	var mu sync.Mutex
	var curSSH *ssh.Client
	dials := 0
	dial := func() (*sftp.Client, error) {
		c, sshConn, err := sftptest.Dial(addr)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		curSSH = sshConn
		dials++
		mu.Unlock()
		return c, nil
	}

	server, err := MountWithDialer(dial, Options{MountPoint: mnt, RemoteRoot: backing, OpTimeout: 3 * time.Second})
	if err != nil {
		t.Skipf("mount failed (FUSE unavailable?): %v", err)
	}
	t.Cleanup(func() { Unmount(server, mnt) })

	// Works before the drop.
	if b, err := os.ReadFile(filepath.Join(mnt, "a.txt")); err != nil || string(b) != "v1" {
		t.Fatalf("pre-drop read = %q, %v; want v1", b, err)
	}

	// Kill the connection.
	mu.Lock()
	curSSH.Close()
	mu.Unlock()

	// Create a NEW file directly on the backing store — the kernel can't serve a
	// never-seen name from cache, so the mount must hit the backend, which forces
	// the re-dial. The mount must recover within a few seconds and never hang
	// (each failed op returns promptly via the op timeout / connection error).
	if err := os.WriteFile(filepath.Join(backing, "after.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(mnt, "after.txt")); err == nil && string(b) == "v2" {
			recovered = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("mount did not recover after the connection drop (no reconnect)")
	}
	mu.Lock()
	d := dials
	mu.Unlock()
	if d < 2 {
		t.Fatalf("expected a re-dial after the drop, got %d dials", d)
	}
}
