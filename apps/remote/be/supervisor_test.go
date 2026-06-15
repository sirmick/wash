package remote

import (
	"strings"
	"testing"
)

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

func TestBuildSSHArgs(t *testing.T) {
	args := buildSSHArgs("user@host", "/tmp/wash/b.sock", 11000)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"/tmp/wash/b.sock:127.0.0.1:11000", // the -L unix-socket forward spec
		"BatchMode=yes",
		"ExitOnForwardFailure=yes",
		"wash-router",
		"--listen-raw",
		"tcp:127.0.0.1:11000", // raw-wire listener (no HTTP/WS)
		"--no-session",
		"--no-auth",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildSSHArgs missing %q\n  got: %v", want, args)
		}
	}
	// No TCP port should bind on A's loopback; only the unix socket forwards.
	if strings.Contains(joined, "--listen ") || strings.Contains(joined, "--allow-cross-origin") {
		t.Errorf("relay must use --listen-raw + unix socket, not --listen/--allow-cross-origin: %v", args)
	}

	// The SSH target host must precede the remote command, or ssh would
	// treat "wash-router" as the host.
	hostIdx := indexOf(args, "user@host")
	cmdIdx := indexOf(args, "wash-router")
	if hostIdx < 0 || cmdIdx < 0 || hostIdx > cmdIdx {
		t.Errorf("host must come before the remote command: hostIdx=%d cmdIdx=%d args=%v", hostIdx, cmdIdx, args)
	}
}

func TestBuildSSHArgsForwardMatchesListen(t *testing.T) {
	args := buildSSHArgs("h", "/run/x.sock", 9999)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "/run/x.sock:127.0.0.1:9999") {
		t.Errorf("forward spec wrong: %v", args)
	}
	if !strings.Contains(joined, "tcp:127.0.0.1:9999") {
		t.Errorf("raw listen target wrong: %v", args)
	}
}

// TestIsAuthFailure pins the classification that drives wash-connect's
// ssh-add widget (docs/REMOTE.md §6.1): auth refusals get code "auth" (so
// the FE offers Authenticate), while network/host errors do not (no key
// will fix them).
func TestIsAuthFailure(t *testing.T) {
	auth := []string{
		"user@host: Permission denied (publickey).",
		"Permission denied (publickey,password).",
		"Received disconnect from 10.0.0.5 port 22:2: Too many authentication failures",
		"Host key verification failed.",
		"ssh: No more authentication methods to try.",
	}
	for _, s := range auth {
		if !isAuthFailure(s) {
			t.Errorf("expected auth failure for %q", s)
		}
	}
	notAuth := []string{
		"ssh: connect to host host port 22: Connection refused",
		"ssh: Could not resolve hostname nope: Name or service not known",
		"channel_setup_fwd_listener_tcpip: cannot listen to port: 40001",
		"",
	}
	for _, s := range notAuth {
		if isAuthFailure(s) {
			t.Errorf("did not expect auth failure for %q", s)
		}
	}
}
