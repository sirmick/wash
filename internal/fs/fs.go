// Package fs is wash's read-side filesystem accessor. Both wash-fm
// (in-process) and wash-fs (as a singleton service over the router)
// build on it.
//
// The package is read-only by design: List, Stat, Complete. Mutations
// (rename, delete, create, write, chmod, chown) live in wash-fm and
// will move to a wash-fs equivalent if and when a second mutating
// consumer appears — per the `no-premature-service` discipline.
//
// Sandboxing
//
// An FS instance carries a root. Confine resolves every input path
// to its absolute, cleaned form and rejects paths outside the root
// with ErrOutsideRoot. An empty root disables the sandbox (matches
// the historical wash-fm behavior when WASH_FM_ROOT was unset).
package fs

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
// wash-fs returns to its callers. JSON + CBOR tags so either
// transport can re-emit it as-is.
type Entry struct {
	Name        string `json:"name" cbor:"name"`
	Type        string `json:"type" cbor:"type"` // "dir" | "file" | "symlink" | "other"
	Size        int64  `json:"size" cbor:"size"`
	ModUnix     int64  `json:"mod_unix" cbor:"mod_unix"`
	CreatedUnix int64  `json:"created_unix" cbor:"created_unix"`
	Perm        string `json:"perm" cbor:"perm"` // "rwxr-xr--" 9-char human form
	Mode        uint32 `json:"mode" cbor:"mode"` // raw permission bits
	UID         uint32 `json:"uid" cbor:"uid"`
	GID         uint32 `json:"gid" cbor:"gid"`
	Owner       string `json:"owner,omitempty" cbor:"owner,omitempty"`
	Group       string `json:"group,omitempty" cbor:"group,omitempty"`
	LinkTo      string `json:"link_to,omitempty" cbor:"link_to,omitempty"`
	LinkErr     string `json:"link_err,omitempty" cbor:"link_err,omitempty"`
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
	for _, fi := range infos {
		e := entryFor(abs, fi, owners, groups)
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

// ErrCode maps a List/Stat/Complete error to a short, stable string
// suitable for inclusion in app-msg error replies. Callers wrap this
// into their own protocol's error envelope.
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
		} else {
			e.LinkErr = err.Error()
		}
	}
	return e
}

func typeOf(fi os.FileInfo) string {
	m := fi.Mode()
	switch {
	case m.IsDir():
		return "dir"
	case m&os.ModeSymlink != 0:
		return "symlink"
	case m.IsRegular():
		return "file"
	default:
		return "other"
	}
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
