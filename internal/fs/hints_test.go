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

// TestListSymlinkTargetType: a symlink's Type stays "symlink" (the FE still
// renders it as a link) but LinkType says what it resolves to, so a link to
// a directory can be treated as one. Without it every FE that switches on
// Type alone files a linked folder with the regular files: un-enterable in
// fm and the file tree, and dropped outright by the directory picker.
func TestListSymlinkTargetType(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, l := range [][2]string{
		{"target", "to-dir"},
		{"plain.txt", "to-file"},
		{"nope", "dangling"},
	} {
		if err := os.Symlink(filepath.Join(dir, l[0]), filepath.Join(dir, l[1])); err != nil {
			t.Fatal(err)
		}
	}

	entries, _, _, err := New(dir).List(dir, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]Entry{}
	for _, e := range entries {
		got[e.Name] = e
	}

	for _, tc := range []struct {
		name     string
		wantType string
		wantLink string
		broken   bool
	}{
		{"target", "dir", "", false},
		{"plain.txt", "file", "", false},
		{"to-dir", "symlink", "dir", false},
		{"to-file", "symlink", "file", false},
		// A dangling link resolves to nothing, so LinkType stays empty —
		// callers must not treat "unknown" as "directory".
		{"dangling", "symlink", "", true},
	} {
		e, ok := got[tc.name]
		if !ok {
			t.Errorf("%s missing from listing", tc.name)
			continue
		}
		if e.Type != tc.wantType {
			t.Errorf("%s: type = %q, want %q", tc.name, e.Type, tc.wantType)
		}
		if e.LinkType != tc.wantLink {
			t.Errorf("%s: link_type = %q, want %q", tc.name, e.LinkType, tc.wantLink)
		}
		if e.Broken != tc.broken {
			t.Errorf("%s: broken = %v, want %v", tc.name, e.Broken, tc.broken)
		}
	}
}
