package fs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrBadRelPath is returned when an upload's relative path can't be
// safely placed under the destination — an absolute path, an empty
// component, or a "." / ".." traversal. The FE sanitizes too, but a
// malicious or buggy client must not be able to escape the dest dir.
var ErrBadRelPath = errors.New("invalid upload relative path")

// SanitizeRel validates a slash-separated relative upload path (the
// browser's File.webkitRelativePath form) and returns the cleaned,
// OS-separated relative path. It rejects absolute paths and any path
// whose components include "", ".", or "..". The result is guaranteed
// to stay within whatever directory it's joined onto.
//
// Directory uploads carry paths like "photos/2024/img.jpg"; plain
// file uploads carry a bare "img.jpg".
func SanitizeRel(rel string) (string, error) {
	if rel == "" {
		return "", ErrBadRelPath
	}
	// Normalize Windows separators defensively, then split on "/".
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(rel, "/") {
		return "", ErrBadRelPath
	}
	parts := strings.Split(rel, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		switch p {
		case "", ".", "..":
			return "", ErrBadRelPath
		}
		out = append(out, p)
	}
	return filepath.Join(out...), nil
}

// UploadWriter is a confined, chunk-at-a-time single-file writer. The
// FE streams a file's bytes across many upload_chunk messages; each is
// appended here. On Commit the temp file is fsync'd and atomically
// renamed into place; on Abort it's discarded. Parent directories
// (for nested directory uploads) are created under the destination as
// the writer opens.
//
// One UploadWriter holds an open fd, so the upload session keeps at
// most one alive at a time (the FE streams files sequentially).
type UploadWriter struct {
	abs string // final confined destination path
	tmp string // sibling temp file currently being written
	fp  *os.File
	n   int64 // bytes written so far
	max int64 // per-file cap (0 = unlimited)
}

// NewUpload opens a writer for relPath placed under destDir. destDir
// is confined to the FS root; relPath is sanitized and its parent
// directories are created (MkdirAll) under destDir. The returned
// writer's final path is also re-confined as defense in depth.
//
// maxBytes caps the file size; Write returns ErrTooLarge once the cap
// is exceeded. 0 disables the cap.
func (f *FS) NewUpload(destDir, relPath string, maxBytes int64) (*UploadWriter, error) {
	dirAbs, err := f.Confine(destDir)
	if err != nil {
		return nil, err
	}
	clean, err := SanitizeRel(relPath)
	if err != nil {
		return nil, err
	}
	abs, err := f.Confine(filepath.Join(dirAbs, clean))
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	tmp := filepath.Join(parent, ".wash-upload.tmp."+hex.EncodeToString(suffix))
	fp, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &UploadWriter{abs: abs, tmp: tmp, fp: fp, max: maxBytes}, nil
}

// Abs returns the writer's final destination path.
func (w *UploadWriter) Abs() string { return w.abs }

// Write appends a chunk to the temp file, enforcing the per-file cap.
// On a cap violation it returns ErrTooLarge without writing the chunk.
func (w *UploadWriter) Write(chunk []byte) (int, error) {
	if w.max > 0 && w.n+int64(len(chunk)) > w.max {
		return 0, ErrTooLarge
	}
	n, err := w.fp.Write(chunk)
	w.n += int64(n)
	return n, err
}

// Exists reports whether the final destination path already exists.
// The upload session calls this before Commit to apply the user's
// conflict policy (replace vs skip).
func (w *UploadWriter) Exists() bool {
	_, err := os.Lstat(w.abs)
	return err == nil
}

// Commit fsyncs and atomically renames the temp file into place. When
// the destination already exists, replace=true overwrites it (atomic
// for a regular file) and replace=false returns os.ErrExist after
// discarding the temp — the caller treats that as a skip. Either way
// the temp file is gone when Commit returns.
func (w *UploadWriter) Commit(replace bool) (string, error) {
	if err := w.fp.Sync(); err != nil {
		_ = w.fp.Close()
		_ = os.Remove(w.tmp)
		return "", err
	}
	if err := w.fp.Close(); err != nil {
		_ = os.Remove(w.tmp)
		return "", err
	}
	if !replace && w.Exists() {
		_ = os.Remove(w.tmp)
		return w.abs, os.ErrExist
	}
	if err := os.Rename(w.tmp, w.abs); err != nil {
		_ = os.Remove(w.tmp)
		return w.abs, classifyMutateErr(err)
	}
	return w.abs, nil
}

// Abort closes and removes the temp file, discarding any bytes
// written. Safe to call after Commit (the temp is already gone).
func (w *UploadWriter) Abort() error {
	_ = w.fp.Close()
	err := os.Remove(w.tmp)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
