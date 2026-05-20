package router

import (
	"os"
	"syscall"
)

// stopSignal returns the polite-shutdown signal for child apps.
// SIGTERM lets the SDK run its shutdown handler; if the app doesn't
// exit within the grace period, the close-handshake force-kill (Kill())
// is the harder stop.
func stopSignal() os.Signal { return syscall.SIGTERM }
