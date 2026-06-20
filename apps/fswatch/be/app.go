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
	"log"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/fswatch"
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
)

type watchReq struct {
	Path string `json:"path"`
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
}

func eventPayload(ev fswatch.Event) map[string]any {
	return map[string]any{
		"kind": KindEvent,
		"path": ev.Path,
		"op":   ev.Op.String(),
	}
}
