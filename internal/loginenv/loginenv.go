// Package loginenv adopts the environment a login shell would have
// produced into the current process.
//
// The per-user router is spawned by wash-login — itself a systemd
// service — so it starts with systemd's minimal environment: no
// ~/.profile, no ~/.local/bin on PATH. Every app the router spawns
// (wash-term tabs, wash-edit's embedded terminal, anything launched
// from them) inherits that, so user-installed CLIs that work fine
// over ssh are "command not found" inside wash. Running the user's
// login shell once at router startup and merging the environment it
// exports fixes the whole process tree at the root — the same trick
// VS Code's shell-env resolution uses.
package loginenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	osuser "os/user"
	"strings"
	"time"
)

// UserShell returns the current euid's login shell: the passwd entry
// first (not $SHELL — under sudo or a system spawner $SHELL is the
// invoking user's, or unset entirely), then $SHELL, then /bin/bash.
func UserShell() string {
	if u, err := osuser.Current(); err == nil {
		if sh := ShellFromPasswd(u.Uid); sh != "" {
			return sh
		}
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/bash"
}

// ShellFromPasswd parses /etc/passwd for the row with matching uid
// and returns its shell field, or "" when the uid has no row (or
// /etc/passwd is unreadable). Go's os/user doesn't expose the shell
// field, so read the file directly.
func ShellFromPasswd(uid string) string {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 7 && fields[2] == uid {
			return fields[6]
		}
	}
	return ""
}

// Resolve runs shellPath as a login shell (-l) and returns the
// environment it exports. A random marker separates profile noise
// (echoes, motd fragments) from the env dump, and the dump is
// NUL-separated (env -0) so multi-line values survive; when env -0
// isn't available the plain `env` fallback is parsed line-wise,
// losing multi-line values but nothing else. Profiles that prompt
// hang against a /dev/null stdin until ctx expires — callers should
// bound this and fail open.
func Resolve(ctx context.Context, shellPath string) (map[string]string, error) {
	markerBytes := make([]byte, 16)
	if _, err := rand.Read(markerBytes); err != nil {
		return nil, fmt.Errorf("mint marker: %w", err)
	}
	marker := "WASH_LOGINENV_" + hex.EncodeToString(markerBytes)

	// `command env` dodges aliases/functions named env. POSIX-ish
	// syntax on purpose: works in bash/zsh/dash and fish ≥3.0; an
	// exotic login shell just makes Resolve error and the caller
	// keeps the inherited env.
	script := fmt.Sprintf("printf '%%s' %s; command env -0 2>/dev/null || command env", marker)
	cmd := exec.CommandContext(ctx, shellPath, "-l", "-c", script)
	// Without WaitDelay, Output() blocks past ctx for as long as any
	// grandchild holds the stdout pipe — a profile that spawns a
	// background daemon (ssh-agent …) would stall the router for the
	// daemon's lifetime, not the ctx deadline.
	cmd.WaitDelay = time.Second
	if home := os.Getenv("HOME"); home != "" {
		// Login shells start in $HOME and profiles sometimes assume it.
		if st, err := os.Stat(home); err == nil && st.IsDir() {
			cmd.Dir = home
		}
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s -l: %w (stderr: %.200s)", shellPath, err, stderr.String())
	}
	idx := strings.Index(string(out), marker)
	if idx < 0 {
		return nil, fmt.Errorf("%s -l: no marker in output (%d bytes) — profile swallowed stdout?", shellPath, len(out))
	}
	payload := string(out)[idx+len(marker):]

	sep := "\x00"
	if !strings.Contains(payload, sep) {
		sep = "\n" // env-without--0 fallback
	}
	env := make(map[string]string)
	for _, entry := range strings.Split(payload, sep) {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || !validKey(k) {
			// In line-fallback mode this also drops continuation lines
			// of multi-line values — acceptable for a fallback.
			continue
		}
		env[k] = v
	}
	if len(env) == 0 {
		return nil, fmt.Errorf("%s -l: env dump parsed to nothing", shellPath)
	}
	return env, nil
}

// validKey reports whether k looks like a real variable name. Filters
// the junk a line-parsed dump can produce (fragments of multi-line
// values, bash's exported-function pseudo-vars like BASH_FUNC_x%%).
func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// skipKey lists what Adopt refuses to take from the login shell:
// identity and session plumbing wash-login/the router established
// deliberately (a profile that rewrites HOME must not break the
// setuid session), transient shell state, host GUI handles (the
// router strips those from app env anyway — see inheritedAppEnv),
// ssh connection metadata that would misdescribe the router, and
// WASH_* so a profile can't reconfigure the router underneath its
// own flags. SHELL is allowed through: bash fills it from passwd
// when unset, which is exactly what wash-term's tabs want.
func skipKey(k string) bool {
	switch k {
	case "_", "SHLVL", "PWD", "OLDPWD",
		"HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR",
		"DISPLAY", "WAYLAND_DISPLAY", "WAYLAND_SOCKET", "XAUTHORITY",
		"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY":
		return true
	}
	return strings.HasPrefix(k, "WASH_")
}

// merge applies resolved to setenv, skipping denied keys and values
// that already match. Split from Adopt so it's testable without
// mutating the test process env.
func merge(resolved map[string]string, setenv func(k, v string) error) int {
	n := 0
	for k, v := range resolved {
		if skipKey(k) || os.Getenv(k) == v {
			continue
		}
		if setenv(k, v) == nil {
			n++
		}
	}
	return n
}

// Adopt resolves the login-shell environment for the current user
// and merges it into the process env. Returns how many vars were
// set. On any failure the process env is left untouched — callers
// log and continue with the inherited environment.
func Adopt(ctx context.Context) (int, error) {
	resolved, err := Resolve(ctx, UserShell())
	if err != nil {
		return 0, err
	}
	return merge(resolved, os.Setenv), nil
}
