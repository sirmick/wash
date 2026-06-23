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
	args := buildSSHArgs("user@host", "/tmp/a/A.sock", "/tmp/wash-relay-deadbeef.sock", 0)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"/tmp/a/A.sock:/tmp/wash-relay-deadbeef.sock", // -L unix→unix forward
		"BatchMode=yes",
		"ExitOnForwardFailure=yes",
		"wash-router",
		"--listen-raw",
		"unix:/tmp/wash-relay-deadbeef.sock", // raw-wire UNIX listener on B (chmod 0600 there)
		"--no-session",
		"--no-auth",
		"--control-socket", "/tmp/wash-relay-deadbeef.sock.ctl", // unique ctl socket (spawn needs it; avoids B-desktop collision)
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildSSHArgs missing %q\n  got: %v", want, args)
		}
	}
	// No TCP loopback listener on B (multi-user-reachable), no ws listener.
	if strings.Contains(joined, "tcp:127.0.0.1") || strings.Contains(joined, "--listen ") || strings.Contains(joined, "--allow-cross-origin") {
		t.Errorf("relay must use a unix socket end to end, not tcp loopback / --listen: %v", args)
	}

	// The SSH target host must precede the remote command, or ssh would
	// treat "wash-router" as the host.
	hostIdx := indexOf(args, "user@host")
	cmdIdx := indexOf(args, "wash-router")
	if hostIdx < 0 || cmdIdx < 0 || hostIdx > cmdIdx {
		t.Errorf("host must come before the remote command: hostIdx=%d cmdIdx=%d args=%v", hostIdx, cmdIdx, args)
	}

	// Default/zero port emits no -p; a non-default port is dialed with -p
	// (ssh's positional target can't carry host:port).
	if i := indexOf(args, "-p"); i >= 0 {
		t.Errorf("default port must not emit -p: %v", args)
	}
	p := buildSSHArgs("user@host", "/tmp/a/A.sock", "/tmp/b.sock", 2222)
	if i := indexOf(p, "-p"); i < 0 || i+1 >= len(p) || p[i+1] != "2222" {
		t.Errorf("non-default port must emit -p 2222: %v", p)
	}
	// -p must precede the positional host, like every other ssh option.
	if indexOf(p, "-p") > indexOf(p, "user@host") {
		t.Errorf("-p must come before the host target: %v", p)
	}
}

// TestRemoteSockPathUnique: per-connection B socket paths must not collide.
func TestRemoteSockPathUnique(t *testing.T) {
	a, b := remoteSockPath(), remoteSockPath()
	if a == b {
		t.Errorf("remoteSockPath not unique: %s == %s", a, b)
	}
	for _, p := range []string{a, b} {
		if !strings.HasPrefix(p, "/tmp/wash-relay-") || !strings.HasSuffix(p, ".sock") {
			t.Errorf("unexpected socket path shape: %s", p)
		}
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
