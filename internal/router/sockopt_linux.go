package router

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// setReuseAddr sets SO_REUSEADDR on the listening socket before bind, so a
// freshly-started router can rebind a port whose previous owner just died
// without waiting out the old connections' TIME_WAIT. Used as the
// net.ListenConfig.Control hook for the HTTP listener (http.go). Unlike the
// mDNS reusePort, we do NOT set SO_REUSEPORT — we want fast rebind, not two
// routers load-balancing one port.
func setReuseAddr(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return serr
}
