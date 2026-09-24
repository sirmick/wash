// Package unixsock opens the private unix sockets apps publish through the
// router's ingress proxy (Radio's stream proxy, Washamp's and Music's file
// servers).
//
// They used to live at $TMPDIR/wash-<app>-<instanceID>.sock. Instance ids
// are small per-router counters ("i-6"), so two routers on one machine —
// two sessions of one user, a local desktop beside a remote one, or the
// e2e suite's parallel workers — named the same path: each start removed
// the other's socket and bound its own, and every router's ingress then
// reached whichever app started last (another session's Radio streaming
// into yours). In a shared /tmp a second USER's app could not remove the
// first user's socket at all, and failed to start.
//
// A socket in its own private temp directory has neither problem.
package unixsock

import (
	"net"
	"os"
	"path/filepath"
)

// Listen binds a unix socket in a fresh private directory named for
// prefix (e.g. "wash-radio-"). close stops the listener and removes the
// directory; it is safe to call more than once.
func Listen(prefix string) (ln net.Listener, path string, close func(), err error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return nil, "", nil, err
	}
	path = filepath.Join(dir, "http.sock")
	ln, err = net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", nil, err
	}
	close = func() {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
	}
	return ln, path, close, nil
}
