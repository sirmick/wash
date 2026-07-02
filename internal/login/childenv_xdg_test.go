package login

import "testing"

// TestChildEnvSetsPerUserXDGRuntimeDir — (REVIEW-X11-WAYLAND #4) the spawned
// router (and its wash-display child) must get a per-uid XDG_RUNTIME_DIR, not
// wash-login's own (or none). childEnv sets it to <runRoot>/<uid>/xdg and
// strips any inherited XDG_RUNTIME_DIR.
func TestChildEnvSetsPerUserXDGRuntimeDir(t *testing.T) {
	// A stray inherited value that must NOT leak through.
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/9999")

	env := childEnv(Identity{UID: 1234, Name: "alice"}, "/run/wash")

	want := "XDG_RUNTIME_DIR=/run/wash/1234/xdg"
	got := ""
	count := 0
	for _, kv := range env {
		if len(kv) >= len("XDG_RUNTIME_DIR=") && kv[:len("XDG_RUNTIME_DIR=")] == "XDG_RUNTIME_DIR=" {
			got = kv
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one XDG_RUNTIME_DIR in child env, got %d (env=%v)", count, env)
	}
	if got != want {
		t.Errorf("XDG_RUNTIME_DIR = %q, want %q (inherited value must be replaced)", got, want)
	}
}

func TestPerUserRuntimeDir(t *testing.T) {
	if d := PerUserRuntimeDir("/run/wash", 1000); d != "/run/wash/1000/xdg" {
		t.Errorf("PerUserRuntimeDir = %q, want /run/wash/1000/xdg", d)
	}
}
