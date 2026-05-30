package fs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Sentinel errors for mutation-specific conditions that aren't
// covered by the standard os.Err* family. ErrCode maps each to a
// stable string for app-msg error replies.
var (
	ErrTooLarge    = errors.New("content exceeds size cap")
	ErrSamePath    = errors.New("from and to resolve to the same path")
	ErrNotEmpty    = errors.New("directory not empty")
	ErrNotEmptyDir = errors.New("target is a non-empty directory")
	ErrCrossDevice = errors.New("cross-device link")
	ErrForbidden   = errors.New("operation forbidden")
)

// Write writes content atomically: tmp file in the same directory,
// fsync, rename into place. Overwrites any existing target — this is
// the editor save path. Returns the resolved absolute path and the
// number of bytes written.
//
// maxBytes=0 means no cap. Anything else returns ErrTooLarge before
// touching the disk when content exceeds the cap.
func (f *FS) Write(p string, content []byte, maxBytes int) (abs string, n int, err error) {
	if p == "" {
		return "", 0, errors.New("missing path")
	}
	if maxBytes > 0 && len(content) > maxBytes {
		return "", 0, ErrTooLarge
	}
	abs, err = f.Confine(p)
	if err != nil {
		return "", 0, err
	}
	dir := filepath.Dir(abs)
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return abs, 0, err
	}
	tmp := filepath.Join(dir, ".wash-fs.tmp."+hex.EncodeToString(suffix))
	fp, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return abs, 0, err
	}
	n, werr := fp.Write(content)
	if werr == nil {
		werr = fp.Sync()
	}
	if cerr := fp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return abs, 0, werr
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		return abs, 0, classifyMutateErr(err)
	}
	return abs, n, nil
}

// CreateFile creates an empty file at p with O_EXCL — refuses to
// overwrite. Use Write to overwrite or to populate with content.
func (f *FS) CreateFile(p string) (abs string, err error) {
	if p == "" {
		return "", errors.New("missing path")
	}
	abs, err = f.Confine(p)
	if err != nil {
		return "", err
	}
	fp, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return abs, err
	}
	_ = fp.Close()
	return abs, nil
}

// CreateDir creates a single directory level (no MkdirAll). Refuses
// to clobber.
func (f *FS) CreateDir(p string) (abs string, err error) {
	if p == "" {
		return "", errors.New("missing path")
	}
	abs, err = f.Confine(p)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(abs, 0o755); err != nil {
		return abs, err
	}
	return abs, nil
}

// Rename moves from→to. If `to` already exists:
//   - replace=false → os.ErrExist
//   - replace=true and `to` is an empty dir or regular file → it's
//     removed first
//   - replace=true and `to` is a non-empty dir → ErrNotEmptyDir
//     (recursive replace belongs in bulk-ops, not the synchronous
//     fast path)
// from == to (resolved) → ErrSamePath.
func (f *FS) Rename(from, to string, replace bool) (src, dst string, err error) {
	if from == "" || to == "" {
		return "", "", errors.New("missing from or to")
	}
	src, err = f.Confine(from)
	if err != nil {
		return "", "", err
	}
	dst, err = f.Confine(to)
	if err != nil {
		return src, "", err
	}
	if src == dst {
		return src, dst, ErrSamePath
	}
	if _, err := os.Lstat(src); err != nil {
		return src, dst, err
	}
	if dstInfo, err := os.Lstat(dst); err == nil {
		if !replace {
			return src, dst, os.ErrExist
		}
		if err := os.Remove(dst); err != nil {
			if dstInfo.IsDir() && errors.Is(err, syscall.ENOTEMPTY) {
				return src, dst, ErrNotEmptyDir
			}
			return src, dst, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return src, dst, err
	}
	if err := os.Rename(src, dst); err != nil {
		return src, dst, classifyMutateErr(err)
	}
	return src, dst, nil
}

// Delete removes a single file or empty directory. Refuses to
// delete f.Root() itself (returns ErrForbidden) — sandbox-root
// destruction is a deliberate disallow. Non-empty dirs return
// ErrNotEmpty.
func (f *FS) Delete(p string) (abs string, err error) {
	if p == "" {
		return "", errors.New("missing path")
	}
	abs, err = f.Confine(p)
	if err != nil {
		return "", err
	}
	if f.root != "" && abs == f.root {
		return abs, ErrForbidden
	}
	if err := os.Remove(abs); err != nil {
		if errors.Is(err, syscall.ENOTEMPTY) {
			return abs, ErrNotEmpty
		}
		return abs, err
	}
	return abs, nil
}

// classifyMutateErr promotes a few low-level os errors to package
// sentinels callers can switch on without string-matching.
func classifyMutateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EXDEV) {
		return ErrCrossDevice
	}
	if strings.Contains(err.Error(), "cross-device") {
		return ErrCrossDevice
	}
	return err
}
