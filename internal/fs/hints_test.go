package fs

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestKindFromMode(t *testing.T) {
	cases := []struct {
		mode os.FileMode
		want string
	}{
		{0o644, "file"},
		{os.ModeDir | 0o755, "dir"},
		{os.ModeSymlink | 0o777, "symlink"},
		{os.ModeDevice | os.ModeCharDevice | 0o660, "chardev"},
		{os.ModeDevice | 0o660, "blockdev"},
		{os.ModeNamedPipe | 0o644, "fifo"},
		{os.ModeSocket | 0o755, "socket"},
		{os.ModeIrregular, "other"},
	}
	for _, c := range cases {
		if got := kindFromMode(c.mode); got != c.want {
			t.Errorf("kindFromMode(%v) = %q, want %q", c.mode, got, c.want)
		}
	}
}

// fifo + socket are creatable unprivileged, so we can exercise typeOf end
// to end (FileInfo → string) for those without root. block/char devices
// need mknod (root); kindFromMode covers their bit logic above.
func TestTypeOfRealSpecialFiles(t *testing.T) {
	dir := t.TempDir()

	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if fi, err := os.Lstat(fifo); err != nil {
		t.Fatal(err)
	} else if got := typeOf(fi); got != "fifo" {
		t.Errorf("fifo type = %q, want fifo", got)
	}

	sockPath := filepath.Join(dir, "sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer l.Close()
	if fi, err := os.Lstat(sockPath); err != nil {
		t.Fatal(err)
	} else if got := typeOf(fi); got != "socket" {
		t.Errorf("socket type = %q, want socket", got)
	}
}

func TestPermFor(t *testing.T) {
	cases := []struct {
		name                   string
		mode                   os.FileMode
		isRoot, isOwner, inGrp bool
		wantW, wantX           bool
	}{
		{"owner rwx", 0o700, false, true, false, true, true},
		{"owner read-only", 0o400, false, true, false, false, false},
		{"owner of 0755", 0o755, false, true, false, true, true},
		{"group member 0750", 0o750, false, false, true, false, true},
		{"group member 0740", 0o740, false, false, true, false, false},
		{"other on 0755", 0o755, false, false, false, false, true},
		{"other on 0700 (denied)", 0o700, false, false, false, false, false},
		{"other write on 0777", 0o777, false, false, false, true, true},
		{"root writes 0444", 0o444, true, false, false, true, false},
		{"root execs when any x (0744)", 0o744, true, false, false, true, true},
		{"root no x on 0644", 0o644, true, false, false, true, false},
	}
	for _, c := range cases {
		w, x := permFor(c.mode, c.isRoot, c.isOwner, c.inGrp)
		if w != c.wantW || x != c.wantX {
			t.Errorf("%s: permFor=%v,%v want %v,%v", c.name, w, x, c.wantW, c.wantX)
		}
	}
}

func TestUnescapeMountPath(t *testing.T) {
	cases := map[string]string{
		"/mnt/usb":         "/mnt/usb",
		`/mnt/my\040drive`: "/mnt/my drive",
		`/a\011b`:          "/a\tb",
		`/back\134slash`:   `/back\slash`,
		`/plain`:           "/plain",
	}
	for in, want := range cases {
		if got := unescapeMountPath(in); got != want {
			t.Errorf("unescapeMountPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// mountPoints should always include "/" (the root mount) on Linux, and a
// fresh tmpdir should not be a mount point.
func TestMountPointsIncludesRoot(t *testing.T) {
	set := mountPoints()
	if set == nil {
		t.Skip("mountinfo unreadable on this platform")
	}
	if !set["/"] {
		t.Error(`mountPoints() missing "/"`)
	}
	if set[t.TempDir()] {
		t.Error("a tmpdir should not be reported as a mount point")
	}
}
