package washmount

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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
			return nil // clean: go-fuse reaped its own loop goroutines
		} else {
			log.Printf("washmount: graceful unmount failed mp=%s: %v; escalating", mountpoint, err)
		}
		// A graceful failure means the backend is wedged: a lazy detach then
		// returns 0 while go-fuse's loop goroutines stay parked in
		// read(/dev/fuse), leaking. Force the kernel abort (parked reads get
		// ENODEV and exit), then Wait() to reap them — but only when the abort
		// actually happened, or Wait() would hang on a bare lazy success
		// (TODO-sftp-mount-bugs.md Low).
		aborted, err := forceUnmount(mountpoint, true)
		if err != nil {
			return err
		}
		if aborted {
			server.Wait()
		}
		return nil
	}
	_, err := forceUnmount(mountpoint, false)
	return err
}

// UnmountGraceful is the first rung only: a plain `fusermount3 -u`, which the
// kernel refuses (EBUSY) while any process holds the mount — an open fd, a
// cwd, an mmap, a parked request. It never escalates: a holder is almost
// always a shell or editor using the tree, not a wedge (every handler is
// bounded by the op timeout, so a parked request clears itself), and
// aborting it would turn that program's next I/O into a hard error. Use
// IsBusy to tell "in use" from a real failure, and Unmount to force.
func UnmountGraceful(server *fuse.Server) error {
	if server == nil {
		return errors.New("washmount: no server")
	}
	return server.Unmount()
}

// IsBusy reports whether an unmount error means the mount is in use.
// go-fuse surfaces fusermount's stderr ("Device or resource busy") in the
// error text; a direct umount2 path yields EBUSY itself.
func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, unix.EBUSY) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "busy")
}

// Holder is one process keeping a mount busy, and how (cwd, root, exe, fd,
// mmap).
type Holder struct {
	PID  int
	Comm string
	Via  string
}

// Holders lists the processes holding a reference under mountpoint, found by
// walking /proc the way `fuser -m` does: cwd/root/exe links, open fds, and
// mapped files. Only processes this uid may inspect are visible, which for a
// single-uid mount is exactly the set that can hold it. Best-effort: a pid
// that exits mid-walk is skipped. The calling process is excluded (it holds
// /dev/fuse, not the tree).
func Holders(mountpoint string) []Holder {
	mp := filepath.Clean(mountpoint)
	under := func(p string) bool {
		p = strings.TrimSuffix(p, " (deleted)")
		return p == mp || strings.HasPrefix(p, mp+"/")
	}
	self := os.Getpid()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []Holder
	for _, ent := range ents {
		pid, err := strconv.Atoi(ent.Name())
		if err != nil || pid == self {
			continue
		}
		dir := "/proc/" + ent.Name()
		via := ""
		for _, link := range []string{"cwd", "root", "exe"} {
			if t, err := os.Readlink(dir + "/" + link); err == nil && under(t) {
				via = link
				break
			}
		}
		if via == "" {
			if fds, err := os.ReadDir(dir + "/fd"); err == nil {
				for _, fd := range fds {
					if t, err := os.Readlink(dir + "/fd/" + fd.Name()); err == nil && under(t) {
						via = "fd"
						break
					}
				}
			}
		}
		if via == "" {
			if f, err := os.Open(dir + "/maps"); err == nil {
				sc := bufio.NewScanner(f)
				for sc.Scan() {
					fields := strings.Fields(sc.Text())
					if len(fields) >= 6 && under(strings.Join(fields[5:], " ")) {
						via = "mmap"
						break
					}
				}
				f.Close()
			}
		}
		if via == "" {
			continue
		}
		comm := ""
		if b, err := os.ReadFile(dir + "/comm"); err == nil {
			comm = strings.TrimSpace(string(b))
		}
		out = append(out, Holder{PID: pid, Comm: comm, Via: via})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// DescribeHolders renders Holders(mountpoint) for a status line, e.g.
// "bash (pid 4120, cwd), code (pid 4388, fd)". Empty when nothing was found
// (the holder may be another uid's process, or already gone).
func DescribeHolders(mountpoint string) string {
	hs := Holders(mountpoint)
	const max = 4
	parts := make([]string, 0, len(hs))
	for i, h := range hs {
		if i == max {
			parts = append(parts, fmt.Sprintf("+%d more", len(hs)-max))
			break
		}
		name := h.Comm
		if name == "" {
			name = "process"
		}
		parts = append(parts, fmt.Sprintf("%s (pid %d, %s)", name, h.PID, h.Via))
	}
	return strings.Join(parts, ", ")
}

// ClearStale detaches a mountpoint left behind by a mounter that died
// without unmounting (SIGKILL, OOM, a crash): the kernel keeps the FUSE
// mount but has aborted its connection, so every op on it — even the stat
// inside MkdirAll — fails ENOTCONN, and a fresh mount over it is refused.
// Only such a dead mountpoint is touched; a live mount (ours or anyone
// else's) is left alone. Reports whether a detach was performed.
func ClearStale(mountpoint string) (bool, error) {
	var st unix.Stat_t
	if err := unix.Stat(mountpoint, &st); !errors.Is(err, unix.ENOTCONN) {
		return false, nil
	}
	log.Printf("washmount: %s is a dead fuse mount (ENOTCONN); detaching it", mountpoint)
	_, err := forceUnmount(mountpoint, false)
	return true, err
}

// forceUnmount runs the lazy→abort escalation. wantAbort forces the kernel
// abort even when lazy detach succeeds (to release a wedged backend's parked
// /dev/fuse readers). It returns aborted=true only if the abort file was
// actually written, so the caller knows server.Wait() won't hang.
func forceUnmount(mountpoint string, wantAbort bool) (aborted bool, err error) {
	// Capture the FUSE connection id before detaching — once the mount is gone
	// from the namespace we can no longer stat it. fuseConnMinor also verifies
	// the path really is a fuse mount, so a stale/non-fuse path can't make us
	// abort an unrelated connection.
	minor, minorErr := fuseConnMinor(mountpoint)

	lazyErr := lazyDetach(mountpoint)
	if lazyErr != nil {
		log.Printf("washmount: lazy detach failed mp=%s: %v; aborting connection", mountpoint, lazyErr)
	} else if !wantAbort {
		return false, nil // healthy: lazy detach drains on its own
	}

	// Aborting needs the connection minor.
	if minorErr != nil {
		if lazyErr == nil {
			return false, nil // detached; just couldn't abort (not fuse / gone)
		}
		return false, fmt.Errorf("washmount: cannot locate fuse connection for %s: %w", mountpoint, minorErr)
	}
	abortPath := fmt.Sprintf("/sys/fs/fuse/connections/%d/abort", minor)
	if werr := os.WriteFile(abortPath, []byte("1"), 0o200); werr != nil {
		if lazyErr == nil {
			return false, nil // already detached; abort was only to reap goroutines
		}
		return false, fmt.Errorf("washmount: abort %s: %w", abortPath, werr)
	}
	// If the namespace detach hadn't happened yet (lazy failed), do it now that
	// the connection is dead.
	if lazyErr != nil {
		if derr := lazyDetach(mountpoint); derr != nil {
			return true, fmt.Errorf("washmount: detach after abort %s: %w", mountpoint, derr)
		}
	}
	return true, nil
}

// lazyDetach issues a lazy unmount, preferring the fusermount3 helper (works
// unprivileged) and falling back to the umount2 syscall with MNT_DETACH.
func lazyDetach(mountpoint string) error {
	if path, err := exec.LookPath("fusermount3"); err == nil {
		if out, err := exec.Command(path, "-uz", mountpoint).CombinedOutput(); err != nil {
			log.Printf("washmount: fusermount3 -uz %s: %v: %s", mountpoint, err, out)
		} else {
			return nil
		}
	}
	// Fallback: lazy umount syscall (needs privilege or an unprivileged
	// user-namespace mount, but covers hosts without the fusermount helper).
	if err := unix.Unmount(mountpoint, unix.MNT_DETACH); err != nil {
		return fmt.Errorf("umount2 MNT_DETACH: %w", err)
	}
	return nil
}

// fuseSuperMagic is statfs(2)'s f_type for a FUSE filesystem (linux/magic.h).
const fuseSuperMagic = 0x65735546

// fuseConnMinor returns the FUSE connection minor number backing mountpoint,
// which keys its directory under /sys/fs/fuse/connections. The connection id is
// the minor of the mount's device. It first confirms the path is actually a
// fuse mount: for a stale/non-fuse path, st.Dev is the BACKING fs's device, and
// the abort file built from that minor could abort an unrelated live fuse mount
// sharing the major-0 minor pool (TODO-sftp-mount-bugs.md Low).
func fuseConnMinor(mountpoint string) (uint32, error) {
	mp := filepath.Clean(mountpoint)
	var sf unix.Statfs_t
	if err := unix.Statfs(mp, &sf); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", mp, err)
	}
	if uint32(sf.Type) != fuseSuperMagic {
		return 0, fmt.Errorf("%s is not a fuse mount (fstype 0x%x)", mp, uint64(sf.Type))
	}
	var st unix.Stat_t
	if err := unix.Stat(mp, &st); err != nil {
		return 0, fmt.Errorf("stat %s: %w", mp, err)
	}
	return unix.Minor(uint64(st.Dev)), nil
}
