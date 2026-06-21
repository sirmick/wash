// Package mountsession ties the two channels of a wash-to-wash mount into one
// lifecycle: SFTP for data (a FUSE mount) and the wash watch-stream for change
// notification (registered into a fswatch.Router so a wash app's watch on the
// mountpoint is answered by the remote host's inotify).
//
// It is transport-agnostic: the caller supplies the already-dialed SFTP client
// and the already-opened watch channel (in production both ride ssh to the wash
// host; in tests they are an in-process server and a pipe). The supervisor that
// owns the host connection calls Open on mount and Close on unmount.
package mountsession

import (
	"fmt"
	"io"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/pkg/sftp"

	"github.com/sirmick/wash/internal/fswatch"
	"github.com/sirmick/wash/internal/remotewatch"
	"github.com/sirmick/wash/internal/washmount"
)

// Options configures one mount session.
type Options struct {
	// MountPoint is the local directory the remote tree appears at.
	MountPoint string
	// RemoteRoot is the path on the remote host exposed as the mount root.
	RemoteRoot string

	// SFTP is the data channel (caller-dialed). Required.
	SFTP *sftp.Client
	// Watch is the change-notification channel to the remote host's watch
	// daemon (caller-opened). Optional — without it the mount still works but
	// watch falls back to the kernel's attr-timeout eventual consistency.
	Watch io.ReadWriteCloser
	// Router is where the watch source is registered under MountPoint, so app
	// watches on the mountpoint route to the remote host. Required iff Watch
	// is set.

	Router *fswatch.Router
}

// Session is a live mount with its optional watch wiring.
type Session struct {
	mountpoint string
	server     *fuse.Server
	watch      *remotewatch.Client
	router     *fswatch.Router
}

// Open mounts the remote tree and, if a watch channel was supplied, registers
// its event stream into the Router under the mountpoint.
func Open(opts Options) (*Session, error) {
	if opts.SFTP == nil {
		return nil, fmt.Errorf("mountsession: nil SFTP client")
	}
	server, err := washmount.Mount(opts.SFTP, washmount.Options{
		MountPoint: opts.MountPoint,
		RemoteRoot: opts.RemoteRoot,
	})
	if err != nil {
		return nil, err
	}
	s := &Session{mountpoint: opts.MountPoint, server: server}

	if opts.Watch != nil {
		if opts.Router == nil {
			// Don't silently drop watch wiring — fail loudly so a miswire is caught.
			washmount.Unmount(server, opts.MountPoint)
			return nil, fmt.Errorf("mountsession: Watch set but Router is nil")
		}
		s.watch = remotewatch.NewClient(opts.Watch, remotewatch.PathMap{
			MountPoint: opts.MountPoint,
			RemoteRoot: opts.RemoteRoot,
		})
		s.router = opts.Router
		// The Router takes ownership of the watch client (Unmount/Close it).
		s.router.Mount(opts.MountPoint, s.watch)
	}
	return s, nil
}

// Close unregisters the watch (closing the watch client) and unmounts via the
// escalating escape hatch, so a dead backend never leaves a wedged mountpoint.
// The caller still owns the underlying transports (ssh procs, the SFTP client,
// the watch channel) and tears them down after Close returns.
func (s *Session) Close() error {
	if s.router != nil {
		s.router.Unmount(s.mountpoint) // closes the remotewatch.Client
	}
	return washmount.Unmount(s.server, s.mountpoint)
}

// MountPoint reports where the tree is mounted.
func (s *Session) MountPoint() string { return s.mountpoint }
