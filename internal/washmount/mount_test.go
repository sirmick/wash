package washmount

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/sftptest"
)

// TestMountRoundTrip is the Tier-1 gate: a real FUSE mount backed by SFTP, with
// every operation exercised through the kernel via the os package, and the
// results cross-checked against the backing directory the server serves.
func TestMountRoundTrip(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skipf("no /dev/fuse, cannot mount: %v", err)
	}

	backing := t.TempDir() // what the "remote" actually stores
	mnt := t.TempDir()     // where it appears locally

	client, cleanup, err := sftptest.New()
	if err != nil {
		t.Fatalf("sftp test server: %v", err)
	}
	t.Cleanup(cleanup)

	server, err := Mount(client, Options{
		MountPoint: mnt,
		RemoteRoot: backing,
		OpTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Skipf("mount failed (FUSE unavailable in this env?): %v", err)
	}
	t.Cleanup(func() { server.Unmount() })

	// write through the mount, verify it landed in the backing store
	want := []byte("hello from fuse\n")
	if err := os.WriteFile(filepath.Join(mnt, "greeting.txt"), want, 0o644); err != nil {
		t.Fatalf("write via mount: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(backing, "greeting.txt")); err != nil || string(got) != string(want) {
		t.Fatalf("backing readback = %q, %v; want %q", got, err, want)
	}

	// read back through the mount
	if got, err := os.ReadFile(filepath.Join(mnt, "greeting.txt")); err != nil || string(got) != string(want) {
		t.Fatalf("mount readback = %q, %v; want %q", got, err, want)
	}

	// mkdir + readdir
	if err := os.Mkdir(filepath.Join(mnt, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mnt, "sub", "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write in subdir: %v", err)
	}
	entries, err := os.ReadDir(mnt)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["greeting.txt"] || !names["sub"] {
		t.Fatalf("readdir missing entries, got %v", names)
	}

	// rename
	if err := os.Rename(filepath.Join(mnt, "greeting.txt"), filepath.Join(mnt, "renamed.txt")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backing, "renamed.txt")); err != nil {
		t.Fatalf("renamed file not in backing: %v", err)
	}

	// truncate
	if err := os.Truncate(filepath.Join(mnt, "renamed.txt"), 5); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(mnt, "renamed.txt")); err != nil || fi.Size() != 5 {
		t.Fatalf("size after truncate = %v, %v; want 5", fi, err)
	}

	// symlink + readlink
	if err := os.Symlink("renamed.txt", filepath.Join(mnt, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(mnt, "link")); err != nil || target != "renamed.txt" {
		t.Fatalf("readlink = %q, %v; want renamed.txt", target, err)
	}

	// remove
	if err := os.Remove(filepath.Join(mnt, "renamed.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backing, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still in backing after remove: %v", err)
	}
}
