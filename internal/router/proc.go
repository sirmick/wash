package router

import (
	"fmt"
	"os"
	"strings"
)

// procInfo is what we read from /proc/<pid>/* about a wash-sudo
// caller — used to populate the wash-priv approval row so the user
// can see which terminal made the request before approving.
//
// Fields are best-effort; any unavailable field stays empty rather
// than failing the whole lookup. The pid is router-attested via
// SO_PEERCRED, so the rest can be displayed as derived-from-pid
// without an extra trust check.
type procInfo struct {
	PID  int32  // SO_PEERCRED — unforgeable
	UID  uint32 // SO_PEERCRED — unforgeable
	Comm string // /proc/<pid>/comm
	TTY  string // resolved /proc/<pid>/fd/0 if it's a tty
}

func readProcInfo(pid int32) procInfo {
	out := procInfo{PID: pid}
	if pid <= 0 {
		return out
	}
	if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		out.Comm = strings.TrimSpace(string(comm))
	}
	if tty, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/0", pid)); err == nil {
		// /dev/pts/N is the normal terminal case; /dev/null or
		// /memfd:foo etc. mean "not a tty" — we still show it but
		// the FE can render plainly.
		out.TTY = tty
	}
	return out
}
