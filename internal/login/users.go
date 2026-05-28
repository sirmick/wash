package login

// /etc/passwd-based user enumeration for the login form.
//
// Per the "no NSS, no fancy syscalls" direction in docs/MULTIUSER.md:
// we read /etc/passwd directly (world-readable text file) instead of
// shelling out to getent or calling NSS. This is pure-Go, dependency
// free, and covers every account the local system declares.
//
// Limitation: users that exist only in LDAP / SSSD / NIS won't appear
// in /etc/passwd. Most deployments mirror LDAP into passwd via
// nss_files / sssd's enumerate option; deployments that don't either
// (a) lose the user list — sysadmins can still type the username
// manually — or (b) pass a --passwd-path pointing somewhere
// pre-populated. v2 may add a richer backend; v1 keeps this simple.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// User is a single passwd-style entry surfaced on the login form.
type User struct {
	Name    string // login name (passwd field 1)
	UID     uint32
	GID     uint32
	Display string // first comma-delimited GECOS field
	Home    string
	Shell   string
}

// UserLister enumerates the accounts the login form should display
// and resolves uid+gid for the post-auth setuid step. Production
// wires PasswdLister; tests pass a deterministic fake.
type UserLister interface {
	List() ([]User, error)
	Lookup(name string) (User, error)
}

// PasswdLister reads /etc/passwd. Filters by MinUID/MaxUID and a
// skip-shells set so the picker doesn't surface daemons.
type PasswdLister struct {
	Path       string   // override; "" → "/etc/passwd"
	MinUID     uint32   // default 1000 — matches /etc/login.defs UID_MIN
	MaxUID     uint32   // default 60000 — excludes nobody / nfsnobody
	SkipShells []string // matched by basename; nil → defaults
}

func (l *PasswdLister) path() string {
	if l.Path != "" {
		return l.Path
	}
	return "/etc/passwd"
}

func (l *PasswdLister) minUID() uint32 {
	if l.MinUID == 0 {
		return 1000
	}
	return l.MinUID
}

func (l *PasswdLister) maxUID() uint32 {
	if l.MaxUID == 0 {
		return 60000
	}
	return l.MaxUID
}

func (l *PasswdLister) skipShellSet() map[string]bool {
	out := map[string]bool{}
	src := l.SkipShells
	if src == nil {
		src = []string{"nologin", "false", "halt", "shutdown", "sync"}
	}
	for _, s := range src {
		out[s] = true
	}
	return out
}

// List reads the passwd file and returns the filtered set.
func (l *PasswdLister) List() ([]User, error) {
	blob, err := os.ReadFile(l.path())
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", l.path(), err)
	}
	users := parsePasswd(blob, l.minUID(), l.maxUID(), l.skipShellSet())
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return users, nil
}

// Lookup returns one User by name, regardless of MinUID/MaxUID or
// shell filters. Used post-auth to resolve the uid/gid to setuid to.
// Returns ErrUserNotFound when there's no matching entry.
func (l *PasswdLister) Lookup(name string) (User, error) {
	f, err := os.Open(l.path())
	if err != nil {
		return User{}, fmt.Errorf("open %s: %w", l.path(), err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		u, ok := parsePasswdLine(sc.Text())
		if !ok {
			continue
		}
		if u.Name == name {
			return u, nil
		}
	}
	if err := sc.Err(); err != nil {
		return User{}, fmt.Errorf("scan %s: %w", l.path(), err)
	}
	return User{}, ErrUserNotFound
}

// parsePasswd filters by uid range + shell-skip. Exposed for tests
// so they don't need to materialise a temp /etc/passwd just to feed
// the parser.
func parsePasswd(blob []byte, minUID, maxUID uint32, skip map[string]bool) []User {
	var out []User
	for _, line := range strings.Split(string(blob), "\n") {
		u, ok := parsePasswdLine(line)
		if !ok {
			continue
		}
		if u.UID < minUID || u.UID > maxUID {
			continue
		}
		shellBase := u.Shell
		if i := strings.LastIndex(u.Shell, "/"); i >= 0 {
			shellBase = u.Shell[i+1:]
		}
		if skip[shellBase] {
			continue
		}
		out = append(out, u)
	}
	return out
}

// parsePasswdLine decodes one "name:x:uid:gid:gecos:home:shell" line.
// Returns (zero, false) on any structural problem so the caller can
// skip silently.
func parsePasswdLine(line string) (User, bool) {
	if line == "" {
		return User{}, false
	}
	parts := strings.SplitN(line, ":", 7)
	if len(parts) != 7 {
		return User{}, false
	}
	uid64, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil {
		return User{}, false
	}
	gid64, _ := strconv.ParseUint(parts[3], 10, 32)
	return User{
		Name:    parts[0],
		UID:     uint32(uid64),
		GID:     uint32(gid64),
		Display: displayNameFromGECOS(parts[4]),
		Home:    parts[5],
		Shell:   parts[6],
	}, true
}

// displayNameFromGECOS extracts the first comma-delimited field of a
// GECOS string, which is conventionally the user's real name.
func displayNameFromGECOS(g string) string {
	if i := strings.IndexByte(g, ','); i >= 0 {
		return strings.TrimSpace(g[:i])
	}
	return strings.TrimSpace(g)
}

// ErrUserNotFound is returned by Lookup when no such user exists.
var ErrUserNotFound = errors.New("user not found")

// Compile-time check.
var _ UserLister = (*PasswdLister)(nil)
