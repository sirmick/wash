package login

// /etc/group lookup for the "wash" group's gid.
//
// Per "no NSS, no fancy syscalls" — pure file read, no getent, no
// libnss. wash-login uses this gid to (a) chown its socket-dir tree
// to group wash and (b) confirm at startup that the wash group
// exists for production deployments.

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
)

// WashGroupName is the standard group wash-login expects for socket
// access. Configurable in case a deployment renames it.
const WashGroupName = "wash"

// ErrGroupNotFound is returned when LookupGroupGID can't find a
// matching /etc/group entry.
var ErrGroupNotFound = errors.New("group not found")

// LookupGroupGID parses /etc/group for the named group and returns
// its gid. Pure file read; works regardless of NSS configuration.
//
// Returns ErrGroupNotFound when there's no matching entry, which
// callers treat as "this is a single-user / dev install — fall back
// to owner-only modes."
func LookupGroupGID(name string) (uint32, error) {
	return lookupGroupGIDIn("/etc/group", name)
}

// lookupGroupGIDIn is the path-parameterised version of LookupGroupGID
// — tests pass a temp file.
func lookupGroupGIDIn(path, name string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		// name:passwd:gid:members
		parts := strings.SplitN(line, ":", 4)
		if len(parts) < 3 || parts[0] != name {
			continue
		}
		gid, err := strconv.ParseUint(parts[2], 10, 32)
		if err != nil {
			return 0, err
		}
		return uint32(gid), nil
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, ErrGroupNotFound
}
