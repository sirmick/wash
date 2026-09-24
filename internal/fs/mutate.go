package fs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
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

// Write writes content to p, preserving the identity of whatever is
// already there. Returns the confined path as given (callers key
// editor tabs by the path the user opened, so a symlink path comes
// back as the symlink path) and the number of bytes written.
//
// Identity rules for an existing target:
//
//   - Symlinks: the write lands on the link's final target
//     (filepath.EvalSymlinks), never on the link itself, so the link
//     survives. The resolved path is re-confined — a link that escapes
//     the FS root is refused with ErrOutsideRoot rather than followed.
//     Dangling links are refused with the underlying not-found error.
//   - Mode bits (incl. setuid/setgid/sticky) are copied from the
//     existing file; owner/group are re-applied best-effort (EPERM is
//     expected for a non-root writer saving another user's file and
//     is ignored; anything else is logged).
//   - Hard links (Nlink > 1): an atomic rename would split the link
//     set, leaving the other names pointing at the stale inode. For
//     that case we fall back to an in-place O_TRUNC write on the
//     existing inode — not crash-atomic, but it keeps what the user
//     has. Nlink == 1 files keep the default atomic path: tmp file in
//     the same directory as the resolved target, fsync, rename.
//
// A new file is created 0644 (minus umask) as before.
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
	target, existing, err := f.resolveWriteTarget(abs)
	if err != nil {
		return abs, 0, err
	}

	if existing != nil && existing.Mode().IsRegular() && nlinkOf(existing) > 1 {
		n, err = writeInPlace(target, content)
		return abs, n, err
	}

	dir := filepath.Dir(target)
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
	if werr == nil && existing != nil {
		werr = copyIdentity(fp, target, existing)
	}
	if cerr := fp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return abs, 0, werr
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return abs, 0, classifyMutateErr(err)
	}
	return abs, n, nil
}

// resolveWriteTarget maps a confined path to the real path a write
// must land on. If abs exists, every symlink component (the leaf
// included) is followed; if it doesn't, the parent directory is
// resolved instead so the tmp file and rename stay on one device.
// Either way the resolved path is re-confined so a symlink cannot
// smuggle a write outside the root. existing is nil for a new file.
func (f *FS) resolveWriteTarget(abs string) (target string, existing os.FileInfo, err error) {
	if _, err := os.Lstat(abs); err == nil {
		target, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return "", nil, err
		}
		existing, err = os.Stat(target)
		if err != nil {
			return "", nil, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		dir, derr := filepath.EvalSymlinks(filepath.Dir(abs))
		if derr != nil {
			return "", nil, derr
		}
		target = filepath.Join(dir, filepath.Base(abs))
	} else {
		return "", nil, err
	}
	if err := f.confineResolved(target); err != nil {
		return "", nil, err
	}
	return target, existing, nil
}

// confineResolved checks a symlink-resolved path against the root.
// The root itself may sit behind a symlink (Confine is lexical and
// never resolves it), so a lexical miss is retried against the
// resolved root before refusing.
func (f *FS) confineResolved(resolved string) error {
	if f.root == "" {
		return nil
	}
	if _, err := f.Confine(resolved); err == nil {
		return nil
	}
	rootReal, err := filepath.EvalSymlinks(f.root)
	if err != nil || rootReal == f.root {
		return ErrOutsideRoot
	}
	_, err = (&FS{root: rootReal}).Confine(resolved)
	return err
}

// copyIdentity re-applies the existing file's owner/group and mode
// bits to the freshly written tmp file. Chown runs first: on Linux a
// chown clears setuid/setgid, so the mode must land afterwards.
func copyIdentity(fp *os.File, target string, existing os.FileInfo) error {
	if st, ok := existing.Sys().(*syscall.Stat_t); ok {
		uid, gid := int(st.Uid), int(st.Gid)
		if uid != os.Geteuid() || gid != os.Getegid() {
			if err := fp.Chown(uid, gid); err != nil && !errors.Is(err, syscall.EPERM) {
				log.Printf("fs: write chown path=%q uid=%d gid=%d: %v", target, uid, gid, err)
			}
		}
	}
	mode := existing.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	return fp.Chmod(mode)
}

// writeInPlace truncates and rewrites the existing inode at target.
// Used for hard-linked files, where a rename would split the link
// set. Mode and ownership are untouched by construction.
func writeInPlace(target string, content []byte) (int, error) {
	fp, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return 0, err
	}
	n, werr := fp.Write(content)
	if werr == nil {
		werr = fp.Sync()
	}
	if cerr := fp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return 0, werr
	}
	return n, nil
}

// nlinkOf returns the hard-link count, or 1 when the platform
// doesn't expose it.
func nlinkOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
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
//
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
	return err
}
