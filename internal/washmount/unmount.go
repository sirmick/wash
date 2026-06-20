package washmount

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
)

// Unmount detaches the mount at mountpoint, escalating until it succeeds so a
// dead or hung backend never strands the user with an unkillable mountpoint —
// the classic network-FUSE failure mode. The escalation, weakest to strongest:
//
//  1. graceful: server.Unmount() (FUSE flush, then fusermount). Works when the
//     backend is responsive.
//  2. lazy: `fusermount3 -uz` — detaches the tree from the namespace now and
//     releases the FUSE connection once in-flight ops drain. Survives a backend
//     that is slow but not wedged.
//  3. abort: write to /sys/fs/fuse/connections/<minor>/abort, which the kernel
//     guarantees frees even a wedged connection (every parked request returns
//     EIO), then lazy-detach the now-dead mount.
//
// server may be nil — a caller that only holds the mountpoint (e.g. cleaning up
// after a crashed mounter) still gets steps 2 and 3.
func Unmount(server *fuse.Server, mountpoint string) error {
	if server != nil {
		if err := server.Unmount(); err == nil {
			return nil
		} else {
			log.Printf("washmount: graceful unmount failed mp=%s: %v; escalating", mountpoint, err)
		}
	}
	return forceUnmount(mountpoint)
}

// forceUnmount runs the lazy→abort escalation. Exposed via Unmount.
func forceUnmount(mountpoint string) error {
	// Capture the FUSE connection id before detaching — once the mount is gone
	// from the namespace we can no longer stat it to find the connection.
	minor, minorErr := fuseConnMinor(mountpoint)

	if err := lazyDetach(mountpoint); err == nil {
		return nil
	} else {
		log.Printf("washmount: lazy detach failed mp=%s: %v; aborting connection", mountpoint, err)
	}

	// Last resort: abort the kernel-side connection so every parked request
	// returns EIO and the mount stops being a tarpit, then lazy-detach the husk.
	if minorErr != nil {
		return fmt.Errorf("washmount: cannot locate fuse connection for %s: %w", mountpoint, minorErr)
	}
	abortPath := fmt.Sprintf("/sys/fs/fuse/connections/%d/abort", minor)
	if err := os.WriteFile(abortPath, []byte("1"), 0o200); err != nil {
		return fmt.Errorf("washmount: abort %s: %w", abortPath, err)
	}
	// After abort the mount is dead; lazy-detach should now always succeed.
	if err := lazyDetach(mountpoint); err != nil {
		return fmt.Errorf("washmount: detach after abort %s: %w", mountpoint, err)
	}
	return nil
}

// lazyDetach issues a lazy unmount, preferring the fusermount3 helper (works
// unprivileged) and falling back to the umount2 syscall with MNT_DETACH.
func lazyDetach(mountpoint string) error {
	if path, err := exec.LookPath("fusermount3"); err == nil {
		if out, err := exec.Command(path, "-uz", mountpoint).CombinedOutput(); err == nil {
			return nil
		} else {
			log.Printf("washmount: fusermount3 -uz %s: %v: %s", mountpoint, err, out)
		}
	}
	// Fallback: lazy umount syscall (needs privilege or an unprivileged
	// user-namespace mount, but covers hosts without the fusermount helper).
	if err := unix.Unmount(mountpoint, unix.MNT_DETACH); err != nil {
		return fmt.Errorf("umount2 MNT_DETACH: %w", err)
	}
	return nil
}

// fuseConnMinor returns the FUSE connection minor number backing mountpoint,
// which keys its directory under /sys/fs/fuse/connections. The connection id is
// the minor of the mount's device.
func fuseConnMinor(mountpoint string) (uint32, error) {
	var st unix.Stat_t
	if err := unix.Stat(filepath.Clean(mountpoint), &st); err != nil {
		return 0, fmt.Errorf("stat %s: %w", mountpoint, err)
	}
	return unix.Minor(uint64(st.Dev)), nil
}
