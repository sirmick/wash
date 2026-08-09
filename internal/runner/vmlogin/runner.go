// Package vmlogin is the wash-vmlogin CLI's compiled-in form: the in-guest
// login front that both wash VMs (browser RISC-V + wemu QEMU) run in place of
// the old "su wash -c wash-router" launcher loop. It owns the wash-wire channel
// device, authenticates one browser, then forks wash-router as the resolved
// user with the channel fd handed over (suid/fork handoff). See wash-vm/UNIFY.md.
//
//	wash-vmlogin --transport=fd:3 --auth-test root:pw        # dev/CI
//	wash-vmlogin --transport=virtio-console:/dev/vport0p1    # prod (su/passwd)
//
// The login loop + handshake live in wash-vm/guest; this package is the thin
// shell: flag parsing, channel open, Authenticator construction, and the real
// SpawnRouter (syscall.ForkExec with the device at child fd 3 + setuid).
package vmlogin

import (
	"context"
	"flag"
	"fmt"
	"github.com/sirmick/wash/internal/version"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/sirmick/wash/internal/login"
	"github.com/sirmick/wash/pkg/wire"
	"github.com/sirmick/wash/wash-vm/guest"
)

// Run drives wash-vmlogin with the given argv (excluding the program name).
func Run(args []string) int {
	fs := flag.NewFlagSet("wash-vmlogin", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	transport := fs.String("transport", "fd:3", `channel device: "fd:N" (inherited, pre-opened blocking by the launcher), "virtio-console:/dev/vportXpY", or "serial:/dev/ttySN".`)
	routerBinary := fs.String("router-binary", "", "path to wash-router. Empty: look beside this binary, then PATH.")
	appsDir := fs.String("apps-dir", "", "apps-dir passed to the forked wash-router.")
	authTest := fs.String("auth-test", "", `dev/CI backend: --auth-test "user:password" hard-codes one credential. When set, overrides the default su+passwd backend.`)
	authTestUID := fs.Uint("auth-test-uid", 0, "uid for a successful --auth-test login (default: this process's uid).")
	authTestGID := fs.Uint("auth-test-gid", 0, "gid for a successful --auth-test login (default: this process's gid).")
	authTestShell := fs.String("auth-test-shell", "/bin/sh", "shell recorded on a successful --auth-test login.")
	suPath := fs.String("su-path", "", "path to su for unix auth. Empty searches /bin and /usr/bin.")
	passwdPath := fs.String("passwd-path", "/etc/passwd", "passwd file for the user lookup.")
	autologin := fs.String("autologin", "", "skip the login prompt and bring wash-router up immediately as this user (browser VM — no auth boundary). uid/gid/shell resolved via --passwd-path.")
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Printf("wash-vmlogin %s\n", version.Version)
		return 0
	}

	logger := log.New(os.Stderr, "wash-vmlogin ", log.LstdFlags|log.Lmsgprefix)

	devFd, err := openChannel(*transport)
	if err != nil {
		logger.Printf("channel: %v", err)
		return 1
	}

	routerBin := resolveRouterBinary(*routerBinary)
	if routerBin == "" {
		logger.Printf("could not locate wash-router; pass --router-binary")
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	spawn := spawnRouter(devFd, routerBin, *appsDir, logger)

	// Auto-login: bring wash-router up immediately as the named user, no login
	// prompt. The browser VM is the user's own single-user sandbox — there's no
	// auth boundary to enforce — so it boots straight to the desktop; the
	// browser then reattaches to the live router (catalog, not the login form).
	// No su/password: uid/gid/shell come straight from passwd. On router exit
	// the supervisor loop (S99wash-router) respawns us.
	if *autologin != "" {
		id, err := resolveAutoLoginID(*autologin, *passwdPath)
		if err != nil {
			logger.Printf("autologin %q: %v", *autologin, err)
			return 1
		}
		logger.Printf("wash-vmlogin %s up: autologin=%q uid=%d transport=%s router=%s — no login prompt",
			version.Version, id.Name, id.UID, *transport, routerBin)
		if err := spawn(ctx, id); err != nil && ctx.Err() == nil {
			logger.Printf("autologin router: %v", err)
			return 1
		}
		return 0
	}

	auth, err := buildAuth(*authTest, uint32(*authTestUID), uint32(*authTestGID), *authTestShell, *suPath, *passwdPath, logger)
	if err != nil {
		logger.Printf("auth: %v", err)
		return 1
	}
	front := &guest.Front{
		Auth:   auth,
		Spawn:  spawn,
		Logger: logger,
	}
	logger.Printf("wash-vmlogin %s up: transport=%s router=%s apps-dir=%s", version.Version, *transport, routerBin, *appsDir)

	if err := front.Serve(ctx, wire.NewStreamTransport(&rawFD{fd: devFd})); err != nil &&
		ctx.Err() == nil {
		logger.Printf("serve: %v", err)
		return 1
	}
	return 0
}

// resolveAutoLoginID looks up uid/gid/shell for the --autologin user straight
// from passwd (no password — the browser VM trusts its single user).
func resolveAutoLoginID(name, passwdPath string) (guest.Identity, error) {
	u, err := (&login.PasswdLister{Path: passwdPath}).Lookup(name)
	if err != nil {
		return guest.Identity{}, fmt.Errorf("passwd lookup %q in %s: %w", name, passwdPath, err)
	}
	return guest.Identity{UID: u.UID, GID: u.GID, Name: u.Name, Shell: u.Shell}, nil
}

// openChannel resolves the --transport flag to a blocking channel fd. fd:N
// inherits a launcher-preopened descriptor; path schemes open the device with
// a raw syscall (no Go poller, no O_NONBLOCK) so the fd stays blocking for the
// forked wash-router's rawFD reads.
func openChannel(transport string) (int, error) {
	i := strings.Index(transport, ":")
	if i < 0 {
		return -1, fmt.Errorf("expected <scheme>:<path|fd>, got %q", transport)
	}
	scheme, rest := transport[:i], transport[i+1:]
	switch scheme {
	case "fd":
		n, err := strconv.Atoi(rest)
		if err != nil {
			return -1, fmt.Errorf("fd:%s: %w", rest, err)
		}
		return n, nil
	case "virtio-console", "serial":
		fd, err := syscall.Open(rest, syscall.O_RDWR, 0)
		if err != nil {
			return -1, fmt.Errorf("open %s: %w", rest, err)
		}
		return fd, nil
	default:
		return -1, fmt.Errorf("unsupported scheme %q (expected fd, virtio-console, serial)", scheme)
	}
}

// buildAuth returns a guest.Authenticator: --auth-test for dev/CI, otherwise
// su-via-PTY backed by the passwd user lookup (same backends as wash-login,
// adapted to the guest contract).
func buildAuth(authTest string, uid, gid uint32, shell, suPath, passwdPath string, logger *log.Logger) (guest.Authenticator, error) {
	var inner login.Authenticator
	if authTest != "" {
		if uid == 0 {
			uid = uint32(os.Getuid())
		}
		if gid == 0 {
			gid = uint32(os.Getgid())
		}
		a, err := login.NewTestAuth(authTest, uid, gid, shell)
		if err != nil {
			return nil, err
		}
		inner = a
		logger.Printf("auth: --auth-test (dev/CI backend)")
	} else {
		lister := &login.PasswdLister{Path: passwdPath}
		a, err := login.NewSuAuth(suPath, lister.Lookup)
		if err != nil {
			return nil, err
		}
		inner = a
		logger.Printf("auth: su via PTY (passwd=%s)", passwdPath)
	}
	return authAdapter{inner}, nil
}

// authAdapter bridges internal/login.Authenticator (HTTP login server's
// contract) to the guest core's dependency-light guest.Authenticator.
type authAdapter struct{ a login.Authenticator }

func (ad authAdapter) Authenticate(user, password string) (guest.Identity, error) {
	id, err := ad.a.Authenticate(user, password)
	if err != nil {
		return guest.Identity{}, err
	}
	return guest.Identity{UID: id.UID, GID: id.GID, Name: id.Name, Shell: id.Shell}, nil
}

// spawnRouter returns a guest.SpawnRouter that forks wash-router as the resolved
// uid/gid with the channel device at child fd 3, then blocks until it exits.
//
// syscall.ForkExec (not os/exec) is deliberate: Files maps child fd indices to
// parent fds directly, so the device passes through as a raw blocking fd with
// no *os.File wrapper — no Go runtime poller, no O_NONBLOCK that would break
// wash-router's rawFD syscall reads. fd 0/1/2 carry through so the router's
// stdout/stderr land on the launcher's log device.
func spawnRouter(devFd int, routerBin, appsDir string, logger *log.Logger) guest.SpawnRouter {
	return func(ctx context.Context, id guest.Identity) error {
		argv := []string{routerBin, "--transport=fd:3", "--session-preopened"}
		if appsDir != "" {
			argv = append(argv, "--apps-dir="+appsDir)
		}
		attr := &syscall.ProcAttr{
			// Inherit our environment explicitly — unlike os/exec, raw ForkExec
			// gives the child an EMPTY env when Env is nil, which would drop the
			// launcher's PATH/SHELL/WASH_NETD_BACKEND the desktop needs — but with
			// HOME/USER/LOGNAME re-pointed at the authed user (see childEnv).
			Env:   childEnv(id, logger),
			Files: []uintptr{0, 1, 2, uintptr(devFd)},
			Sys:   &syscall.SysProcAttr{Credential: resolveCredential(id, logger)},
		}
		pid, err := syscall.ForkExec(routerBin, argv, attr)
		if err != nil {
			return fmt.Errorf("fork wash-router: %w", err)
		}
		logger.Printf("forked wash-router pid=%d uid=%d gid=%d", pid, id.UID, id.GID)
		var ws syscall.WaitStatus
		for {
			_, err := syscall.Wait4(pid, &ws, 0, nil)
			if err != syscall.EINTR {
				return err
			}
		}
	}
}

// childEnv is the launcher environment with HOME/USER/LOGNAME re-pointed at
// the authed user. The launcher runs as root (init), so its os.Environ() has
// HOME=/root; without this the forked desktop — though setuid to `wash` —
// would inherit HOME=/root and resolve ~/.config to /root/.config, which it
// can't write (perm denied on every desktop.json / asset / settings write).
// We take the home from the passwd entry (falling back to /home/<name>).
func childEnv(id guest.Identity, logger *log.Logger) []string {
	home := ""
	if u, err := user.Lookup(id.Name); err == nil && u.HomeDir != "" {
		home = u.HomeDir
	} else if id.Name != "" {
		home = "/home/" + id.Name
	}
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "USER=") || strings.HasPrefix(kv, "LOGNAME=") {
			continue // replaced below with the authed user's values
		}
		env = append(env, kv)
	}
	if home != "" {
		env = append(env, "HOME="+home)
	}
	if id.Name != "" {
		env = append(env, "USER="+id.Name, "LOGNAME="+id.Name)
	}
	logger.Printf("child env: HOME=%q USER=%q", home, id.Name)
	return env
}

// resolveCredential builds the setuid credential for the forked wash-router.
// It re-resolves uid/gid AND supplementary groups from the system user database
// (osusergo: /etc/passwd + /etc/group) keyed on the authed name — so the
// desktop keeps group memberships like `netdev` that gate NetworkManager
// access, which a bare Uid/Gid handoff would silently drop. Falls back to the
// Identity's uid/gid when the name isn't in the database (e.g. --auth-test with
// a synthetic user).
func resolveCredential(id guest.Identity, logger *log.Logger) *syscall.Credential {
	cred := &syscall.Credential{Uid: id.UID, Gid: id.GID}
	u, err := user.Lookup(id.Name)
	if err != nil {
		logger.Printf("credential: user %q not in passwd, using uid=%d gid=%d (no supplementary groups)", id.Name, id.UID, id.GID)
		return cred
	}
	if n, err := strconv.Atoi(u.Uid); err == nil {
		cred.Uid = uint32(n)
	}
	if n, err := strconv.Atoi(u.Gid); err == nil {
		cred.Gid = uint32(n)
	}
	if gids, err := u.GroupIds(); err == nil {
		for _, g := range gids {
			if n, err := strconv.Atoi(g); err == nil {
				cred.Groups = append(cred.Groups, uint32(n))
			}
		}
	}
	logger.Printf("credential: uid=%d gid=%d groups=%v (user %q)", cred.Uid, cred.Gid, cred.Groups, id.Name)
	return cred
}

// resolveRouterBinary locates wash-router: explicit override, then beside this
// binary (the colocated install/dev case), then PATH.
func resolveRouterBinary(override string) string {
	if override != "" {
		return override
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "wash-router")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if p, err := exec.LookPath("wash-router"); err == nil {
		return p
	}
	return ""
}

// rawFD performs blocking I/O directly via syscall.Read/Write on a raw kernel
// fd — never wrapping it in *os.File, so Go's runtime poller never registers
// it (no fcntl O_NONBLOCK, no epoll_ctl). Same approach as the router's fd
// transport: on the HVC/virtio paths that registration can wedge khvcd.
type rawFD struct{ fd int }

func (r *rawFD) Read(p []byte) (int, error) {
	n, err := syscall.Read(r.fd, p)
	if err != nil {
		return n, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (r *rawFD) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		n, err := syscall.Write(r.fd, p[written:])
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
		written += n
	}
	return written, nil
}

func (r *rawFD) Close() error { return syscall.Close(r.fd) }
