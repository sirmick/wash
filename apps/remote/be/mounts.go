package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/sftp"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/sirmick/wash/internal/fswatchproto"
	"github.com/sirmick/wash/internal/sshtarget"
	"github.com/sirmick/wash/internal/washmount"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// Mount status values, surfaced to the FE under each host.
const (
	MountStarting = "mounting"
	MountUp       = "mounted"
	MountDown     = "error"
)

// MountState is one active (or failed) mount, published in State.Mounts.
type MountState struct {
	Host       string `json:"host"`
	RemoteRoot string `json:"remote_root"`
	MountPoint string `json:"mount_point"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	// Busy is set (while Status stays "mounted") when an unmount was refused
	// because processes hold the tree: who, from a /proc walk. The FE offers
	// a forced unmount (unmount{force:true}) next to it.
	Busy string `json:"busy,omitempty"`
}

// mountCtlReq / unmountCtlReq are the FE controls (relayed via wash-connect).
type mountCtlReq struct {
	Host       string `json:"host"`
	RemoteRoot string `json:"remote_root"`
	// Persist saves the mount so it re-establishes when com.wash.remote next
	// starts (login / reboot) — the "reconnect at launch" option.
	Persist bool `json:"persist"`
}

type unmountCtlReq struct {
	MountPoint string `json:"mount_point"`
	// Force runs the escalating detach (lazy + kernel abort) even if the
	// mount is in use — the holders' I/O then fails. Without it an in-use
	// mount is left mounted and reported busy.
	Force bool `json:"force"`
}

// mountManager owns the FUSE data mounts. It mounts the remote tree over SFTP
// and tells com.wash.fswatch to stream that mountpoint's changes; the watch
// channel is the service's to own (one per host, shared across watchers).
type mountManager struct {
	svc     stateMutator // narrow slice of StateService (see supervisor.svc)
	conn    *sdk.Conn
	sshPath string
	baseDir string

	mu      sync.Mutex
	entries map[string]*mountEntry // keyed by mountpoint
	// failed remembers the host:root behind each published error row, so
	// an "unmount" on one can forget its persisted entry and clear the row.
	failed map[string]savedMount

	// after / stop are the retry clock and the shutdown signal for
	// restoreMounts' retry loops (seams for tests).
	after func(time.Duration) <-chan time.Time
	stop  chan struct{}
}

// errInvalidMountHost marks a mount that can never succeed as specified, so a
// persisted-mount retry loop gives up on it rather than retrying forever.
var errInvalidMountHost = errors.New("invalid mount host")

const (
	// A persisted mount that fails at startup (host off-network, agent not
	// yet unlocked, laptop just woke) is retried with this backoff until it
	// mounts or the user forgets it.
	restoreRetryMin = 15 * time.Second
	restoreRetryMax = 5 * time.Minute
)

type mountEntry struct {
	server           *fuse.Server
	conn             *sftpConn
	host, remoteRoot string // for forgetting a persisted mount on unmount
	unmounting       bool   // a graceful unmount is in progress (guarded by m.mu)
}

// sftpConn is the per-mount ssh transport behind a reconnecting FUSE mount.
// dial() re-execs `ssh -s sftp` on each call (the first mount, and again after a
// drop), reaping the prior dead subprocess; closeCurrent() tears the live one
// down on unmount.
type sftpConn struct {
	sshPath, host string

	mu     sync.Mutex
	cmd    *exec.Cmd
	client *sftp.Client
	closed bool // closeCurrent ran: a dial that lands afterwards is torn down, not kept
}

func (c *sftpConn) dial() (*sftp.Client, error) {
	client, cmd, err := dialSFTP(c.sshPath, c.host)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		// Unmounted while this dial was in flight: nobody will close what we
		// just opened, so do it here rather than leak the ssh for good.
		c.mu.Unlock()
		client.Close()
		killCmd(cmd)
		return nil, errors.New("mount closed")
	}
	prev := c.cmd
	c.cmd, c.client = cmd, client
	c.mu.Unlock()
	killCmd(prev) // reap the dropped subprocess, if any
	return client, nil
}

func (c *sftpConn) closeCurrent() {
	c.mu.Lock()
	client, cmd := c.client, c.cmd
	c.client, c.cmd = nil, nil
	c.closed = true
	c.mu.Unlock()
	if client != nil {
		client.Close()
	}
	killCmd(cmd)
}

func newMountManager(svc *sdk.StateService[State], conn *sdk.Conn) *mountManager {
	return &mountManager{
		svc:     svc,
		conn:    conn,
		sshPath: "ssh",
		baseDir: defaultMountBase(),
		entries: map[string]*mountEntry{},
		failed:  map[string]savedMount{},
		after:   time.After,
		stop:    make(chan struct{}),
	}
}

func defaultMountBase() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "wash", "remote")
	}
	return filepath.Join(os.TempDir(), "wash-mounts")
}

// mountPointFor derives the local mountpoint for host:remoteRoot, e.g.
// ~/wash/remote/wash_at_host/project.
func (m *mountManager) mountPointFor(host, remoteRoot string) string {
	base := filepath.Base(filepath.Clean(remoteRoot))
	if base == "/" || base == "." || base == "" {
		base = "root"
	}
	return filepath.Join(m.baseDir, sanitizeHost(host), base)
}

// sanitizeHost makes a user@host:port string safe as a single path segment.
func sanitizeHost(host string) string {
	return strings.NewReplacer("@", "_at_", ":", "_", "/", "_").Replace(host)
}

// Overridable seams so the orchestration is unit-testable without real ssh/FUSE.
var (
	dialSFTP = func(sshPath, host string) (*sftp.Client, *exec.Cmd, error) {
		cmd := exec.Command(sshPath,
			"-o", "BatchMode=yes",
			"-o", "StrictHostKeyChecking=accept-new",
			// Surface a silent link death as an ssh error so the FUSE layer
			// can re-dial (conn.dial), instead of a half-open transport that
			// wedges every op at the 30s opTimeout. The relay supervisor
			// already does this; the mount path omitted it
			// (TODO-sftp-mount-bugs.md High: "op timeout never marks dead").
			"-o", "ConnectTimeout=10",
			"-o", "ServerAliveInterval=15",
			"-o", "ServerAliveCountMax=3",
			// "--": the host is user/LAN-supplied; never let ssh read it as
			// an option (sshtarget.Validate in mount() is the first line).
			"--", host, "-s", "sftp")
		// Backstop: if wash-remote dies abruptly, the kernel SIGTERMs this
		// sftp ssh instead of orphaning it (REVIEW-RECONNECT H5).
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
		wr, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, err
		}
		rd, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, err
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		client, err := sftp.NewClientPipe(rd, wr)
		if err != nil {
			killCmd(cmd)
			return nil, nil, err
		}
		return client, cmd, nil
	}

	mountWithDialer = func(dial func() (*sftp.Client, error), opts washmount.Options) (*fuse.Server, error) {
		return washmount.MountWithDialer(dial, opts)
	}

	unmountGraceful = washmount.UnmountGraceful
	describeHolders = washmount.DescribeHolders
)

// restoreMounts re-establishes every persisted ("reconnect at launch") mount.
// Run once at startup; each mount is async and retried with backoff until it
// mounts, the user forgets it (unmount on its error row), or shutdown.
func (m *mountManager) restoreMounts() {
	for _, sm := range loadSavedMounts() {
		go m.restoreWithRetry(sm)
	}
}

func (m *mountManager) restoreWithRetry(sm savedMount) {
	backoff := restoreRetryMin
	for {
		err := m.mount(sm.Host, sm.RemoteRoot, true)
		if err == nil || errors.Is(err, errInvalidMountHost) {
			return
		}
		after := m.after
		if after == nil {
			after = time.After
		}
		select {
		case <-after(backoff):
		case <-m.stop: // nil in a bare test literal: never fires, like no shutdown
			return
		}
		if !isSavedMount(sm.Host, sm.RemoteRoot) {
			return // forgotten meanwhile (unmount on the error row)
		}
		if backoff *= 2; backoff > restoreRetryMax {
			backoff = restoreRetryMax
		}
	}
}

// mount brings up a FUSE mount of host:remoteRoot and registers its watch with
// com.wash.fswatch. If persist, it is saved for re-mount at next launch. Blocks
// on the ssh handshake + mount; run it in a goroutine. Returns nil once the
// target is mounted (or already was), else the failure it also published.
func (m *mountManager) mount(host, remoteRoot string, persist bool) error {
	if host == "" {
		return errInvalidMountHost
	}
	if remoteRoot == "" {
		remoteRoot = "/"
	}

	// Reserve a mountpoint atomically with a placeholder entry. Concurrent
	// mounts of the same target are by design (restoreMounts spawns one, a
	// control spawns another); without the reservation both pass the
	// exists-check, both bring up a server+ssh child, and the second insert
	// clobbers the first into an unreachable leak (TODO-sftp-mount-bugs.md Low
	// TOCTOU). A concurrent caller now sees the placeholder and bails.
	mp, placeholder, ok := m.reserve(host, remoteRoot)
	if !ok {
		return nil // already mounted (or mounting)
	}

	// release drops our reservation, but only if it's still the placeholder
	// (success upgrades it in place; an interleaved unmount may have removed it).
	release := func() {
		m.mu.Lock()
		if m.entries[mp] == placeholder {
			delete(m.entries, mp)
		}
		m.mu.Unlock()
	}

	m.setMount(MountState{Host: host, RemoteRoot: remoteRoot, MountPoint: mp, Status: MountStarting})

	fail := func(stage string, err error) error {
		release()
		m.mu.Lock()
		if m.failed == nil {
			m.failed = map[string]savedMount{}
		}
		m.failed[mp] = savedMount{Host: host, RemoteRoot: remoteRoot}
		m.mu.Unlock()
		m.setMount(MountState{Host: host, RemoteRoot: remoteRoot, MountPoint: mp, Status: MountDown, Error: stage + ": " + err.Error()})
		return fmt.Errorf("%s: %w", stage, err)
	}

	// host becomes ssh's positional target (dialSFTP) and, via register_mount,
	// the watch service's too. Reject one ssh could parse as an option.
	if err := sshtarget.Validate(host); err != nil {
		return fail("host", fmt.Errorf("%w: %v", errInvalidMountHost, err))
	}
	// A previous wash-remote that died without unmounting (SIGKILL, OOM)
	// leaves a dead FUSE mount here: every op on it is ENOTCONN, MkdirAll
	// fails with a misleading "file exists", and no mount can go over it.
	// Detach it first; a live mount is left alone (and fails below).
	if cleared, err := washmount.ClearStale(mp); cleared && err != nil {
		return fail("clear stale mount", err)
	}
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return fail("mkdir", err)
	}
	conn := &sftpConn{sshPath: m.sshPath, host: host}
	// MountWithDialer dials eagerly: it fails fast on an unreachable host / bad
	// auth and seeds a live client so the mount works on first access (no lazy
	// first-op dial that could stall the process cd'ing in), and re-dials via
	// conn.dial after a drop.
	server, err := mountWithDialer(conn.dial, washmount.Options{MountPoint: mp, RemoteRoot: remoteRoot})
	if err != nil {
		conn.closeCurrent()
		return fail("mount", err)
	}
	m.mu.Lock()
	if m.entries[mp] != placeholder {
		// Unmounted while we were dialing — tear down what we just built.
		m.mu.Unlock()
		conn.closeCurrent()
		_ = washmount.Unmount(server, mp)
		return nil
	}
	placeholder.server, placeholder.conn = server, conn
	delete(m.failed, mp)
	m.mu.Unlock()

	if persist {
		addSavedMount(host, remoteRoot) // reconnect at launch
	}

	// Ask the shared watch service to stream this mount's changes (it opens its
	// own ssh wash-fswatchd to the host).
	m.registerWatch(host, remoteRoot, mp)
	m.setMount(MountState{Host: host, RemoteRoot: remoteRoot, MountPoint: mp, Status: MountUp})
	return nil
}

// reserve picks the mountpoint for host:remoteRoot and claims it with a
// placeholder entry. Two different roots with the same basename on one host
// (~/project and /srv/project) would land on one path: the later one gets a
// short hash suffix instead of silently no-oping against the first. ok is
// false when this exact target is already mounted or mounting.
func (m *mountManager) reserve(host, remoteRoot string) (mp string, placeholder *mountEntry, ok bool) {
	mp = m.mountPointFor(host, remoteRoot)
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, exists := m.entries[mp]; exists {
		if e.host == host && e.remoteRoot == remoteRoot {
			return mp, nil, false
		}
		sum := sha256.Sum256([]byte(host + "\x00" + remoteRoot))
		mp += "-" + hex.EncodeToString(sum[:3])
		if e, exists := m.entries[mp]; exists {
			if e.host == host && e.remoteRoot == remoteRoot {
				return mp, nil, false
			}
			return mp, nil, false // a third collision is not worth more machinery
		}
	}
	placeholder = &mountEntry{host: host, remoteRoot: remoteRoot} // server/conn filled on success
	m.entries[mp] = placeholder
	return mp, placeholder, true
}

func (m *mountManager) registerWatch(host, remoteRoot, mp string) {
	if m.conn == nil {
		return
	}
	_ = m.conn.SendAppMsgTo(wire.Recipient{AppID: fswatchproto.AppID}, map[string]any{
		"kind":        fswatchproto.KindRegisterMount,
		"host":        host,
		"remote_root": remoteRoot,
		"mount_point": mp,
	})
}

// reregisterAll re-sends register_mount for every live mount. The watch
// service asks for this when it (re)starts, since its registrations live only
// in its memory (fswatchproto.KindResyncMounts).
func (m *mountManager) reregisterAll() {
	m.mu.Lock()
	type live struct{ host, root, mp string }
	var all []live
	for mp, e := range m.entries {
		if e != nil && e.server != nil {
			all = append(all, live{e.host, e.remoteRoot, mp})
		}
	}
	m.mu.Unlock()
	for _, l := range all {
		m.registerWatch(l.host, l.root, l.mp)
	}
}

// unmount detaches a mount (escalating escape hatch) and releases its watch.
// shutdown tears down every live mount's ssh subprocess (and best-effort
// FUSE-unmounts) on service termination, so a router SIGTERM / conn-close
// doesn't leak the sftp ssh processes (REVIEW-RECONNECT H5). Unlike unmount()
// it does NOT forget persisted mounts — they re-establish on the next launch.
func (m *mountManager) shutdown() {
	m.mu.Lock()
	entries := m.entries
	m.entries = map[string]*mountEntry{}
	if m.stop != nil {
		select {
		case <-m.stop:
		default:
			close(m.stop) // ends the persisted-mount retry loops
		}
	}
	m.mu.Unlock()
	for mp, e := range entries {
		if e == nil {
			continue
		}
		_ = washmount.Unmount(e.server, mp) // nil server (still dialing) is handled
		if e.conn != nil {
			e.conn.closeCurrent() // kills the sftp ssh subprocess
		}
	}
}

// unmount detaches a mount. Without force it is graceful only: if processes
// hold the tree (a shell cd'd in, an editor with a file open) the kernel
// refuses, the mount STAYS mounted, and the row reports who holds it — the
// user can retry once they're done, or force. With force (or for a mount
// that never finished coming up) it runs the escalating escape hatch,
// whose kernel abort turns the holders' I/O into errors.
func (m *mountManager) unmount(mp string, force bool) {
	m.mu.Lock()
	e := m.entries[mp]
	if e != nil && e.unmounting {
		m.mu.Unlock()
		return // a second click while the first is still in fusermount
	}
	detached := false
	if e != nil && e.server != nil && !force {
		e.unmounting = true
		m.mu.Unlock()
		err := unmountGraceful(e.server)
		m.mu.Lock()
		e.unmounting = false
		switch {
		case err == nil:
			detached = true
		case washmount.IsBusy(err):
			m.mu.Unlock()
			who := describeHolders(mp)
			if who == "" {
				who = "a process this user can't see"
			}
			m.setMount(MountState{Host: e.host, RemoteRoot: e.remoteRoot, MountPoint: mp, Status: MountUp, Busy: who})
			return
		default:
			// Not busy, not clean: the backend is wedged or the mount is
			// half-gone. Fall through to the escalation.
			log.Printf("wash-remote: graceful unmount of %s failed: %v; escalating", mp, err)
		}
	}
	delete(m.entries, mp)
	f, wasFailed := m.failed[mp]
	delete(m.failed, mp)
	m.mu.Unlock()
	if e == nil {
		if wasFailed {
			// "Unmount" on an error row: dismiss it, and forget the persisted
			// entry so a restore loop (and the next launch) stops retrying.
			removeSavedMount(f.Host, f.RemoteRoot)
			m.removeMount(mp)
		}
		return
	}
	if m.conn != nil {
		_ = m.conn.SendAppMsgTo(wire.Recipient{AppID: fswatchproto.AppID}, map[string]any{
			"kind":        fswatchproto.KindUnregisterMount,
			"mount_point": mp,
		})
	}
	if !detached {
		_ = washmount.Unmount(e.server, mp) // nil server (a still-dialing placeholder) is handled
	}
	if e.conn != nil {
		e.conn.closeCurrent()
	}
	// Remove the now-empty mountpoint (and prune an emptied host dir up to
	// baseDir). This is the fs-notify wash-fm needs: it watches the parent via
	// inotify, so the rmdir fires IN_DELETE and fm drops the stale mount icon.
	// Without it the empty dir lingers and fm keeps showing a mounted volume.
	// os.Remove fails (EBUSY) on a still-mounted point, so it can't nuke live
	// data — it only succeeds once the FUSE detach is real.
	m.pruneMountDir(mp)
	removeSavedMount(e.host, e.remoteRoot) // an explicit unmount also stops re-mounting at launch
	m.removeMount(mp)
}

// pruneMountDir removes the unmounted mountpoint and any now-empty ancestor
// dirs, stopping at baseDir (never removing the ~/wash/remote root itself).
// Each rmdir is best-effort: a non-empty or busy dir just stops the walk.
func (m *mountManager) pruneMountDir(mp string) {
	dir := filepath.Clean(mp)
	base := filepath.Clean(m.baseDir)
	for dir != base && strings.HasPrefix(dir, base+string(filepath.Separator)) {
		if err := os.Remove(dir); err != nil {
			return // non-empty / busy / already gone — stop pruning
		}
		dir = filepath.Dir(dir)
	}
}

func killCmd(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

// setMount/removeMount are copy-on-write for the same reason as setHost: a
// prior Mutate's shallow snapshot may still be marshaling st.Mounts' backing
// array, so rebuild rather than overwrite an element / compact in place.
func (m *mountManager) setMount(ms MountState) {
	m.svc.Mutate(func(st *State) {
		out := make([]MountState, len(st.Mounts), len(st.Mounts)+1)
		copy(out, st.Mounts)
		replaced := false
		for i := range out {
			if out[i].MountPoint == ms.MountPoint {
				out[i] = ms
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, ms)
		}
		st.Mounts = out
	})
}

func (m *mountManager) removeMount(mp string) {
	m.svc.Mutate(func(st *State) {
		out := make([]MountState, 0, len(st.Mounts))
		for _, x := range st.Mounts {
			if x.MountPoint != mp {
				out = append(out, x)
			}
		}
		st.Mounts = out
	})
}
