package login

// Unix authentication by running `su -c true <user>` over a PTY.
//
// Per "rely on login" direction in docs/MULTIUSER.md: instead of
// reimplementing crypt or shelling out to PAM-internal helpers, we
// just ask the system to do an actual login. `su -c true <user>`:
//
//   - Goes through the full PAM auth + account stack (LDAP / SSSD /
//     local — whatever the distro is configured for).
//   - Reads the password from /dev/tty, so we hand it a PTY.
//   - On success, setuid's to the target user and exec's `true`,
//     which exits 0 immediately. No shell, no .profile, no motd.
//   - Exit code is the answer.
//
// uid/gid for the post-auth fork-setuid-exec come from a UserLister
// (PasswdLister.Lookup in production) — we already verified the
// password is right; the lookup just needs the kernel-truthful uid.

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
)

// DefaultSuPaths is the search order for the su binary. Every
// non-trivial Linux distro has it at one of these.
var DefaultSuPaths = []string{
	"/bin/su",
	"/usr/bin/su",
}

// Default timeout for the whole su round-trip. Failed auth gets a
// pam_faildelay-style sleep on most distros (~2s); 10s is generous
// enough to swallow that without timing out a legitimate user.
const defaultAuthTimeout = 10 * time.Second

type suAuth struct {
	suPath  string
	lookup  func(name string) (User, error)
	timeout time.Duration
}

// NewSuAuth returns a UNIX authenticator. suPath is the path to su
// ("" → search DefaultSuPaths); lookup resolves uid/gid post-auth
// (PasswdLister.Lookup in production).
func NewSuAuth(suPath string, lookup func(name string) (User, error)) (Authenticator, error) {
	if suPath == "" {
		suPath = findSu()
	}
	if suPath == "" {
		return nil, fmt.Errorf("su binary not found in any of %v; pass --su-path", DefaultSuPaths)
	}
	if _, err := exec.LookPath(suPath); err != nil {
		return nil, fmt.Errorf("su binary %q not executable: %w", suPath, err)
	}
	if lookup == nil {
		return nil, fmt.Errorf("NewSuAuth: lookup func is required")
	}
	return &suAuth{
		suPath:  suPath,
		lookup:  lookup,
		timeout: defaultAuthTimeout,
	}, nil
}

func findSu() string {
	for _, p := range DefaultSuPaths {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return ""
}

// Authenticate spawns `su -c true <user>` attached to a PTY, writes
// the password when su prompts for it, and uses the exit code as the
// answer. Wrong-password and unknown-user both surface as
// ErrInvalidCredentials so we don't leak existence.
func (a *suAuth) Authenticate(user, password string) (Identity, error) {
	if user == "" || strings.ContainsAny(user, "\x00\n\r:") {
		return Identity{}, ErrInvalidCredentials
	}
	// -c true: run /bin/true as the target user, exit immediately
	//          on success. No shell, no .profile, no motd.
	cmd := exec.Command(a.suPath, "-c", "true", user)
	// Detach from any inherited controlling tty so su reads from
	// the PTY we're about to give it. Setctty applies the slave
	// side as the controlling tty for the child.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}

	ptyMaster, err := creackpty.Start(cmd)
	if err != nil {
		return Identity{}, fmt.Errorf("su pty start: %w", err)
	}
	defer ptyMaster.Close()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	// Run the prompt/password dance in a goroutine so a misbehaving
	// su (hung waiting for input that never arrives) is bounded by
	// the same timeout as the wait.
	var wg sync.WaitGroup
	wg.Add(1)
	driveErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		driveErr <- driveSuPty(ptyMaster, password)
	}()

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case err := <-waitErr:
		<-driveErr // make sure the driver goroutine has finished too
		if err == nil {
			return a.resolveAfterAuth(user)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return Identity{}, ErrInvalidCredentials
		}
		return Identity{}, fmt.Errorf("su wait: %w", err)
	case <-time.After(a.timeout):
		// Wrap rather than return ErrInvalidCredentials bare: a hung su
		// (PAM misconfig, missing binary) must be distinguishable from a
		// wrong password in the auth-rejection log line. errors.Is still
		// matches, so callers treat it as a credentials failure.
		return Identity{}, fmt.Errorf("%w (su timed out after %s)", ErrInvalidCredentials, a.timeout)
	}
}

// driveSuPty waits for a "Password:" prompt from su and writes the
// password followed by a newline. Tolerates the various wordings
// distros use ("Password:", "[sudo] password for X:", etc.) by
// matching just the trailing colon-and-whitespace.
func driveSuPty(master io.ReadWriter, password string) error {
	// Bounded prompt-read: PAM prompts are tiny, ~64 bytes max.
	// We read until we see ':' then write the password.
	const promptCap = 256
	buf := make([]byte, 0, promptCap)
	one := make([]byte, 1)
	for {
		n, err := master.Read(one)
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		buf = append(buf, one[0])
		if one[0] == ':' {
			break
		}
		if len(buf) >= promptCap {
			break
		}
	}
	if _, err := io.WriteString(master, password+"\n"); err != nil {
		return err
	}
	// Drain whatever su prints next (newline echo, PAM faildelay
	// output, etc.) until EOF so cmd.Wait can complete cleanly.
	_, _ = io.Copy(io.Discard, master)
	return nil
}

// resolveAfterAuth looks up the user's kernel-truthful uid/gid via
// the configured lookup func. Auth has already succeeded; we just
// need the numbers to setuid to.
func (a *suAuth) resolveAfterAuth(user string) (Identity, error) {
	u, err := a.lookup(user)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return Identity{}, ErrInvalidCredentials
		}
		return Identity{}, fmt.Errorf("post-auth lookup %q: %w", user, err)
	}
	return Identity{
		UID:   u.UID,
		GID:   u.GID,
		Name:  u.Name,
		Shell: u.Shell,
	}, nil
}
