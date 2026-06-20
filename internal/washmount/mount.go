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
	// Debug dumps the raw FUSE protocol traffic.
	Debug bool
}

// Mount exposes client's filesystem at opts.MountPoint as a kernel FUSE mount.
// It returns the running server; call server.Unmount() to detach, or
// server.Wait() to block until it is unmounted. The caller owns client and must
// keep it open for the lifetime of the mount.
func Mount(client *sftp.Client, opts Options) (*fuse.Server, error) {
	if opts.MountPoint == "" {
		return nil, fmt.Errorf("washmount: empty mount point")
	}
	remoteRoot := opts.RemoteRoot
	if remoteRoot == "" {
		remoteRoot = "/"
	}
	root := &sftpRoot{
		client:    client,
		base:      remoteRoot,
		opTimeout: orDur(opts.OpTimeout, 30*time.Second),
		uid:       orU32(opts.UID, uint32(os.Getuid())),
		gid:       orU32(opts.GID, uint32(os.Getgid())),
	}
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

func orU32(v, def uint32) uint32 {
	if v == 0 {
		return def
	}
	return v
}
