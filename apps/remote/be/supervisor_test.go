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
