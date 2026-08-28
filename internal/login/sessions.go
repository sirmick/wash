package login

// /proc-based session registry for wash-login.
//
// Per docs/MULTIUSER.md: the kernel process table is the source of
// truth for "which sessions exist." wash-login walks /proc, filters
// for wash-router processes owned by a given uid, and parses each
// one's argv to extract sessid and human-readable name. There is no
// state file, no PID file, no internal cache — every page render
// re-reads /proc. This is what makes wash-login stateless across
// restarts: a fresh wash-login finds the existing routers and
// resumes serving them without any handoff dance.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Session is one running wash-router for a given uid.
type Session struct {
	Pid    int    // /proc/<pid>/ entry
	UID    uint32 // kernel-reported owning uid
	SessID string // parsed out of --listen-unix path
	Name   string // parsed out of --name
	Sock   string // ctl socket path (the --listen-unix value)
	Exe    string // resolved /proc/<pid>/exe, for logging
}

// SessionRegistry is the interface the server uses to ask about
// sessions. /proc is the production impl; tests can substitute a
// fake to avoid spawning real processes.
type SessionRegistry interface {
	List(uid uint32) ([]Session, error)
}

// ProcRegistry walks a /proc filesystem (default /proc) to enumerate
// wash-router processes. Construct with NewProcRegistry; pass a
// non-empty Root in tests to point at a fake /proc tree.
type ProcRegistry struct {
	// Root is the proc filesystem root. Empty means "/proc".
	Root string
	// RouterArgv0 is the basename to match in argv[0]. Empty means
	// "wash-router".
	RouterArgv0 string
	// SockRoot, when non-empty, restricts List to routers whose ctl
	// socket lives under it — the run-root that spawned them. Empty
	// means "every wash-router the uid owns, wherever it listens",
	// which is what production wash-login wants: it adopts the
	// session it finds, and there is only ever one run root on a
	// real host.
	//
	// Tests want the opposite. /proc is host-global while a test's
	// Spawner.RunRoot is a t.TempDir(), so an unscoped registry on a
	// machine that is *running* wash finds the developer's real
	// session and attaches to it instead of spawning its own. Set
	// this to the same value as Spawner.RunRoot to keep a test
	// looking only at the routers it started.
	SockRoot string
}

// NewProcRegistry returns a ProcRegistry rooted at /proc with the
// default argv[0] match. Tests should construct one directly with
// Root pointing at a tmpdir.
func NewProcRegistry() *ProcRegistry {
	return &ProcRegistry{}
}

func (r *ProcRegistry) root() string {
	if r.Root != "" {
		return r.Root
	}
	return "/proc"
}

func (r *ProcRegistry) routerArgv0() string {
	if r.RouterArgv0 != "" {
		return r.RouterArgv0
	}
	return "wash-router"
}

// underSockRoot reports whether sock lives under SockRoot. Compares
// on path boundaries rather than raw prefix so a run root of
// /run/wash doesn't swallow /run/wash2.
func (r *ProcRegistry) underSockRoot(sock string) bool {
	if r.SockRoot == "" {
		return true
	}
	root := strings.TrimRight(r.SockRoot, string(filepath.Separator))
	return strings.HasPrefix(sock, root+string(filepath.Separator))
}

// List scans the configured /proc and returns every running
// wash-router owned by uid that we can decode. Processes we can't
// read (transient ones that died between readdir and open, hidepid
// restrictions, parse failures) are silently skipped — the source of
// truth is the kernel's process table, not any one snapshot of it.
func (r *ProcRegistry) List(uid uint32) ([]Session, error) {
	root := r.root()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var out []Session
	for _, e := range entries {
		pid, ok := atoiPid(e.Name())
		if !ok {
			continue
		}
		s, err := r.parseProcEntry(pid, uid)
		if err != nil {
			// Transient / unreadable processes are normal; don't
			// surface as errors. Real config problems (e.g. /proc
			// not mounted) surface above.
			continue
		}
		if s == nil {
			continue
		}
		out = append(out, *s)
	}
	return out, nil
}

func (r *ProcRegistry) parseProcEntry(pid int, wantUID uint32) (*Session, error) {
	root := r.root()
	statusPath := filepath.Join(root, strconv.Itoa(pid), "status")
	statusBytes, err := os.ReadFile(statusPath)
	if err != nil {
		return nil, err
	}
	uid, state, ok := parseStatus(statusBytes)
	if !ok {
		return nil, errors.New("status missing Uid: or State:")
	}
	if uid != wantUID {
		return nil, nil
	}
	if state == 'Z' || state == 'T' {
		return nil, nil
	}
	cmdlineBytes, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	argv := parseCmdline(cmdlineBytes)
	if len(argv) == 0 {
		return nil, nil
	}
	if filepath.Base(argv[0]) != r.routerArgv0() {
		return nil, nil
	}
	sock, name, ok := parseRouterArgv(argv)
	if !ok {
		return nil, nil
	}
	if !r.underSockRoot(sock) {
		return nil, nil
	}
	exe, _ := os.Readlink(filepath.Join(root, strconv.Itoa(pid), "exe"))
	sessid, ok := sessIDFromSock(sock)
	if !ok {
		return nil, nil
	}
	return &Session{
		Pid:    pid,
		UID:    uid,
		SessID: sessid,
		Name:   name,
		Sock:   sock,
		Exe:    exe,
	}, nil
}

// atoiPid returns (pid, true) if name is an unsigned decimal
// representing a process id. Anything else (".", "self", "thread-self",
// non-numeric) returns (0, false).
func atoiPid(name string) (int, bool) {
	if name == "" {
		return 0, false
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return 0, false
		}
	}
	pid, err := strconv.Atoi(name)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// parseStatus extracts the real Uid and process State from a
// /proc/<pid>/status blob. Uid line is tab-separated: real / eff /
// saved / fs; we take the first (real).
func parseStatus(b []byte) (uid uint32, state byte, ok bool) {
	gotUid := false
	gotState := false
	for _, line := range bytes.Split(b, []byte("\n")) {
		switch {
		case bytes.HasPrefix(line, []byte("Uid:")):
			fields := bytes.Fields(line[len("Uid:"):])
			if len(fields) == 0 {
				continue
			}
			n, err := strconv.ParseUint(string(fields[0]), 10, 32)
			if err != nil {
				continue
			}
			uid = uint32(n)
			gotUid = true
		case bytes.HasPrefix(line, []byte("State:")):
			fields := bytes.Fields(line[len("State:"):])
			if len(fields) == 0 || len(fields[0]) == 0 {
				continue
			}
			state = fields[0][0]
			gotState = true
		}
		if gotUid && gotState {
			break
		}
	}
	return uid, state, gotUid && gotState
}

// parseCmdline splits a /proc/<pid>/cmdline blob (NUL-separated argv,
// possibly trailing NUL) into a string slice.
func parseCmdline(b []byte) []string {
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil
	}
	parts := bytes.Split(b, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, string(p))
	}
	return out
}

// parseRouterArgv walks an argv looking for --listen-unix and --name
// flags. Supports both --flag value and --flag=value spellings.
// Returns the listen-unix path, the name, and whether the listen
// flag was found (name may be empty if absent — that's OK).
func parseRouterArgv(argv []string) (sock, name string, ok bool) {
	for i := 1; i < len(argv); i++ {
		switch {
		case argv[i] == "--listen-unix":
			if i+1 < len(argv) {
				sock = argv[i+1]
				i++
			}
		case strings.HasPrefix(argv[i], "--listen-unix="):
			sock = strings.TrimPrefix(argv[i], "--listen-unix=")
		case argv[i] == "--name":
			if i+1 < len(argv) {
				name = argv[i+1]
				i++
			}
		case strings.HasPrefix(argv[i], "--name="):
			name = strings.TrimPrefix(argv[i], "--name=")
		}
	}
	return sock, name, sock != ""
}

// sessIDFromSock extracts the sessid from a ctl socket path. The
// canonical shape is /run/wash/<uid>/sessions/<sessid>.sock. Returns
// (sessid, true) when the path matches; (_, false) otherwise.
//
// A non-canonical path (e.g. a test /tmp socket) is still a valid
// session but its sessid is the file basename minus ".sock". This
// keeps the registry useful in tests + non-standard deployments
// without inventing a parallel naming convention.
func sessIDFromSock(sock string) (string, bool) {
	base := filepath.Base(sock)
	id := strings.TrimSuffix(base, ".sock")
	if id == "" || id == base {
		// No .sock extension — refuse rather than guess.
		return "", false
	}
	return id, true
}

// SessionsDir returns the canonical sessions directory for a uid.
// Production: /run/wash/<uid>/sessions. Tests/dev override the
// /run/wash root via WASH_RUN_ROOT env (interpreted by Spawn).
func SessionsDir(runRoot string, uid uint32) string {
	if runRoot == "" {
		runRoot = "/run/wash"
	}
	return filepath.Join(runRoot, strconv.FormatUint(uint64(uid), 10), "sessions")
}

// SockPath returns the canonical ctl socket path for (uid, sessid).
func SockPath(runRoot string, uid uint32, sessid string) string {
	return filepath.Join(SessionsDir(runRoot, uid), sessid+".sock")
}

// Compile-time assertion that ProcRegistry satisfies SessionRegistry.
var _ SessionRegistry = (*ProcRegistry)(nil)

// Keep the io/fs import referenced.
var _ = fs.ErrNotExist
