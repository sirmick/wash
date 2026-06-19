package vmlogin

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirmick/wash/wash-vm/guest"
)

func envVal(env []string, key string) (string, int) {
	val, n := "", 0
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			val = kv[len(key)+1:]
			n++
		}
	}
	return val, n
}

// childEnv must re-point HOME/USER/LOGNAME at the authed user (not the root
// launcher's /root), or the forked desktop hits perm-denied writing ~/.config.
func TestChildEnvRepointsHomeToAuthedUser(t *testing.T) {
	t.Setenv("HOME", "/root")
	t.Setenv("USER", "root")
	t.Setenv("LOGNAME", "root")
	t.Setenv("PATH", "/usr/bin:/bin")

	lg := log.New(io.Discard, "", 0)
	// A name not in passwd → home falls back to /home/<name>.
	env := childEnv(guest.Identity{Name: "wash-test-xyz", UID: 1000, GID: 1000}, lg)

	if home, n := envVal(env, "HOME"); home != "/home/wash-test-xyz" || n != 1 {
		t.Fatalf("HOME = %q (count %d), want /home/wash-test-xyz (count 1)", home, n)
	}
	if u, n := envVal(env, "USER"); u != "wash-test-xyz" || n != 1 {
		t.Fatalf("USER = %q (count %d), want wash-test-xyz (count 1)", u, n)
	}
	if l, _ := envVal(env, "LOGNAME"); l != "wash-test-xyz" {
		t.Fatalf("LOGNAME = %q, want wash-test-xyz", l)
	}
	// Unrelated env is preserved (the desktop needs PATH etc.).
	if p, _ := envVal(env, "PATH"); p != "/usr/bin:/bin" {
		t.Fatalf("PATH = %q, want it preserved", p)
	}
}

// resolveAutoLoginID maps a passwd entry to the spawn identity, no password.
func TestResolveAutoLoginID(t *testing.T) {
	pw := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(pw, []byte(
		"root:x:0:0:root:/root:/bin/sh\n"+
			"wash:x:1000:1000:wash desktop user:/home/wash:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := resolveAutoLoginID("wash", pw)
	if err != nil {
		t.Fatalf("resolveAutoLoginID: %v", err)
	}
	if id.Name != "wash" || id.UID != 1000 || id.GID != 1000 || id.Shell != "/bin/sh" {
		t.Fatalf("id = %+v, want {wash 1000 1000 /bin/sh}", id)
	}
	if _, err := resolveAutoLoginID("ghost", pw); err == nil {
		t.Fatal("expected error for a user not in passwd")
	}
}
