package fs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeRel(t *testing.T) {
	ok := map[string]string{
		"img.jpg":             "img.jpg",
		"photos/2024/img.jpg": filepath.Join("photos", "2024", "img.jpg"),
		"a/b":                 filepath.Join("a", "b"),
		"weird name.txt":      "weird name.txt",
	}
	for in, want := range ok {
		got, err := SanitizeRel(in)
		if err != nil {
			t.Errorf("SanitizeRel(%q) unexpected err: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("SanitizeRel(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"",
		"/abs/path",
		"..",
		"../escape",
		"a/../../escape",
		"a/../b",   // any ".." component is rejected, even if it'd resolve inside
		"./hidden", // leading "." component
		"a/./b",    // interior "." component
		"a//b",     // empty component
		"\\\\abs",  // backslash-absolute after normalization
		"a/..",     // trailing ".."
	}
	for _, in := range bad {
		if _, err := SanitizeRel(in); !errors.Is(err, ErrBadRelPath) {
			t.Errorf("SanitizeRel(%q) err = %v, want ErrBadRelPath", in, err)
		}
	}
}

func TestUploadWriterRoundTrip(t *testing.T) {
	root := t.TempDir()
	f := New(root)

	w, err := f.NewUpload(root, "sub/dir/hello.txt", 0)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	for _, chunk := range [][]byte{[]byte("hello "), []byte("world")} {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	abs, err := w.Commit(false)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	want := filepath.Join(root, "sub", "dir", "hello.txt")
	if abs != want {
		t.Errorf("Abs = %q, want %q", abs, want)
	}
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("content = %q, want %q", got, "hello world")
	}
	// Parent directories were created under the destination.
	if fi, err := os.Stat(filepath.Join(root, "sub", "dir")); err != nil || !fi.IsDir() {
		t.Errorf("expected sub/dir to exist as a directory, err=%v", err)
	}
}

func TestUploadWriterSizeCap(t *testing.T) {
	root := t.TempDir()
	f := New(root)
	w, err := f.NewUpload(root, "big.bin", 4)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	if _, err := w.Write([]byte("abcd")); err != nil {
		t.Fatalf("Write within cap: %v", err)
	}
	if _, err := w.Write([]byte("e")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Write over cap err = %v, want ErrTooLarge", err)
	}
	_ = w.Abort()
	// Temp must be cleaned up — no stray .wash-upload.tmp.* left.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			t.Errorf("stray temp file left behind: %s", e.Name())
		}
	}
}

func TestUploadWriterConflictPolicy(t *testing.T) {
	root := t.TempDir()
	f := New(root)
	existing := filepath.Join(root, "dup.txt")
	if err := os.WriteFile(existing, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	// replace=false → os.ErrExist, original untouched, temp gone.
	w, err := f.NewUpload(root, "dup.txt", 0)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	_, _ = w.Write([]byte("new"))
	if _, err := w.Commit(false); !errors.Is(err, os.ErrExist) {
		t.Errorf("Commit(replace=false) err = %v, want os.ErrExist", err)
	}
	if got, _ := os.ReadFile(existing); string(got) != "original" {
		t.Errorf("skip should leave original, got %q", got)
	}

	// replace=true → overwrite.
	w2, err := f.NewUpload(root, "dup.txt", 0)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	_, _ = w2.Write([]byte("replaced"))
	if _, err := w2.Commit(true); err != nil {
		t.Fatalf("Commit(replace=true): %v", err)
	}
	if got, _ := os.ReadFile(existing); string(got) != "replaced" {
		t.Errorf("replace should overwrite, got %q", got)
	}
}

func TestUploadWriterConfinement(t *testing.T) {
	root := t.TempDir()
	f := New(root)
	// A traversal rel path is rejected before any fd opens.
	if _, err := f.NewUpload(root, "../escape.txt", 0); !errors.Is(err, ErrBadRelPath) {
		t.Errorf("NewUpload traversal err = %v, want ErrBadRelPath", err)
	}
	// A destDir outside the root is rejected by Confine.
	if _, err := f.NewUpload(filepath.Dir(root), "x.txt", 0); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("NewUpload outside-root err = %v, want ErrOutsideRoot", err)
	}
}
