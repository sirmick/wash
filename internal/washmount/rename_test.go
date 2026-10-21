package washmount

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/sftptest"
)

// TestRenameKeepsInodeIdentity: a file renamed through the mount must stay
// ONE inode. With inode numbers hashed from the path, the kernel's re-lookup
// of the new name (after the entry timeout) used to mint a second inode and
// orphan the renamed one, so fstat on an fd opened before the rename hit a
// go-fuse "deleted" placeholder and failed ENOENT, and `ls -i` showed two
// numbers for one file.
func TestRenameKeepsInodeIdentity(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skipf("no /dev/fuse: %v", err)
	}
	backing := t.TempDir()
	mnt := t.TempDir()
	client, cleanup, err := sftptest.New()
	if err != nil {
		t.Fatalf("sftp test server: %v", err)
	}
	t.Cleanup(cleanup)
	server, err := Mount(client, Options{
		MountPoint:   mnt,
		RemoteRoot:   backing,
		OpTimeout:    5 * time.Second,
		EntryTimeout: 10 * time.Millisecond,
		AttrTimeout:  10 * time.Millisecond,
	})
	if err != nil {
		t.Skipf("mount failed (FUSE unavailable?): %v", err)
	}
	t.Cleanup(func() { Unmount(server, mnt) })

	a, b := filepath.Join(mnt, "a.txt"), filepath.Join(mnt, "b.txt")
	if err := os.WriteFile(a, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(a, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(a, b); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // let the entry cache expire → kernel re-looks-up b
	after, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	ino := func(fi os.FileInfo) uint64 { return fi.Sys().(*syscall.Stat_t).Ino }
	if ino(before) != ino(after) {
		t.Errorf("inode changed across rename: %d → %d", ino(before), ino(after))
	}
	// fstat through the pre-rename fd must still resolve to the live file.
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("fstat on an fd opened before the rename: %v", err)
	}
	if ino(fi) != ino(after) {
		t.Errorf("fd inode %d != path inode %d", ino(fi), ino(after))
	}
	if err := f.Truncate(1); err != nil {
		t.Fatalf("ftruncate on the pre-rename fd: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(backing, "b.txt")); string(got) != "v" {
		t.Errorf("backing b.txt = %q after ftruncate; want %q", got, "v")
	}
}
