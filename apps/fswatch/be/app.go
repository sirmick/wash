// Package fswatchsvc is com.wash.fswatch — the shared filesystem-watch service.
//
// Today every wash app runs its own in-process inotify watcher. This service
// centralizes that: one process watches on behalf of all apps, which (a) lets
// remote-mount paths be served from the remote host's inotify and (b) collapses
// N inotify instances into one, easing the per-user instance ceiling.
//
// It is the streaming sibling of sdk.StateService: subscribers send watch/
// unwatch (carrying a path) and receive fs_event messages for changes under
// the paths they asked for. The fan-out engine is fswatch.Hub over a
// fswatch.Router, so a path under a registered remote mount is answered by the
// remote host while every other path is answered by local inotify — the
// subscriber never learns which.
//
// Wire shape — controls (cross-app from a consumer app's BE):
//
//	→ watch        {path}     subscribe to changes under path
//	→ unwatch      {path}     drop one path
//	→ unwatch_all  {}         drop every path for this subscriber (teardown)
//
// Wire shape — events pushed to a subscriber:
//
//	← {kind:"fs_event", path, op}   op ∈ created|modified|deleted
package fswatchsvc

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/fswatch"
	"github.com/sirmick/wash/internal/remotewatch"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

const version = "0.1.0"

// AppID is the reserved-DNS id of the shared watch service.
const AppID = "com.wash.fswatch"

// Wire vocabulary. Exported so consumer code uses the same strings.
const (
	KindWatch      = "watch"
	KindUnwatch    = "unwatch"
	KindUnwatchAll = "unwatch_all"
	KindEvent      = "fs_event"

	// register_mount / unregister_mount are sent by the mount supervisor
	// (com.wash.remote) when it FUSE-mounts / unmounts a remote tree, so the
	// service routes watches under that mountpoint to the remote host's inotify.
	KindRegisterMount   = "register_mount"
	KindUnregisterMount = "unregister_mount"
)

type watchReq struct {
	Path string `json:"path"`
}

type mountReq struct {
	Host       string `json:"host"`
	RemoteRoot string `json:"remote_root"`
	MountPoint string `json:"mount_point"`
}

var (
	def *sdk.AppDef

	// Package-level so onReady wires them and tests can assert. Singleton
	// service ⇒ one set per process.
	hub      *fswatch.Hub
	router   *fswatch.Router
	localMgr *fswatch.Manager // the local inotify watcher, for lifecycle
)

func init() {
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "FS Watch",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
		},
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-fswatch",
		Manifest: def.Manifest,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

func onReady(c *sdk.Conn, instanceID string, _ uint32) {
	log.Printf("wash-fswatch ready instance=%s", instanceID)
	bus := sdk.NewBus(c)

	// Local inotify is the default source; remote mounts register into the
	// Router later (register_mount, a follow-up). If inotify is unavailable the
	// service still runs — only local Subscribe calls error.
	var localSrc fswatch.Source
	if mgr, err := fswatch.New(); err != nil {
		log.Printf("wash-fswatch: local inotify unavailable: %v", err)
	} else {
		localMgr = mgr
		localSrc = fswatch.LocalSource(mgr)
	}
	router = fswatch.NewRouter(localSrc)
	hub = fswatch.NewHub(router, func(subscriberID string, ev fswatch.Event) {
		// Deliver one change to one subscriber — same mechanism StateService
		// uses to push. The router drops sends to gone instances.
		_ = c.SendAppMsgTo(wire.Recipient{InstanceID: subscriberID}, eventPayload(ev))
	})

	sdk.HandleFromVoid(bus, KindWatch, func(_ *sdk.Conn, _ string, req watchReq, from wire.Sender) error {
		if from.InstanceID == "" || req.Path == "" {
			return nil
		}
		if err := hub.Subscribe(from.InstanceID, req.Path); err != nil {
			log.Printf("wash-fswatch: watch %q for %s: %v", req.Path, from.InstanceID, err)
		}
		return nil
	})
	sdk.HandleFromVoid(bus, KindUnwatch, func(_ *sdk.Conn, _ string, req watchReq, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		hub.Unsubscribe(from.InstanceID, req.Path)
		return nil
	})
	// unwatch_all releases every watch a subscriber holds. A consumer fires it
	// on teardown (window/FE close), since there is no instance-gone signal a
	// background service can hook — without it a crashed consumer's watches
	// would leak (the cost StateService tolerates, but watches hold inotify).
	sdk.HandleFromVoid(bus, KindUnwatchAll, func(_ *sdk.Conn, _ string, _ struct{}, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		hub.RemoveSubscriber(from.InstanceID)
		return nil
	})

	sdk.HandleFromVoid(bus, KindRegisterMount, func(_ *sdk.Conn, _ string, req mountReq, _ wire.Sender) error {
		registerMount(req)
		return nil
	})
	sdk.HandleFromVoid(bus, KindUnregisterMount, func(_ *sdk.Conn, _ string, req mountReq, _ wire.Sender) error {
		unregisterMount(req.MountPoint)
		return nil
	})
}

// openWatchChannel opens the wash watch stream to a remote host — by default
// `ssh <host> wash-fswatchd`, the daemon that streams the host's inotify.
// Overridable in tests.
var openWatchChannel = func(host string) (io.ReadWriteCloser, error) {
	cmd := exec.Command("ssh",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		host, "wash-fswatchd")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &procRWC{r: stdout, w: stdin, cmd: cmd}, nil
}

type mountReg struct {
	client *remotewatch.Client
	rwc    io.ReadWriteCloser
}

var (
	mountsMu sync.Mutex
	mounts   = map[string]*mountReg{} // keyed by mountpoint
)

// registerMount opens the watch channel to host and routes watches under
// mountPoint to it. The data-side FUSE mount is the supervisor's job; this is
// watch-only.
func registerMount(req mountReq) {
	if req.MountPoint == "" || req.Host == "" {
		return
	}
	mountsMu.Lock()
	_, exists := mounts[req.MountPoint]
	mountsMu.Unlock()
	if exists {
		return
	}
	rwc, err := openWatchChannel(req.Host)
	if err != nil {
		log.Printf("wash-fswatch: open watch channel to %s: %v", req.Host, err)
		return
	}
	client := remotewatch.NewClient(rwc, remotewatch.PathMap{
		MountPoint: req.MountPoint,
		RemoteRoot: req.RemoteRoot,
	})
	router.Mount(req.MountPoint, client) // Router owns/closes the client
	mountsMu.Lock()
	mounts[req.MountPoint] = &mountReg{client: client, rwc: rwc}
	mountsMu.Unlock()
	log.Printf("wash-fswatch: registered mount %s -> %s:%s", req.MountPoint, req.Host, req.RemoteRoot)
}

func unregisterMount(mountPoint string) {
	if mountPoint == "" {
		return
	}
	mountsMu.Lock()
	reg := mounts[mountPoint]
	delete(mounts, mountPoint)
	mountsMu.Unlock()
	if reg == nil {
		return
	}
	router.Unmount(mountPoint) // closes the remotewatch.Client
	_ = reg.rwc.Close()        // tears down the ssh wash-fswatchd subprocess
}

// procRWC adapts an ssh subprocess's stdio into one io.ReadWriteCloser; Close
// kills the subprocess.
type procRWC struct {
	r   io.Reader
	w   io.WriteCloser
	cmd *exec.Cmd
}

func (p *procRWC) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *procRWC) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *procRWC) Close() error {
	p.w.Close()
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	return nil
}

func eventPayload(ev fswatch.Event) map[string]any {
	return map[string]any{
		"kind": KindEvent,
		"path": ev.Path,
		"op":   ev.Op.String(),
	}
}
