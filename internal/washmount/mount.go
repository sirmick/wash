package washmount

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/pkg/sftp"
)

// Options configures a single mount.
type Options struct {
	// MountPoint is the local directory the remote tree appears at.
	MountPoint string
	// RemoteRoot is the path on the remote machine to expose as the mount root.
	// Empty means "/".
	RemoteRoot string
	// OpTimeout bounds each backend call. Zero selects a sane default.
	OpTimeout time.Duration
	// AttrTimeout / EntryTimeout control how long the kernel caches metadata.
	// Zero selects a default — a few seconds, the network-fs sweet spot between
	// staleness and a round-trip storm.
	AttrTimeout, EntryTimeout time.Duration
	// AllowOther exposes the mount to other users (needs user_allow_other in
	// /etc/fuse.conf, or root). This is what makes "every process sees it" true
	// across uids.
	AllowOther bool
	// UID/GID is the ownership presented locally. Zero defaults to the caller.
	UID, GID uint32
	// MaxInflight bounds concurrent SFTP ops so a burst can't swamp the single
	// channel (head-of-line). Zero selects a sane default.
	MaxInflight int
	// Debug dumps the raw FUSE protocol traffic.
	Debug bool
}

// Mount exposes client's filesystem at opts.MountPoint as a kernel FUSE mount,
// against a fixed client (no reconnect). The caller owns client and keeps it
// open for the mount's lifetime. See MountWithDialer for a self-healing mount.
func Mount(client *sftp.Client, opts Options) (*fuse.Server, error) {
	// A fixed-client mount is a dialer that always returns the same client; the
	// client is borrowed (not closed on a drop), and a real drop simply EIOs
	// (re-dial returns the same dead client) — recovery needs MountWithDialer.
	return mount(func() (*sftp.Client, error) { return client, nil }, false, opts)
}

// MountWithDialer is Mount with reconnect: dial (re)establishes the SFTP client,
// and the mount calls it again after a connection drop, so a transient ssh
// failure self-heals on the next op without unmounting. dial-created clients are
// owned (closed when dropped).
func MountWithDialer(dial func() (*sftp.Client, error), opts Options) (*fuse.Server, error) {
	return mount(dial, true, opts)
}

func mount(dial func() (*sftp.Client, error), ownsClient bool, opts Options) (*fuse.Server, error) {
	if opts.MountPoint == "" {
		return nil, fmt.Errorf("washmount: empty mount point")
	}
	remoteRoot := opts.RemoteRoot
	if remoteRoot == "" {
		remoteRoot = "/"
	}
	root := &sftpRoot{
		dial:       dial,
		ownsClient: ownsClient,
		base:       remoteRoot,
		opTimeout:  orDur(opts.OpTimeout, 30*time.Second),
		uid:        orU32(opts.UID, uint32(os.Getuid())),
		gid:        orU32(opts.GID, uint32(os.Getgid())),
		sem:        make(chan struct{}, orInt(opts.MaxInflight, 16)),
	}
	// Dial once up front: fail fast on an unreachable host, and seed the live
	// client so the mount is usable on its very first op — no lazy first-op dial
	// that could race or stall the process that just cd'd into the mount. The
	// dialer is still used for reconnects after a drop.
	c, err := dial()
	if err != nil {
		return nil, fmt.Errorf("washmount: initial dial: %w", err)
	}
	root.cur = c
	rootNode := &sftpNode{root: root}

	attr := orDur(opts.AttrTimeout, 3*time.Second)
	entry := orDur(opts.EntryTimeout, 3*time.Second)
	fsOpts := &fs.Options{
		AttrTimeout:  &attr,
		EntryTimeout: &entry,
		// Surface go-fuse diagnostics (incl. fusermount's own stderr) so a mount
		// failure says *why* — e.g. /dev/fuse perms — instead of just an exit code.
		Logger: log.New(os.Stderr, "washmount-fuse: ", log.LstdFlags),
	}
	fsOpts.MountOptions.AllowOther = opts.AllowOther
	fsOpts.MountOptions.Debug = opts.Debug
	fsOpts.MountOptions.FsName = "washmount" // shows as the device in `mount`/df
	fsOpts.MountOptions.Name = "washfs"

	server, err := fs.Mount(opts.MountPoint, rootNode, fsOpts)
	if err != nil {
		return nil, fmt.Errorf("washmount: mount %s: %w", opts.MountPoint, err)
	}
	return server, nil
}

func orDur(v, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	return v
}

func orInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func orU32(v, def uint32) uint32 {
	if v == 0 {
		return def
	}
	return v
}
