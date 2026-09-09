package bulkops

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---- the zip-slip guard ----

func TestSafeJoinRefusesEscapes(t *testing.T) {
	dest := "/tmp/dest"
	bad := []string{
		"../evil.txt",
		"../../etc/passwd",
		"/etc/passwd",
		"sub/../../evil",
		"",
	}
	for _, name := range bad {
		if got, err := safeJoin(dest, name); err == nil {
			t.Errorf("safeJoin(%q) = %q, want a refusal", name, got)
		}
	}
	ok := map[string]string{
		"a.txt":         "/tmp/dest/a.txt",
		"sub/a.txt":     "/tmp/dest/sub/a.txt",
		"./sub/../a":    "/tmp/dest/a",
		"deep/x/../y.c": "/tmp/dest/deep/y.c",
	}
	for name, want := range ok {
		got, err := safeJoin(dest, name)
		if err != nil {
			t.Errorf("safeJoin(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("safeJoin(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSafeSymlinkTargetRefusesEscapes(t *testing.T) {
	dest := "/tmp/dest"
	link := filepath.Join(dest, "sub", "l")
	for _, target := range []string{"/etc/passwd", "../../etc/passwd", "../../../x", ""} {
		if err := safeSymlinkTarget(dest, link, target); err == nil {
			t.Errorf("target %q accepted", target)
		}
	}
	for _, target := range []string{"a.txt", "../a.txt", "./deeper/x"} {
		if err := safeSymlinkTarget(dest, link, target); err != nil {
			t.Errorf("target %q refused: %v", target, err)
		}
	}
}

func TestExtractRefusesTraversingEntriesButKeepsTheRest(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "evil.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"good.txt", "../escaped.txt", "sub/../../also-escaped.txt"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("payload")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(root, "out")
	st, err := Extract(src, dest, nil, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if st.Refused != 2 {
		t.Fatalf("refused=%d, want 2", st.Refused)
	}
	if st.Files != 1 {
		t.Fatalf("files=%d, want 1", st.Files)
	}
	assertFile(t, filepath.Join(dest, "good.txt"), "payload")
	for _, p := range []string{filepath.Join(root, "escaped.txt"), filepath.Join(root, "also-escaped.txt")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("%s was written outside the destination", p)
		}
	}
}

func TestExtractRefusesASymlinkPointingOut(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "link.tar")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	mustTarSymlink(t, tw, "escape", "../../../etc/passwd")
	mustTarSymlink(t, tw, "inside", "good.txt")
	mustTarFile(t, tw, "good.txt", "ok")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(root, "out")
	st, err := Extract(src, dest, nil, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if st.Refused != 1 || st.Symlinks != 1 {
		t.Fatalf("refused=%d symlinks=%d, want 1 and 1", st.Refused, st.Symlinks)
	}
	if _, err := os.Lstat(filepath.Join(dest, "escape")); !os.IsNotExist(err) {
		t.Fatal("the escaping symlink was created")
	}
	target, err := os.Readlink(filepath.Join(dest, "inside"))
	if err != nil || target != "good.txt" {
		t.Fatalf("inside link = %q, %v", target, err)
	}
}

// ---- round trips ----

func TestArchiveRoundTripPreservesEmptyDirsAndSymlinks(t *testing.T) {
	for _, name := range []string{"a.zip", "a.tar", "a.tar.gz", "a.tgz"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			tree := filepath.Join(root, "proj")
			mustMkdir(t, filepath.Join(tree, "empty"))
			mustMkdir(t, filepath.Join(tree, "sub"))
			mustWrite(t, filepath.Join(tree, "sub", "f.txt"), "content")
			if err := os.Symlink("sub/f.txt", filepath.Join(tree, "link")); err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			written := 0
			if err := WriteArchive(&buf, name, []string{tree}, func() { written++ }); err != nil {
				t.Fatalf("WriteArchive: %v", err)
			}
			if written != 5 { // proj, proj/empty, proj/link, proj/sub, proj/sub/f.txt
				t.Fatalf("wrote %d members, want 5", written)
			}
			arc := filepath.Join(root, name)
			if err := os.WriteFile(arc, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}

			n, err := CountEntries(arc)
			if err != nil {
				t.Fatalf("CountEntries: %v", err)
			}
			if n != 5 {
				t.Fatalf("CountEntries = %d, want 5", n)
			}

			dest := filepath.Join(root, "out")
			st, err := Extract(arc, dest, nil, nil)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if st.Refused != 0 {
				t.Fatalf("refused=%d", st.Refused)
			}
			assertFile(t, filepath.Join(dest, "proj", "sub", "f.txt"), "content")
			// The empty dir survived — the thing a contents-only archive drops.
			if fi, err := os.Stat(filepath.Join(dest, "proj", "empty")); err != nil || !fi.IsDir() {
				t.Fatalf("empty dir missing: %v", err)
			}
			// …and the symlink is still a symlink, not a copy of its target.
			target, err := os.Readlink(filepath.Join(dest, "proj", "link"))
			if err != nil {
				t.Fatalf("readlink: %v", err)
			}
			if target != "sub/f.txt" {
				t.Fatalf("link target = %q", target)
			}
		})
	}
}

func TestArchiveFormatOfAndUnsupported(t *testing.T) {
	for _, n := range []string{"a.zip", "A.ZIP", "a.tar", "a.tar.gz", "a.TGZ"} {
		if !IsExtractable(n) {
			t.Errorf("%s not recognised", n)
		}
	}
	// .tar.xz has no stdlib codec: recognised as unsupported, not as tar.
	for _, n := range []string{"a.tar.xz", "a.rar", "a.7z", "a.txt", "a"} {
		if IsExtractable(n) {
			t.Errorf("%s wrongly recognised", n)
		}
	}
	if _, err := Extract("/nonexistent/a.tar.xz", t.TempDir(), nil, nil); !errors.Is(err, ErrUnsupportedArchive) {
		t.Fatalf("err = %v, want ErrUnsupportedArchive", err)
	}
}

// ---- as queued jobs ----

func TestExtractJobReportsProgressAndLandsTheTree(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "proj")
	mustMkdir(t, filepath.Join(tree, "sub"))
	mustWrite(t, filepath.Join(tree, "sub", "f.txt"), "hello")
	var buf bytes.Buffer
	if err := WriteArchive(&buf, "x.tar.gz", []string{tree}, nil); err != nil {
		t.Fatal(err)
	}
	arc := filepath.Join(root, "x.tar.gz")
	if err := os.WriteFile(arc, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "out")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id, err := m.Enqueue(OpExtract, []string{arc}, dest)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if st, msg := waitForStatus(t, c, id, 5*time.Second); st != StatusDone {
		t.Fatalf("status=%s msg=%s", st, msg)
	}
	assertFile(t, filepath.Join(dest, "proj", "sub", "f.txt"), "hello")
	c.mu.Lock()
	defer c.mu.Unlock()
	last := c.updates[len(c.updates)-1]
	if last.Total != 3 || last.Done != 3 {
		t.Fatalf("done=%d total=%d, want 3/3", last.Done, last.Total)
	}
}

func TestExtractJobFailsOnATraversingArchive(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../escaped.txt")
	_, _ = w.Write([]byte("x"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	arc := filepath.Join(root, "evil.zip")
	if err := os.WriteFile(arc, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id, err := m.Enqueue(OpExtract, []string{arc}, filepath.Join(root, "out"))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	st, msg := waitForStatus(t, c, id, 5*time.Second)
	if st != StatusFailed {
		t.Fatalf("status=%s, want failed", st)
	}
	if msg == "" {
		t.Fatal("failed with no message")
	}
}

func TestCompressJobWritesTheArchiveAtomically(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "a")
	mustMkdir(t, filepath.Join(root, "d"))
	mustWrite(t, filepath.Join(root, "d", "b.txt"), "b")

	c := &collector{}
	m := New(WithOnUpdate(c.onUpdate))
	defer m.Close()

	id, err := m.EnqueueAs(OpCompress,
		[]string{filepath.Join(root, "a.txt"), filepath.Join(root, "d")},
		root, []string{"bundle.zip"})
	if err != nil {
		t.Fatalf("EnqueueAs: %v", err)
	}
	if st, msg := waitForStatus(t, c, id, 5*time.Second); st != StatusDone {
		t.Fatalf("status=%s msg=%s", st, msg)
	}
	out := filepath.Join(root, "bundle.zip")
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("archive missing: %v", err)
	}
	// No temp file left behind.
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if len(e.Name()) > 5 && e.Name()[:5] == ".wash" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	// And it round-trips.
	dest := filepath.Join(root, "out")
	if _, err := Extract(out, dest, nil, nil); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	assertFile(t, filepath.Join(dest, "a.txt"), "a")
	assertFile(t, filepath.Join(dest, "d", "b.txt"), "b")
}

func TestCompressRejectsAnUnsupportedName(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "a")
	m := New()
	defer m.Close()
	if _, err := m.EnqueueAs(OpCompress, []string{filepath.Join(root, "a.txt")}, root, []string{"x.tar.xz"}); err == nil {
		t.Fatal("a .tar.xz archive name was accepted")
	}
	if _, err := m.EnqueueAs(OpCompress, []string{filepath.Join(root, "a.txt")}, root, nil); err == nil {
		t.Fatal("compress without a name was accepted")
	}
}

func mustTarFile(t *testing.T, tw *tar.Writer, name, body string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func mustTarSymlink(t *testing.T, tw *tar.Writer, name, target string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777,
	}); err != nil {
		t.Fatal(err)
	}
}

// Guard against the gzip writer being skipped for a .tgz: a .tar.gz that
// is really a bare tar reads back as garbage in every other tool.
func TestTarGzIsActuallyGzipped(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "a")
	var buf bytes.Buffer
	if err := WriteArchive(&buf, "x.tgz", []string{filepath.Join(root, "a.txt")}, nil); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	defer gz.Close()
	if _, err := tar.NewReader(gz).Next(); err != nil {
		t.Fatalf("not a tar inside the gzip: %v", err)
	}
}
