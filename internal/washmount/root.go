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
	// dial (re)establishes the SFTP client. The mount calls it lazily and again
	// after a connection drop, so a transient ssh failure self-heals on the next
	// op — the FUSE mount stays mounted; ops just EIO until the next dial wins.
	// For a fixed-client mount it returns the same client every time.
	dial       func() (*sftp.Client, error)
	ownsClient bool // close a dead client we dialed (not a borrowed fixed one)

	clientMu sync.Mutex
	cur      *sftp.Client // current live client; nil before first dial / after a drop
	// dialing is non-nil while one re-dial is in flight; it closes when that
	// dial settles. Every op that finds cur == nil waits on the SAME dial (no
	// thundering herd of ssh forks), bounded by its own op deadline.
	dialing chan struct{}
	// After a failed dial, further dials are refused until nextDial (with
	// lastDialErr returned at once), the delay doubling per failure up to
	// maxDialBackoff. Without this every FUSE op against a down host forks
	// an ssh and waits its connect timeout — forever, serially.
	nextDial    time.Time
	dialBackoff time.Duration
	lastDialErr error

	// sem bounds in-flight SFTP ops so a burst can't swamp the single channel
	// (head-of-line blocking). Buffered to the configured max.
	sem chan struct{}

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

const (
	minDialBackoff = time.Second
	maxDialBackoff = 30 * time.Second
)

// cli returns a live SFTP client, dialing one if none is current (the first op,
// or the first op after a drop). The dial runs OFF the caller's goroutine and
// outside clientMu, so it is bounded by ctx (the op deadline) rather than by
// however long ssh takes to resolve, connect and authenticate — a FUSE handler
// parked on an unbounded dial is exactly the kernel tarpit run() exists to
// prevent, and under the mutex it would park every other op too. Concurrent
// ops share one in-flight dial. A failed dial is remembered: ops arriving
// before the backoff expires fail immediately with the same error instead of
// forking another ssh each.
func (r *sftpRoot) cli(ctx context.Context) (*sftp.Client, error) {
	r.clientMu.Lock()
	if r.cur != nil {
		c := r.cur
		r.clientMu.Unlock()
		return c, nil
	}
	if r.dialing == nil {
		if !r.nextDial.IsZero() && time.Now().Before(r.nextDial) {
			err := r.lastDialErr
			r.clientMu.Unlock()
			return nil, err
		}
		done := make(chan struct{})
		r.dialing = done
		go r.dialOnce(done)
	}
	done := r.dialing
	r.clientMu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		// The dial carries on in its goroutine; a later op finds its result.
		return nil, ctx.Err()
	}
	r.clientMu.Lock()
	defer r.clientMu.Unlock()
	if r.cur != nil {
		return r.cur, nil
	}
	return nil, r.lastDialErr
}

// dialOnce runs one dial and records its outcome under clientMu, closing done
// so waiters in cli wake. Success resets the backoff; failure escalates it.
func (r *sftpRoot) dialOnce(done chan struct{}) {
	c, err := r.dial()
	r.clientMu.Lock()
	if err == nil {
		r.cur = c
		r.dialBackoff = 0
		r.nextDial = time.Time{}
		r.lastDialErr = nil
	} else {
		if r.dialBackoff == 0 {
			r.dialBackoff = minDialBackoff
		} else if r.dialBackoff < maxDialBackoff {
			r.dialBackoff *= 2
			if r.dialBackoff > maxDialBackoff {
				r.dialBackoff = maxDialBackoff
			}
		}
		r.nextDial = time.Now().Add(r.dialBackoff)
		r.lastDialErr = err
		log.Printf("washmount: dial failed: %v (next attempt in %s)", err, r.dialBackoff)
	}
	r.dialing = nil
	r.clientMu.Unlock()
	close(done)
}

// markDead drops c if it is the current client, so the next cli() re-dials. The
// kernel/app retrying the EIO'd op is what drives the reconnect — no monitor
// goroutine, no in-line retry. (Open file handles from the dead client stay
// dead; apps re-open on EIO.)
func (r *sftpRoot) markDead(c *sftp.Client) {
	r.clientMu.Lock()
	defer r.clientMu.Unlock()
	if r.cur == c {
		r.cur = nil
		if r.ownsClient {
			c.Close()
		}
	}
}

// run executes a blocking SFTP call under a deadline, bounded by the concurrency
// semaphore, against a live (re-dialed if needed) client. The call itself cannot
// be cancelled (pkg/sftp predates context), so on timeout we abandon the
// goroutine — it unblocks when the connection eventually errors — and return EIO
// to the kernel immediately. Never make the kernel wait on something that can
// wait forever. A transport-level failure drops the client so the next op
// reconnects.
func (r *sftpRoot) run(ctx context.Context, what string, fn func(cl *sftp.Client) error) syscall.Errno {
	cctx, cancel := context.WithTimeout(ctx, r.opTimeout)
	defer cancel()

	// Bound in-flight ops; never block past the deadline. The slot is freed
	// by the worker goroutine below (not here): an abandoned op — one we time
	// out on — must keep holding its slot until it actually finishes, or
	// MaxInflight is defeated exactly when the server is congested (N ops time
	// out, free their slots, the next N are admitted while the first N still
	// pump the one ssh channel) — TODO-sftp-mount-bugs.md Medium.
	select {
	case r.sem <- struct{}{}:
	case <-cctx.Done():
		log.Printf("washmount: op queue full op=%s: returning EIO", what)
		return syscall.EIO
	}

	cl, err := r.cli(cctx)
	if err != nil {
		<-r.sem // nothing was spawned to release it
		if cctx.Err() != nil {
			log.Printf("washmount: dial still pending at op deadline op=%s timeout=%s: returning EIO", what, r.opTimeout)
			return syscall.EIO
		}
		log.Printf("washmount: connect for op=%s: %v", what, err)
		return toErrno(err)
	}

	done := make(chan error, 1) // buffered so the abandoned goroutine never leaks on send
	go func() {
		defer func() { <-r.sem }() // free the slot only when the op truly completes
		done <- fn(cl)
	}()

	select {
	case err := <-done:
		if isConnError(err) {
			r.markDead(cl) // next op re-dials
		}
		return toErrno(err)
	case <-cctx.Done():
		// A timed-out op means the transport is stuck — and a SILENT link
		// death (NAT/conntrack drop, suspended laptop, cable pull) produces no
		// error to trip the done arm, only this hang. Drop the client so the
		// next op re-dials; without this, r.cur keeps handing back the stalled
		// client and every later op times out the same way, wedging the mount
		// with no recovery (TODO-sftp-mount-bugs.md High). markDead is
		// idempotent and also closes the client, which errors out (and so
		// unblocks) the abandoned goroutine.
		r.markDead(cl)
		log.Printf("washmount: op timed out op=%s timeout=%s: returning EIO", what, r.opTimeout)
		return syscall.EIO
	}
}

// isConnError reports whether err means the SFTP transport itself died (vs a
// per-file error like ENOENT), so the mount should drop the client and re-dial.
//
// Rather than match transport error strings (which differ between an ssh.Dial
// client and a NewClientPipe-over-subprocess client — the supervisor uses the
// latter), classify by what is NOT a transport death: a *sftp.StatusError is a
// real per-file server response, and the os.Is sentinels are per-file too.
// Anything else over a network/pipe transport is a dead channel — treat it as
// one so the next op reconnects. Being over-eager only costs a cheap re-dial.
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	var se *sftp.StatusError
	if errors.As(err, &se) {
		return false // the server answered → a per-file error, not a dead channel
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrPermission) {
		return false
	}
	return true
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
//
// owner is the client f was opened on. After a connection drop the mount
// re-dials, but the kernel keeps handing back fds opened before it: an op on
// such a handle must NOT be run against the dead f — pkg/sftp answers it with
// a connection-lost error, which isConnError (rightly) classes as a transport
// death, and run() would then markDead the NEW, healthy client it just handed
// us. That tore the connection down once per op on every pre-drop fd (an
// editor with a file open became a reconnect loop). Instead, get() re-opens
// the file by path on the current client, transparently.
type fileHandle struct {
	mu    sync.Mutex
	f     *sftp.File
	owner *sftp.Client
	flags int // open flags, minus creation flags, for a re-open after a drop
}

// get returns the open file for use on cl, re-opening path on cl if f belongs
// to a client that has since been dropped. Caller holds h.mu. An error here
// comes from cl itself, so run()'s transport classification stays valid.
func (h *fileHandle) get(cl *sftp.Client, path string) (*sftp.File, error) {
	if h.owner == cl {
		return h.f, nil
	}
	f, err := cl.OpenFile(path, h.flags)
	if err != nil {
		return nil, err
	}
	// The old server-side handle died with its connection; don't Close it
	// (that would only produce another connection-lost error).
	h.f, h.owner = f, cl
	log.Printf("washmount: re-opened %s on the reconnected client", path)
	return f, nil
}

// reopenFlags strips the creation-time flags from an open's flags so a re-open
// after a drop neither truncates nor insists the file be new.
func reopenFlags(flags int) int {
	return flags &^ (os.O_CREATE | os.O_EXCL | os.O_TRUNC)
}
