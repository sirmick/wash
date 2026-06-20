// Package washmount mounts a remote machine's filesystem locally over SFTP,
// exposing it as a real kernel FUSE mount so every process on the box sees it
// — not just wash-aware apps.
//
// The design is a port of go-fuse's loopback example: each node delegates its
// operations to a backend, except the backend here is a *sftp.Client over an
// existing SSH connection rather than the local POSIX filesystem.
//
// Two robustness primitives are baked in from the start, because a FUSE handler
// that blocks forever wedges every process that touches the mount (the classic
// uninterruptible-D-state tarpit):
//
//   - run() bounds every backend call with a timeout. A slow or dead server
//     surfaces as EIO promptly; it never hangs the kernel indefinitely.
//   - toErrno() maps every backend error to a real errno, so failures appear
//     to applications as errors rather than as a hang or a blind EIO blizzard.
package washmount

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/pkg/sftp"
)

// sftpRoot is shared state for every node in one mounted filesystem.
type sftpRoot struct {
	client *sftp.Client

	// base is the remote path exposed as the mount root. A node's full remote
	// path is base joined with the node's position in the inode tree, so it
	// stays correct across renames (the inode moves; a stored path would not).
	base string

	// opTimeout bounds a single backend call. Past it, the handler returns EIO
	// rather than blocking the kernel request that is parked on it.
	opTimeout time.Duration

	// uid/gid the mount presents files as, since SFTP uids rarely match the
	// local user. Set from the mounting process by default.
	uid, gid uint32
}

// run executes a blocking SFTP call under a deadline. The call itself cannot be
// cancelled (pkg/sftp predates context), so on timeout we abandon the goroutine
// — it unblocks when the connection eventually errors — and return EIO to the
// kernel immediately. This is the single most important rule of a network FUSE
// fs: never make the kernel wait on something that can wait forever.
func (r *sftpRoot) run(ctx context.Context, what string, fn func() error) syscall.Errno {
	cctx, cancel := context.WithTimeout(ctx, r.opTimeout)
	defer cancel()

	done := make(chan error, 1) // buffered so the abandoned goroutine never leaks on send
	go func() { done <- fn() }()

	select {
	case err := <-done:
		return toErrno(err)
	case <-cctx.Done():
		log.Printf("washmount: op timed out op=%s timeout=%s: returning EIO", what, r.opTimeout)
		return syscall.EIO
	}
}

// toErrno maps a backend error to the errno the kernel expects. Anything we
// don't recognise becomes EIO — a clean, retryable failure — never a hang.
func toErrno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	// sftp surfaces server status as *sftp.StatusError but also implements the
	// os.Is* sentinels, so prefer those — they are transport-agnostic.
	switch {
	case errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, os.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, os.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, os.ErrClosed):
		return syscall.EBADF
	}
	// Translate the raw SFTP status codes we care about for sane app behaviour.
	var se *sftp.StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case uint32(sftp.ErrSshFxNoSuchFile):
			return syscall.ENOENT
		case uint32(sftp.ErrSshFxPermissionDenied):
			return syscall.EACCES
		case uint32(sftp.ErrSshFxOpUnsupported):
			return syscall.ENOSYS
		}
	}
	if errno, ok := err.(syscall.Errno); ok {
		return errno
	}
	log.Printf("washmount: unmapped backend error: %v: returning EIO", err)
	return syscall.EIO
}

// fillAttr translates an os.FileInfo from the SFTP backend into a fuse attr.
func (r *sftpRoot) fillAttr(fi os.FileInfo, out *fuse.Attr) {
	out.Mode = modeBits(fi.Mode())
	out.Size = uint64(fi.Size())
	mt := fi.ModTime()
	out.SetTimes(nil, &mt, nil)
	out.Owner = fuse.Owner{Uid: r.uid, Gid: r.gid}
	out.Nlink = 1
	// SFTP exposes the server's uid/gid/atime via FileStat; prefer them when
	// present so ownership and times round-trip rather than being faked.
	if st, ok := fi.Sys().(*sftp.FileStat); ok && st != nil {
		if st.Mtime != 0 {
			mt := time.Unix(int64(st.Mtime), 0)
			at := time.Unix(int64(st.Atime), 0)
			out.SetTimes(&at, &mt, nil)
		}
	}
}

// modeBits converts a Go os.FileMode to the raw mode the kernel wants, carrying
// the type bits (regular/dir/symlink) and the setuid/setgid/sticky specials.
func modeBits(m fs.FileMode) uint32 {
	bits := uint32(m.Perm())
	switch {
	case m.IsDir():
		bits |= syscall.S_IFDIR
	case m&fs.ModeSymlink != 0:
		bits |= syscall.S_IFLNK
	case m&fs.ModeNamedPipe != 0:
		bits |= syscall.S_IFIFO
	case m&fs.ModeSocket != 0:
		bits |= syscall.S_IFSOCK
	case m&fs.ModeDevice != 0:
		if m&fs.ModeCharDevice != 0 {
			bits |= syscall.S_IFCHR
		} else {
			bits |= syscall.S_IFBLK
		}
	default:
		bits |= syscall.S_IFREG
	}
	if m&fs.ModeSetuid != 0 {
		bits |= syscall.S_ISUID
	}
	if m&fs.ModeSetgid != 0 {
		bits |= syscall.S_ISGID
	}
	if m&fs.ModeSticky != 0 {
		bits |= syscall.S_ISVTX
	}
	return bits
}

// fileHandle wraps an open *sftp.File. SFTP file methods are not documented as
// safe for concurrent use, so a mutex guards positioned reads and writes.
type fileHandle struct {
	mu sync.Mutex
	f  *sftp.File
}
