package login

// suAuth tests using a fake su shell script. The script reads the
// "password" from stdin (we give it a PTY via creack/pty exactly the
// same way the real su path does) and exits 0 / 1 based on whether
// the password matches what the test embedded into the script. This
// exercises the PTY drive logic without depending on a real Unix
// account with a known password.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeFakeSu writes a shell script at path that:
//   - Prints "Password: " (no newline) so the prompt-detector
//     finds the trailing ":"
//   - Reads a line from stdin
//   - Compares against `want` (the embedded "valid password")
//   - Exits 0 on match, 1 otherwise
//
// Mimics what the real su would do over a PTY for our purposes.
func writeFakeSu(t *testing.T, path, want string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
# args: -c <cmd> <user>. Ignore everything except stdin password.
printf "Password: "
read -r got
if [ "$got" = %q ]; then
  exit 0
else
  exit 1
fi
`, want)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake su: %v", err)
	}
}

func fakeLookup(name string) (User, error) {
	if name == "alice" {
		return User{Name: "alice", UID: 1000, GID: 1000, Shell: "/bin/sh"}, nil
	}
	return User{}, ErrUserNotFound
}

func TestSuAuthValidPassword(t *testing.T) {
	dir := t.TempDir()
	fakeSu := filepath.Join(dir, "su")
	writeFakeSu(t, fakeSu, "hunter2")

	auth, err := NewSuAuth(fakeSu, fakeLookup)
	if err != nil {
		t.Fatalf("NewSuAuth: %v", err)
	}
	id, err := auth.Authenticate("alice", "hunter2")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.UID != 1000 || id.Name != "alice" {
		t.Errorf("identity: %+v", id)
	}
}

func TestSuAuthInvalidPassword(t *testing.T) {
	dir := t.TempDir()
	fakeSu := filepath.Join(dir, "su")
	writeFakeSu(t, fakeSu, "hunter2")

	auth, err := NewSuAuth(fakeSu, fakeLookup)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.Authenticate("alice", "wrong")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("got %v, want ErrInvalidCredentials", err)
	}
}

func TestSuAuthUnknownUserAfterSuccess(t *testing.T) {
	// Fake su accepts the password — but the lookup func reports the
	// user doesn't exist. Should surface as ErrInvalidCredentials
	// rather than leaking the partial-success state.
	dir := t.TempDir()
	fakeSu := filepath.Join(dir, "su")
	writeFakeSu(t, fakeSu, "x")

	auth, err := NewSuAuth(fakeSu, fakeLookup)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.Authenticate("ghost", "x")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("got %v, want ErrInvalidCredentials", err)
	}
}

func TestSuAuthRejectsBadUsernames(t *testing.T) {
	auth, err := NewSuAuth("/bin/true", fakeLookup)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "user\nwith\nnewline", "user:colon", "user\x00nul"} {
		_, err := auth.Authenticate(bad, "anything")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Authenticate(%q): got %v want ErrInvalidCredentials", bad, err)
		}
	}
}

func TestNewSuAuthMissingBinary(t *testing.T) {
	_, err := NewSuAuth("/nope/not/here/su", fakeLookup)
	if err == nil {
		t.Fatal("expected error for nonexistent su path")
	}
}
