package login

// Authenticator abstraction for wash-login's credential checks.
//
// v1 ships two backends:
//   - testAuth: in-process compare against a user/password pair
//     supplied via --auth-test; for CI and dev.
//   - (TODO) shadowAuth: /etc/shadow + sha512crypt against the
//     system user database. Wash-system uid must be in group
//     `shadow`. Lands in a follow-up commit.
//
// Sysadmins can plug in PAM / LDAP / SSO via --auth-helper (v2),
// which execs an external helper that takes credentials on stdin
// and reports the resolved uid on stdout.

import (
	"crypto/subtle"
	"errors"
	"fmt"
)

// Identity is the result of a successful authentication. It carries
// the kernel-truthful uid/gid for the resolved Unix account so
// wash-login can fork-exec wash-router as that user without
// re-resolving.
type Identity struct {
	UID   uint32
	GID   uint32
	Name  string
	Shell string
}

// Authenticator validates a (user, password) pair. On success it
// returns the Identity to spawn under. Failures return a generic
// error; the message is logged on the server but the browser only
// ever sees a generic "invalid credentials" response — never leak
// distinguishing detail about why a credential was rejected.
type Authenticator interface {
	Authenticate(user, password string) (Identity, error)
}

// ErrInvalidCredentials is the sentinel returned for any auth failure
// the user should see — wrong username, wrong password, disabled
// account, etc. Internal errors (config problems, IO failures) are
// wrapped separately so logs distinguish them.
var ErrInvalidCredentials = errors.New("invalid credentials")

// testAuth is an Authenticator that compares against a single hard-
// coded (user, password) pair. Used by --auth-test for CI and dev.
// Never use this in production.
type testAuth struct {
	user     string
	password string
	uid      uint32
	gid      uint32
	shell    string
}

// NewTestAuth returns a testAuth populated from a "user:password" spec.
// Optionally accepts a uid (default: current process uid) and gid
// (default: current process gid). Used by --auth-test.
func NewTestAuth(spec string, uid, gid uint32, shell string) (Authenticator, error) {
	for i := 0; i < len(spec); i++ {
		if spec[i] == ':' {
			user := spec[:i]
			pass := spec[i+1:]
			if user == "" || pass == "" {
				return nil, fmt.Errorf("--auth-test: empty user or password in %q", spec)
			}
			return &testAuth{user: user, password: pass, uid: uid, gid: gid, shell: shell}, nil
		}
	}
	return nil, fmt.Errorf("--auth-test: expected user:password, got %q", spec)
}

func (a *testAuth) Authenticate(user, password string) (Identity, error) {
	// Constant-time compares so a user-enumeration timing attack
	// can't tell "wrong user" from "wrong password."
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.user)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1
	if !userOK || !passOK {
		return Identity{}, ErrInvalidCredentials
	}
	return Identity{UID: a.uid, GID: a.gid, Name: a.user, Shell: a.shell}, nil
}
