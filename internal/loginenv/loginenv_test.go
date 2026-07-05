package loginenv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeShell writes an executable that mimics a login shell: it takes
// (-l -c <script>), prints some profile noise first, then runs the
// script with /bin/sh. That exercises the marker split against the
// exact argv Resolve builds.
func fakeShell(t *testing.T, prelude string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fakesh")
	body := "#!/bin/sh\n" + prelude + "\nexec /bin/sh -c \"$3\"\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveSurvivesProfileNoise(t *testing.T) {
	t.Setenv("LOGINENV_TEST_SENTINEL", "hello world")
	sh := fakeShell(t, `printf 'motd: welcome\nsome=noise that looks like env\n'`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env, err := Resolve(ctx, sh)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := env["LOGINENV_TEST_SENTINEL"]; got != "hello world" {
		t.Errorf("sentinel = %q, want %q", got, "hello world")
	}
	if _, ok := env["some"]; ok {
		t.Error("profile noise before the marker leaked into the parsed env")
	}
}

func TestResolveMultilineValue(t *testing.T) {
	t.Setenv("LOGINENV_TEST_MULTI", "line1\nline2")
	sh := fakeShell(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env, err := Resolve(ctx, sh)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// env -0 mode preserves embedded newlines. If this host's env has
	// no -0 (line fallback), the value is truncated at the newline —
	// accept either, but the key must parse cleanly.
	got := env["LOGINENV_TEST_MULTI"]
	if got != "line1\nline2" && got != "line1" {
		t.Errorf("multiline value = %q", got)
	}
}

func TestResolveNoMarkerErrors(t *testing.T) {
	// A "shell" that ignores the script entirely: no marker, no env.
	dir := t.TempDir()
	path := filepath.Join(dir, "brokensh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho nothing useful\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Resolve(ctx, path); err == nil {
		t.Fatal("Resolve succeeded with no marker in output")
	}
}

func TestResolveTimeout(t *testing.T) {
	sh := fakeShell(t, "sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Resolve(ctx, sh); err == nil {
		t.Fatal("Resolve succeeded despite a hung profile")
	}
}

func TestMergeSkipsDeniedAndUnchanged(t *testing.T) {
	t.Setenv("LOGINENV_TEST_SAME", "already")
	resolved := map[string]string{
		"PATH":                    "/home/u/.local/bin:/usr/bin",
		"LOGINENV_TEST_SAME":      "already",     // unchanged → skipped
		"LOGINENV_TEST_NEW":       "fresh",       // adopted
		"HOME":                    "/tmp/evil",   // denied
		"XDG_RUNTIME_DIR":         "/tmp/evil",   // denied
		"WASH_LISTEN":             "0.0.0.0:666", // denied prefix
		"_":                       "/usr/bin/env",
		"SHLVL":                   "2",
		"SSH_CONNECTION":          "1 2 3 4",
	}
	set := map[string]string{}
	n := merge(resolved, func(k, v string) error { set[k] = v; return nil })
	want := map[string]string{
		"PATH":              "/home/u/.local/bin:/usr/bin",
		"LOGINENV_TEST_NEW": "fresh",
	}
	if n != len(want) {
		t.Errorf("merge set %d vars, want %d (%v)", n, len(want), set)
	}
	for k, v := range want {
		if set[k] != v {
			t.Errorf("set[%s] = %q, want %q", k, set[k], v)
		}
	}
	for k := range set {
		if _, ok := want[k]; !ok {
			t.Errorf("merge adopted %s=%q, which should have been skipped", k, set[k])
		}
	}
}

func TestValidKey(t *testing.T) {
	for k, want := range map[string]bool{
		"PATH": true, "_": true, "MY_VAR2": true, "lower": true,
		"": false, "2LEAD": false, "BASH_FUNC_x%%": false, "has space": false,
	} {
		if got := validKey(k); got != want {
			t.Errorf("validKey(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestUserShellNonEmpty(t *testing.T) {
	if UserShell() == "" {
		t.Fatal("UserShell returned empty")
	}
}
