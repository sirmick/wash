package fs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func statT(t *testing.T, p string) *syscall.Stat_t {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	return st
}

func readString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestWriteNewFile(t *testing.T) {
	root := t.TempDir()
	f := New(root)

	abs, n, err := f.Write(filepath.Join(root, "new.txt"), []byte("hello"), 0)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if abs != filepath.Join(root, "new.txt") || n != 5 {
		t.Fatalf("Write = (%q, %d), want (%q, 5)", abs, n, filepath.Join(root, "new.txt"))
	}
	if got := readString(t, abs); got != "hello" {
		t.Fatalf("content = %q, want hello", got)
	}
	// 0644 minus whatever umask is in force: never executable, always
	// owner-rw. Root + umask 022 in CI lands on exactly 0644; a stricter
	// umask locally may drop group/other bits, so bound rather than pin.
	fi, _ := os.Stat(abs)
	if perm := fi.Mode().Perm(); perm&0o111 != 0 || perm&0o600 != 0o600 || perm&^0o644 != 0 {
		t.Fatalf("new file mode = %o, want 0644 minus umask", perm)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatalf("tmp file leaked: %v", entries)
	}
}

func TestWriteTooLarge(t *testing.T) {
	root := t.TempDir()
	f := New(root)
	p := filepath.Join(root, "cap.txt")
	if _, _, err := f.Write(p, []byte("0123456789"), 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cap breach touched disk: %v", err)
	}
}

func TestWriteOutsideRoot(t *testing.T) {
	root := t.TempDir()
	f := New(root)
	if _, _, err := f.Write(filepath.Join(root, "..", "escape.txt"), []byte("x"), 0); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v, want ErrOutsideRoot", err)
	}
}

// Saving through a symlink must land on the link's target and leave
// the link in place — the /etc/foo -> /data/foo editor case.
func TestWriteThroughSymlinkKeepsLink(t *testing.T) {
	root := t.TempDir()
	f := New(root)
	target := filepath.Join(root, "data", "foo")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "foo")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	abs, n, err := f.Write(link, []byte("new"), 0)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if abs != link {
		t.Fatalf("abs = %q, want the link path %q", abs, link)
	}
	if n != 3 {
		t.Fatalf("n = %d, want 3", n)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced by a %v", fi.Mode())
	}
	if dest, _ := os.Readlink(link); dest != target {
		t.Fatalf("link now points at %q, want %q", dest, target)
	}
	if got := readString(t, target); got != "new" {
		t.Fatalf("target content = %q, want new", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 1 {
		t.Fatalf("tmp file leaked next to target: %v", entries)
	}
}

// A symlink whose target lies outside the root is refused, not
// followed, and neither side is touched.
func TestWriteThroughEscapingSymlinkRefused(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside", "secret")
	for _, d := range []string{root, filepath.Dir(outside)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "leak")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	f := New(root)

	if _, _, err := f.Write(link, []byte("pwned"), 0); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("err = %v, want ErrOutsideRoot", err)
	}
	if got := readString(t, outside); got != "keep" {
		t.Fatalf("outside file was written: %q", got)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced by a %v", fi.Mode())
	}

	// Same thing one level up: a symlinked parent directory escaping
	// the root must not be usable to create a new file outside it.
	dirLink := filepath.Join(root, "outdir")
	if err := os.Symlink(filepath.Dir(outside), dirLink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Write(filepath.Join(dirLink, "fresh"), []byte("x"), 0); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("new file via escaping dir symlink: err = %v, want ErrOutsideRoot", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(outside), "fresh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file created outside root: %v", err)
	}
}

// The root itself may be reached through a symlink (macOS /tmp, a
// symlinked $HOME). Resolved paths then miss the lexical prefix check
// and must be re-checked against the resolved root instead of refused.
func TestWriteRootBehindSymlink(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, rootLink); err != nil {
		t.Fatal(err)
	}
	f := New(rootLink)
	p := filepath.Join(rootLink, "a.txt")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Write(p, []byte("new"), 0); err != nil {
		t.Fatalf("existing file under symlinked root: %v", err)
	}
	if _, _, err := f.Write(filepath.Join(rootLink, "b.txt"), []byte("new"), 0); err != nil {
		t.Fatalf("new file under symlinked root: %v", err)
	}
	if got := readString(t, filepath.Join(realRoot, "a.txt")); got != "new" {
		t.Fatalf("content = %q, want new", got)
	}
}

// A 0755 script stays 0755 after a save. Chmod is explicit so this
// holds whether or not CI runs as root with a 022 umask.
func TestWritePreservesMode(t *testing.T) {
	root := t.TempDir()
	f := New(root)
	p := filepath.Join(root, "run.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil { // defeat umask in setup
		t.Fatal(err)
	}
	before := statT(t, p)

	if _, _, err := f.Write(p, []byte("#!/bin/sh\necho hi\n"), 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o755 {
		t.Fatalf("mode = %o, want 0755", perm)
	}
	after := statT(t, p)
	if after.Uid != before.Uid || after.Gid != before.Gid {
		t.Fatalf("owner changed %d:%d -> %d:%d", before.Uid, before.Gid, after.Uid, after.Gid)
	}
	if after.Ino == before.Ino {
		t.Fatalf("expected the atomic path to produce a fresh inode")
	}
}

// Best-effort chown: as root the original owner must come back; as a
// normal user a cross-uid chown is EPERM and silently skipped, so only
// the root branch is checked.
func TestWritePreservesOwnerAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown to a foreign uid")
	}
	root := t.TempDir()
	f := New(root)
	p := filepath.Join(root, "owned")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(p, 65534, 65534); err != nil { // nobody
		t.Fatal(err)
	}
	if _, _, err := f.Write(p, []byte("new"), 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	st := statT(t, p)
	if st.Uid != 65534 || st.Gid != 65534 {
		t.Fatalf("owner = %d:%d, want 65534:65534", st.Uid, st.Gid)
	}
}

// Hard-linked files are rewritten in place so every name still sees
// the new content and the link count is intact.
func TestWriteHardLinkInPlace(t *testing.T) {
	root := t.TempDir()
	f := New(root)
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	if err := os.WriteFile(a, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(a, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, b); err != nil {
		t.Fatal(err)
	}
	before := statT(t, a)

	if _, n, err := f.Write(a, []byte("new content"), 0); err != nil || n != 11 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := readString(t, b); got != "new content" {
		t.Fatalf("other hard link sees %q, want new content", got)
	}
	after := statT(t, a)
	if after.Ino != before.Ino {
		t.Fatalf("inode changed %d -> %d: link set split", before.Ino, after.Ino)
	}
	if after.Nlink != 2 {
		t.Fatalf("nlink = %d, want 2", after.Nlink)
	}
	if fi, _ := os.Stat(a); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
	}
	if entries, _ := os.ReadDir(root); len(entries) != 2 {
		t.Fatalf("tmp file leaked: %v", entries)
	}
}

func TestWriteDanglingSymlinkRefused(t *testing.T) {
	root := t.TempDir()
	f := New(root)
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Write(link, []byte("x"), 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("dangling link was replaced by a %v", fi.Mode())
	}
}

// A rename across filesystems fails with EXDEV, which os.Rename wraps in
// a *LinkError. The sentinel and the wire code both have to come out of
// that shape: fm's FE keys its bulk-move fallback on "cross_device".
func TestRenameCrossDeviceClassified(t *testing.T) {
	err := classifyMutateErr(&os.LinkError{Op: "rename", Old: "/a", New: "/b", Err: syscall.EXDEV})
	if !errors.Is(err, ErrCrossDevice) {
		t.Fatalf("classifyMutateErr(EXDEV) = %v, want ErrCrossDevice", err)
	}
	if code := ErrCode(err); code != "cross_device" {
		t.Fatalf("ErrCode = %q, want cross_device", code)
	}
	// Anything else passes through untouched.
	other := &os.LinkError{Op: "rename", Old: "/a", New: "/b", Err: syscall.EACCES}
	if got := classifyMutateErr(other); got != other {
		t.Fatalf("classifyMutateErr(EACCES) = %v, want the original error", got)
	}
}
