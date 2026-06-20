package remote

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkg/sftp"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/washmount"
	"github.com/sirmick/wash/internal/wire"
)

// fswatchAppID is the shared watch service this supervisor tells about each
// mount, so changes on the remote host stream to local watchers. Duplicated
// rather than imported — the contract is the app-id.
const fswatchAppID = "com.wash.fswatch"

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
}

// mountCtlReq / unmountCtlReq are the FE controls (relayed via wash-connect).
type mountCtlReq struct {
	Host       string `json:"host"`
	RemoteRoot string `json:"remote_root"`
}

type unmountCtlReq struct {
	MountPoint string `json:"mount_point"`
}

// mountManager owns the FUSE data mounts. It mounts the remote tree over SFTP
// and tells com.wash.fswatch to stream that mountpoint's changes; the watch
// channel is the service's to own (one per host, shared across watchers).
type mountManager struct {
	svc     *sdk.StateService[State]
	conn    *sdk.Conn
	sshPath string
	baseDir string

	mu      sync.Mutex
	entries map[string]*mountEntry // keyed by mountpoint
}

type mountEntry struct {
	server *fuse.Server
	cmd    *exec.Cmd
	client *sftp.Client
}

func newMountManager(svc *sdk.StateService[State], conn *sdk.Conn) *mountManager {
	return &mountManager{
		svc:     svc,
		conn:    conn,
		sshPath: "ssh",
		baseDir: defaultMountBase(),
		entries: map[string]*mountEntry{},
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
			host, "-s", "sftp")
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

	mountFUSE = func(client *sftp.Client, opts washmount.Options) (*fuse.Server, error) {
		return washmount.Mount(client, opts)
	}
)

// mount brings up a FUSE mount of host:remoteRoot and registers its watch with
// com.wash.fswatch. Blocks on the ssh handshake + mount; run it in a goroutine.
func (m *mountManager) mount(host, remoteRoot string) {
	if host == "" {
		return
	}
	if remoteRoot == "" {
		remoteRoot = "/"
	}
	mp := m.mountPointFor(host, remoteRoot)

	m.mu.Lock()
	_, exists := m.entries[mp]
	m.mu.Unlock()
	if exists {
		return
	}

	m.setMount(MountState{Host: host, RemoteRoot: remoteRoot, MountPoint: mp, Status: MountStarting})

	fail := func(stage string, err error) {
		m.setMount(MountState{Host: host, RemoteRoot: remoteRoot, MountPoint: mp, Status: MountDown, Error: stage + ": " + err.Error()})
	}

	if err := os.MkdirAll(mp, 0o755); err != nil {
		fail("mkdir", err)
		return
	}
	client, cmd, err := dialSFTP(m.sshPath, host)
	if err != nil {
		fail("ssh sftp", err)
		return
	}
	server, err := mountFUSE(client, washmount.Options{MountPoint: mp, RemoteRoot: remoteRoot})
	if err != nil {
		client.Close()
		killCmd(cmd)
		fail("mount", err)
		return
	}
	m.mu.Lock()
	m.entries[mp] = &mountEntry{server: server, cmd: cmd, client: client}
	m.mu.Unlock()

	// Ask the shared watch service to stream this mount's changes (it opens its
	// own ssh wash-fswatchd to the host).
	if m.conn != nil {
		_ = m.conn.SendAppMsgTo(wire.Recipient{AppID: fswatchAppID}, map[string]any{
			"kind":        "register_mount",
			"host":        host,
			"remote_root": remoteRoot,
			"mount_point": mp,
		})
	}
	m.setMount(MountState{Host: host, RemoteRoot: remoteRoot, MountPoint: mp, Status: MountUp})
}

// unmount detaches a mount (escalating escape hatch) and releases its watch.
func (m *mountManager) unmount(mp string) {
	m.mu.Lock()
	e := m.entries[mp]
	delete(m.entries, mp)
	m.mu.Unlock()
	if e == nil {
		return
	}
	if m.conn != nil {
		_ = m.conn.SendAppMsgTo(wire.Recipient{AppID: fswatchAppID}, map[string]any{
			"kind":        "unregister_mount",
			"mount_point": mp,
		})
	}
	_ = washmount.Unmount(e.server, mp)
	if e.client != nil {
		e.client.Close()
	}
	killCmd(e.cmd)
	m.removeMount(mp)
}

func killCmd(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

func (m *mountManager) setMount(ms MountState) {
	m.svc.Mutate(func(st *State) {
		for i := range st.Mounts {
			if st.Mounts[i].MountPoint == ms.MountPoint {
				st.Mounts[i] = ms
				return
			}
		}
		st.Mounts = append(st.Mounts, ms)
	})
}

func (m *mountManager) removeMount(mp string) {
	m.svc.Mutate(func(st *State) {
		out := st.Mounts[:0]
		for _, x := range st.Mounts {
			if x.MountPoint != mp {
				out = append(out, x)
			}
		}
		st.Mounts = out
	})
}
