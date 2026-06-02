package priv

import (
	"sort"
	"strings"
)

// This file owns the single construction of a `sudo` argument list used
// by all three exec paths in wash-priv:
//
//   - runSudo        (queue.go)      — spawns a REGISTERED app as root
//   - startInlineRun (inline.go)     — wash-sudo CLI bridge (pipe mode)
//   - startInlinePTY (inline_pty.go) — embedded-terminal runs (pty mode)
//
// Before, each rebuilt the same "-S -k / -n  --preserve-env=…  -- argv"
// shape by hand, which let the password-mode logic and the env-scrub
// allowlist drift apart silently. The two allowlists below are
// deliberately DIFFERENT (app-spawn vs inline), but the assembly is now
// shared so only the allowlist — never the mechanics — varies per site.

// inlinePreserveEnv is the WASH_* allowlist forwarded through sudo's env
// scrubbing for INLINE command runs (the wash-sudo CLI bridge and
// embedded-terminal apps such as packages). It carries the control
// socket + bin dir so the run can still reach the router and find its
// sibling wash binaries. Callers merge their own requested vars on top.
//
// Pre-sorted for readability; sudoArgv sorts the final union anyway.
var inlinePreserveEnv = []string{"WASH_BIN_DIR", "WASH_CONTROL_SOCKET", "WASH_DISPLAY", "WASH_PROTO"}

// appSpawnPreserveEnv is the allowlist for runSudo, which spawns a
// registered app as root. Unlike the inline list it carries the router
// re-attach identity (app id / instance id / attach token) instead of
// the control socket, because the spawned app re-handshakes with the
// router as an ordinary app would. Keeping these two lists distinct is
// intentional — see the file comment.
var appSpawnPreserveEnv = []string{"WASH_APP_ID", "WASH_ATTACH_TOKEN", "WASH_DISPLAY", "WASH_INSTANCE_ID", "WASH_PROTO"}

// sudoArgv builds the argument list for `sudo` (excluding the sudo
// binary itself, which becomes argv[0] of the resulting exec.Command)
// to run target under sudo.
//
//   - injectPassword=true  → "-S -k": read the password from stdin and
//     drop any cached credentials. The caller MUST feed pw\n to stdin.
//   - injectPassword=false → "-n": non-interactive; sudo fails fast
//     rather than prompt (used when a NOPASSWD policy is expected).
//
// preserve is the base WASH_* allowlist; keys of extraEnv (caller-
// requested vars) are merged in and the union is sorted so the argv is
// deterministic. target is the command (and its args) to run as root.
func sudoArgv(injectPassword bool, preserve []string, extraEnv map[string]string, target []string) []string {
	var cmdArgs []string
	if injectPassword {
		cmdArgs = []string{"-S", "-k"}
	} else {
		cmdArgs = []string{"-n"}
	}
	names := append([]string(nil), preserve...)
	for k := range extraEnv {
		names = append(names, k)
	}
	sort.Strings(names)
	cmdArgs = append(cmdArgs, "--preserve-env="+strings.Join(names, ","))
	cmdArgs = append(cmdArgs, "--")
	return append(cmdArgs, target...)
}
