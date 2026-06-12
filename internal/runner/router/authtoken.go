package routerrun

// Recovery for the --transport=ws gate token: a long-lived router's
// startup line scrolls off the console within days, and an
// auto-generated token lives only in process memory. Two recovery
// paths, so at least one works regardless of how the router was
// launched:
//
//   1. A 0600 token file (works headless: systemd, nohup, &, pipes).
//   2. Reprint-on-Enter when attached to an interactive terminal.

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// routerRuntimeDir picks a per-user runtime directory for the token
// file: $XDG_RUNTIME_DIR/wash on logind systems, else /tmp/wash-<uid>.
// Mirrors wash-login's defaultRunRoot logic so both daemons agree on
// where transient per-user state lives.
func routerRuntimeDir() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "wash")
	}
	return fmt.Sprintf("/tmp/wash-%d", os.Getuid())
}

// defaultTokenFilePath is the per-pid token file used when the operator
// didn't pass --auth-token-file. Per-pid so concurrent routers on one
// box don't clobber each other's tokens.
func defaultTokenFilePath(pid int) string {
	return filepath.Join(routerRuntimeDir(), fmt.Sprintf("router-%d.token", pid))
}

// writeTokenFile writes token to path with mode 0600, creating the
// parent directory (0700) if needed. The trailing newline makes the
// file friendly to `cat` and `$(...)` capture.
func writeTokenFile(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(token+"\n"), 0o600)
}

// readTokenFile reads a token previously written by writeTokenFile,
// trimming surrounding whitespace. Returns an error if the file is
// absent or unreadable (the caller treats that as "no existing token").
func readTokenFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// startTokenReprint launches a goroutine that reprints tokenURL each
// time the operator presses Enter — but only when stdin is an
// interactive, foreground terminal. A backgrounded process (launched
// with &, under systemd, or with stdin redirected) must NOT read stdin:
// a background read against a controlling TTY raises SIGTTIN and would
// stop the router. We therefore (a) require stdin to be a TTY and (b)
// require this process to be the terminal's foreground process group,
// and we ignore SIGTTIN defensively so a race into the background can't
// freeze the daemon.
func startTokenReprint(tokenURL, tokenFilePath string, logger *log.Logger) {
	fd := int(os.Stdin.Fd())
	if !isForegroundTTY(fd) {
		return
	}
	// Belt-and-braces: if we lose the foreground (someone backgrounds
	// us later), a read would raise SIGTTIN — ignore it so the read
	// just returns an error and the goroutine exits instead of the
	// whole router stopping.
	signal.Ignore(unix.SIGTTIN)

	logger.Printf("  (press Enter at this console to reprint the token URL)")
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			logger.Printf("auth token — open: %s", tokenURL)
			logger.Printf("  token file: %s", tokenFilePath)
		}
		// EOF or a read error (e.g. backgrounded → EIO): stop quietly.
		// The token file remains the recovery path.
	}()
}

// isForegroundTTY reports whether fd is a terminal AND this process is
// its foreground process group — the only safe condition under which a
// long-lived process may read stdin.
func isForegroundTTY(fd int) bool {
	// IoctlGetTermios fails with ENOTTY on non-terminals.
	if _, err := unix.IoctlGetTermios(fd, unix.TCGETS); err != nil {
		return false
	}
	// TIOCGPGRP returns the terminal's foreground process group.
	fg, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		return false
	}
	return fg == unix.Getpgrp()
}
