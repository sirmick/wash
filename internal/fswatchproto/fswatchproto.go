// Package fswatchproto is the wire vocabulary of the shared filesystem-watch
// service (com.wash.fswatch). It is a dependency-free leaf so every layer can
// import it without pulling in the fsnotify-backed internal/fswatch engine:
// the service binary (apps/fswatch/be), the consumer SDK (internal/sdk's
// WatchClient), and the mount supervisor (apps/remote/be) all share these
// exact strings instead of re-declaring — and drifting from — them.
package fswatchproto

// AppID is the reserved-DNS id of the shared watch service.
const AppID = "com.wash.fswatch"

// Wire message kinds spoken to/from the service.
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
