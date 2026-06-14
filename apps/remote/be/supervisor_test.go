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
	args := buildSSHArgs("user@host", 40001, 11000)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"127.0.0.1:40001:127.0.0.1:11000", // the -L forward spec
		"BatchMode=yes",
		"ExitOnForwardFailure=yes",
		"wash-router",
		"--allow-cross-origin",
		"--no-session",
		"--no-auth",
		"127.0.0.1:11000", // router --listen
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildSSHArgs missing %q\n  got: %v", want, args)
		}
	}

	// The SSH target host must precede the remote command, or ssh would
	// treat "wash-router" as the host.
	hostIdx := indexOf(args, "user@host")
	cmdIdx := indexOf(args, "wash-router")
	if hostIdx < 0 || cmdIdx < 0 || hostIdx > cmdIdx {
		t.Errorf("host must come before the remote command: hostIdx=%d cmdIdx=%d args=%v", hostIdx, cmdIdx, args)
	}

	// The forward must target the same port the router is told to bind.
	if !strings.Contains(joined, "--listen 127.0.0.1:11000") {
		t.Errorf("router --listen port must match the forward target: %v", args)
	}
}

func TestBuildSSHArgsForwardMatchesListen(t *testing.T) {
	args := buildSSHArgs("h", 5, 9999)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "127.0.0.1:5:127.0.0.1:9999") {
		t.Errorf("forward spec wrong: %v", args)
	}
	if !strings.Contains(joined, "--listen 127.0.0.1:9999") {
		t.Errorf("listen port wrong: %v", args)
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
