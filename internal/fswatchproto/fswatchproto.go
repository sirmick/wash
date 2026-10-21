// Package fswatchproto is the wire vocabulary of the shared filesystem-watch
// service (com.wash.fswatch). It is a dependency-free leaf so every layer can
// import it without pulling in the fsnotify-backed internal/fswatch engine:
// the service binary (apps/fswatch/be), the consumer SDK (internal/sdk's
// WatchClient), and the mount supervisor (apps/remote/be) all share these
// exact strings instead of re-declaring — and drifting from — them.
package fswatchproto

// AppID is the reserved-DNS id of the shared watch service.
const AppID = "com.wash.fswatch"

// RemoteAppID is the mount supervisor (com.wash.remote), the service's
// register_mount sender and the recipient of its resync request.
const RemoteAppID = "com.wash.remote"

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

	// resync_mounts is sent BY the service TO the supervisor when the
	// service starts: its registrations live only in its memory, so after a
	// restart (crash, Settings restart) every mounted tree would silently
	// stop updating. The supervisor answers with a register_mount per live
	// mount (idempotent on the service side).
	KindResyncMounts = "resync_mounts"
)
