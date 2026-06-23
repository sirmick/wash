// Package fs is wash's read-side filesystem accessor. Both wash-fm
// (in-process) and wash-fs (as a singleton service over the router)
// build on it.
//
// The package is read-only by design: List, Stat, Complete. Mutations
// (rename, delete, create, write, chmod, chown) live in wash-fm and
// will move to a wash-fs equivalent if and when a second mutating
// consumer appears — per the `no-premature-service` discipline.
//
// # Sandboxing
//
// An FS instance carries a root. Confine resolves every input path
// to its absolute, cleaned form and rejects paths outside the root
// with ErrOutsideRoot. An empty root disables the sandbox.
package fs

import (
	"bufio"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Default caps. Callers pass an override into List/Complete; 0 falls
// back to these. Tuned for wash's UI use cases — beyond these sizes
// the FE has bigger problems than truncation.
const (
	DefaultMaxListEntries = 5_000
	DefaultMaxCompletions = 50
)

// ErrOutsideRoot is returned when a path resolves outside the
// configured sandbox root.
var ErrOutsideRoot = errors.New("path is outside the configured root")

// Entry is the metadata shape wash-fm puts on the wire and that
// wash-fs returns to its callers.
type Entry struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // "dir"|"file"|"symlink"|"blockdev"|"chardev"|"fifo"|"socket"|"other"
	Size        int64  `json:"size"`
	ModUnix     int64  `json:"mod_unix"`
	CreatedUnix int64  `json:"created_unix"`
	Perm        string `json:"perm"` // "rwxr-xr--" 9-char human form
	Mode        uint32 `json:"mode"` // raw permission bits
	UID         uint32 `json:"uid"`
	GID         uint32 `json:"gid"`
	Owner       string `json:"owner,omitempty"`
	Group       string `json:"group,omitempty"`
	LinkTo      string `json:"link_to,omitempty"`
	LinkErr     string `json:"link_err,omitempty"`
	Broken      bool   `json:"broken,omitempty"` // symlink whose target can't be reached

	// Display hints, computed for the *calling* process's identity
	// (uid + supplementary groups). Each is omitempty with the polarity
	// chosen so "true" is the notable/rare state — the common case is
	// absent on the wire. The FE turns these into colours/badges.
	Mount    bool `json:"mount,omitempty"`     // dir is a mount point (from /proc/self/mountinfo)
	Exec     bool `json:"exec,omitempty"`      // regular file the caller may execute
	ReadOnly bool `json:"read_only,omitempty"` // caller may not write it
	NoEnter  bool `json:"no_enter,omitempty"`  // dir the caller may not enter (no x)
	Setuid   bool `json:"setuid,omitempty"`    // setuid bit set
	Setgid   bool `json:"setgid,omitempty"`    // setgid bit set
}

// FS is a sandboxed read accessor. Construct with New(root). Methods
// are safe for concurrent use.
type FS struct {
	root string
}

// New returns an FS rooted at root. If root is non-empty it must
// already be absolute and filepath.Clean'd — the caller (router or
// wash-fm) normalizes once at startup; we don't redo it on every
// call.
func New(root string) *FS {
	return &FS{root: root}
}

// Root returns the configured sandbox root ("" means unconfined).
func (f *FS) Root() string { return f.root }

// DefaultStart picks the "where do we land on first paint" path
// for an unconfined FS. Today: $HOME when available, "/" as a
// last-ditch fallback. Sandboxed apps should use Root() instead —
// this helper is for the unsandboxed case where "/" would dump
// the user at the filesystem root instead of somewhere useful.
func DefaultStart() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "/"
}

// Confine resolves p to its absolute, cleaned form and verifies it
// sits inside f.Root when configured. Returns ErrOutsideRoot on
// escape.
func (f *FS) Confine(p string) (string, error) {
	if p == "" {
		return "", errors.New("missing path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	cleaned := filepath.Clean(abs)
	if f.root == "" {
		return cleaned, nil
	}
	if cleaned != f.root && !strings.HasPrefix(cleaned, f.root+string(filepath.Separator)) {
		return "", ErrOutsideRoot
	}
	return cleaned, nil
}

// List reads the directory at p and returns its entries, sorted
// directories-first then alphabetical. Symlinks are reported but not
// followed; the caller can re-List the target on demand.
//
// maxEntries caps the returned slice; 0 means DefaultMaxListEntries.
// When the cap is reached, truncated is true and the entries slice
// is exactly maxEntries long.
func (f *FS) List(p string, maxEntries int) (entries []Entry, abs string, truncated bool, err error) {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxListEntries
	}
	abs, err = f.Confine(p)
	if err != nil {
		return nil, "", false, err
	}
	dir, err := os.Open(abs)
	if err != nil {
		return nil, abs, false, err
	}
	defer dir.Close()
	infos, err := dir.Readdir(-1)
	if err != nil {
		return nil, abs, false, err
	}
	if len(infos) > maxEntries {
		infos = infos[:maxEntries]
		truncated = true
	}
	out := make([]Entry, 0, len(infos))
	owners := map[uint32]string{}
	groups := map[uint32]string{}
	mounts := mountPoints()
	for _, fi := range infos {
		e := entryFor(abs, fi, owners, groups)
		if e.Type == "dir" && mounts[filepath.Join(abs, fi.Name())] {
			e.Mount = true
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		di := out[i].Type == "dir"
		dj := out[j].Type == "dir"
		if di != dj {
			return di
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, abs, truncated, nil
}

// Stat returns metadata for a single path. Mirrors List's entry
// shape so callers get one type back regardless of how they probed
// the fs. Doesn't follow symlinks (lstat semantics).
func (f *FS) Stat(p string) (Entry, string, error) {
	abs, err := f.Confine(p)
	if err != nil {
		return Entry{}, "", err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return Entry{}, abs, err
	}
	owners := map[uint32]string{}
	groups := map[uint32]string{}
	parent := filepath.Dir(abs)
	e := entryFor(parent, fi, owners, groups)
	return e, abs, nil
}

// Complete returns path-string completions for `partial`. The dir
// part of partial is listed; entries whose name starts with the
// base part are returned, sorted, capped at maxMatches (0 →
// DefaultMaxCompletions). Out-of-sandbox or unreadable directories
// silently produce zero matches — autocomplete is a UX path, not a
// place to surface filesystem errors.
func (f *FS) Complete(partial string, maxMatches int) []string {
	if maxMatches <= 0 {
		maxMatches = DefaultMaxCompletions
	}
	var dir, prefix string
	switch {
	case partial == "":
		dir, prefix = "/", ""
	case strings.HasSuffix(partial, "/"):
		dir, prefix = partial, ""
	default:
		dir = filepath.Dir(partial)
		if dir == "" {
			dir = "/"
		}
		prefix = filepath.Base(partial)
	}
	abs, err := f.Confine(dir)
	if err != nil {
		return nil
	}
	d, err := os.Open(abs)
	if err != nil {
		return nil
	}
	defer d.Close()
	infos, err := d.Readdir(-1)
	if err != nil {
		return nil
	}
	matches := make([]string, 0, 16)
	for _, fi := range infos {
		name := fi.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		full := filepath.Join(abs, name)
		if fi.IsDir() {
			full += "/"
		}
		matches = append(matches, full)
		if len(matches) >= maxMatches {
			break
		}
	}
	sort.Strings(matches)
	return matches
}

// ErrCode maps an error from any FS method (read- or mutation-side)
// to a short, stable string for app-msg error replies. Callers wrap
// this into their own protocol's error envelope.
func ErrCode(err error) string {
	switch {
	case errors.Is(err, ErrOutsideRoot):
		return "outside_root"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, os.ErrPermission):
		return "denied"
	case errors.Is(err, os.ErrExist):
		return "exists"
	case errors.Is(err, ErrTooLarge):
		return "too_large"
	case errors.Is(err, ErrSamePath):
		return "same_path"
	case errors.Is(err, ErrNotEmpty):
		return "not_empty"
	case errors.Is(err, ErrNotEmptyDir):
		return "not_empty_dir"
	case errors.Is(err, ErrCrossDevice):
		return "cross_device"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	}
	return "io"
}

// entryFor builds an Entry from a FileInfo + its containing absDir.
// Shared by List (where absDir is the listed directory) and Stat
// (where absDir is the parent of the statted path). owners/groups
// are the caller's per-batch caches so repeated lookups across an
// FS-method call collapse.
func entryFor(absDir string, fi os.FileInfo, owners, groups map[uint32]string) Entry {
	full := filepath.Join(absDir, fi.Name())
	e := Entry{
		Name:        fi.Name(),
		Type:        typeOf(fi),
		Size:        fi.Size(),
		ModUnix:     fi.ModTime().Unix(),
		CreatedUnix: birthtime(full, fi.ModTime().Unix()),
		Perm:        formatPerm(fi.Mode()),
		Mode:        uint32(fi.Mode().Perm()),
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		e.UID = st.Uid
		e.GID = st.Gid
		e.Owner = lookupOwner(owners, st.Uid)
		e.Group = lookupGroup(groups, st.Gid)
	}
	if e.Type == "symlink" {
		if target, err := os.Readlink(full); err == nil {
			e.LinkTo = target
			// Readlink succeeds even for a dangling link (it only reads the
			// target string). Follow it with Stat to learn if the target is
			// actually reachable; a failure (missing target, ELOOP) means
			// the link is broken.
			if _, serr := os.Stat(full); serr != nil {
				e.Broken = true
			}
		} else {
			e.LinkErr = err.Error()
		}
	}
	// Access hints for the caller's identity. Symlinks carry the link's
	// own (meaningless) perms, so skip them — the FE colours a symlink by
	// Broken instead. setuid/setgid come from the full mode.
	if e.Type != "symlink" {
		m := fi.Mode()
		e.Setuid = m&os.ModeSetuid != 0
		e.Setgid = m&os.ModeSetgid != 0
		w, x := selfPerm(m, e.UID, e.GID)
		e.ReadOnly = !w
		switch e.Type {
		case "dir":
			e.NoEnter = !x
		case "file":
			e.Exec = x
		}
	}
	return e
}

func typeOf(fi os.FileInfo) string { return kindFromMode(fi.Mode()) }

// kindFromMode maps a FileMode to the wire `type` string. Split out from
// typeOf so it's unit-testable without real device nodes (which need root
// to mknod). Order matters: ModeCharDevice is only meaningful alongside
// ModeDevice.
func kindFromMode(m os.FileMode) string {
	switch {
	case m.IsDir():
		return "dir"
	case m&os.ModeSymlink != 0:
		return "symlink"
	case m.IsRegular():
		return "file"
	case m&os.ModeDevice != 0 && m&os.ModeCharDevice != 0:
		return "chardev"
	case m&os.ModeDevice != 0:
		return "blockdev"
	case m&os.ModeNamedPipe != 0:
		return "fifo"
	case m&os.ModeSocket != 0:
		return "socket"
	default:
		return "other"
	}
}

// self is the calling process's identity, used to compute per-entry
// access hints (exec / read-only / no-enter). Loaded once: a process's
// uid + supplementary groups don't change under us.
var (
	selfOnce   sync.Once
	selfUID    uint32
	selfIsRoot bool
	selfGroups map[uint32]bool
)

func loadSelf() {
	selfUID = uint32(os.Getuid())
	selfIsRoot = os.Geteuid() == 0
	selfGroups = map[uint32]bool{uint32(os.Getgid()): true}
	if gs, err := os.Getgroups(); err == nil {
		for _, g := range gs {
			selfGroups[uint32(g)] = true
		}
	}
}

// selfPerm returns whether the calling process may read/write/execute a
// file with the given mode/owner, applying the standard Unix triad
// resolution (owner, else group, else other) plus the root override.
func selfPerm(mode os.FileMode, uid, gid uint32) (w, x bool) {
	selfOnce.Do(loadSelf)
	return permFor(mode, selfIsRoot, selfUID == uid, selfGroups[gid])
}

// permFor is the pure triad-resolution core of selfPerm, split out so it
// can be unit-tested without the process's real identity. Returns whether
// the resolved identity may write / execute. root writes anything and may
// execute when any exec bit is set (Linux semantics).
func permFor(mode os.FileMode, isRoot, isOwner, inGroup bool) (w, x bool) {
	perm := mode.Perm()
	if isRoot {
		return true, perm&0o111 != 0
	}
	var bits os.FileMode
	switch {
	case isOwner:
		bits = (perm >> 6) & 7
	case inGroup:
		bits = (perm >> 3) & 7
	default:
		bits = perm & 7
	}
	return bits&0o2 != 0, bits&0o1 != 0
}

// mountPoints returns the set of absolute mount-point paths from
// /proc/self/mountinfo. Used by List to flag directories that are mount
// boundaries (catches bind mounts a parent/child st_dev compare would
// miss). Returns nil (an empty set lookups against safely) on any read
// error — mount flagging is a hint, never load-bearing.
func mountPoints() map[string]bool {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	defer f.Close()
	set := make(map[string]bool, 64)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		// mountinfo field 5 (1-based) is the mount point; whitespace in
		// paths is octal-escaped (\040 etc.), so Fields is safe here.
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		set[unescapeMountPath(fields[4])] = true
	}
	return set
}

// unescapeMountPath decodes the octal escapes mountinfo uses for space
// (\040), tab (\011), newline (\012) and backslash (\134) in paths.
func unescapeMountPath(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseInt(s[i+1:i+4], 8, 16); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// formatPerm renders the low 9 bits in conventional rwxrwxrwx form.
func formatPerm(m os.FileMode) string {
	const set = "rwxrwxrwx"
	out := []byte("---------")
	for i := 0; i < 9; i++ {
		if m&(1<<uint(8-i)) != 0 {
			out[i] = set[i]
		}
	}
	return string(out)
}

// birthtime returns the file's btime (Unix seconds), or fallback
// when the kernel/filesystem doesn't supply STATX_BTIME (pre-4.11
// kernel, older filesystems). Caller always gets a sortable value.
func birthtime(absPath string, fallback int64) int64 {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, absPath, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &stx); err != nil {
		return fallback
	}
	if stx.Mask&unix.STATX_BTIME == 0 {
		return fallback
	}
	return stx.Btime.Sec
}

// lookupOwner resolves a uid to a username, caching the result.
// Empty string in the cache marks "lookup failed, don't retry."
func lookupOwner(cache map[uint32]string, uid uint32) string {
	if v, ok := cache[uid]; ok {
		return v
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		cache[uid] = ""
		return ""
	}
	cache[uid] = u.Username
	return u.Username
}

func lookupGroup(cache map[uint32]string, gid uint32) string {
	if v, ok := cache[gid]; ok {
		return v
	}
	g, err := user.LookupGroupId(strconv.FormatUint(uint64(gid), 10))
	if err != nil {
		cache[gid] = ""
		return ""
	}
	cache[gid] = g.Name
	return g.Name
}
