package router

import (
	"os"
	"path/filepath"
	"testing"
)

// writeExec creates a 0755 file at path with stub contents — discovery
// doesn't probe, only checks +x.
func writeExec(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// os.WriteFile's mode is filtered by the process umask, so a
	// caller asking for 0o777 actually gets e.g. 0o755 under umask
	// 022 (the GitHub Actions default) — which silently flips the
	// trust posture these tests assert on. Chmod is NOT umask-masked,
	// so set the exact mode the test intends.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func TestExecutablesIn_DiscoversRegularBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "wash-about")
	writeExec(t, bin, 0o755)

	got, err := executablesIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != bin {
		t.Fatalf("got %v, want [%s]", got, bin)
	}
}

func TestExecutablesIn_DiscoversSymlinkToExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wash")
	writeExec(t, target, 0o755)

	link := filepath.Join(dir, "wash-about")
	if err := os.Symlink("wash", link); err != nil {
		t.Fatal(err)
	}

	got, err := executablesIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The wash-about symlink is an app candidate; the bare `wash`
	// dispatcher is filtered by the wash- prefix (it's the multicall
	// entrypoint, not an app), so only the symlink is returned.
	if len(got) != 1 || got[0] != link {
		t.Fatalf("got %v, want [%s]", got, link)
	}
}

func TestExecutablesIn_SkipsNonWashPrefixed(t *testing.T) {
	dir := t.TempDir()
	// A wash app and an unrelated executable (as a crowded /usr/bin
	// would hold). Only the wash- one is an app candidate; the other
	// must never be exec-probed.
	app := filepath.Join(dir, "wash-about")
	writeExec(t, app, 0o755)
	writeExec(t, filepath.Join(dir, "tree"), 0o755)
	writeExec(t, filepath.Join(dir, "washnt"), 0o755) // close-but-not wash-

	got, err := executablesIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != app {
		t.Fatalf("got %v, want [%s] (only the wash- prefixed binary)", got, app)
	}
}

func TestExecutablesIn_SkipsBrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "wash-ghost")
	if err := os.Symlink("nonexistent", link); err != nil {
		t.Fatal(err)
	}
	got, err := executablesIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want []", got)
	}
}

func TestExecutablesIn_SkipsSymlinkToNonExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "wash-data")
	if err := os.Symlink("data", link); err != nil {
		t.Fatal(err)
	}
	got, err := executablesIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want []", got)
	}
}

func TestExecutablesIn_SkipsSymlinkToDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "stuff")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "wash-stuff")
	if err := os.Symlink("stuff", link); err != nil {
		t.Fatal(err)
	}
	got, err := executablesIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want []", got)
	}
}

// isTrustedBinary follows symlinks (os.Stat), so a symlink inherits
// its target's trust posture. This is load-bearing for the multi-call
// layout where wash-priv → wash and the trust attaches to wash.
func TestIsTrustedBinary_SymlinkInheritsTrustedTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wash")
	writeExec(t, target, 0o755) // current uid, 0755 → branch (b) trusts it

	link := filepath.Join(dir, "wash-priv")
	if err := os.Symlink("wash", link); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	if !r.isTrustedBinary(link) {
		t.Fatalf("symlink %s pointing at 0755 same-uid target should be trusted", link)
	}
}

func TestIsTrustedBinary_SymlinkInheritsUntrustedTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wash")
	writeExec(t, target, 0o777) // world-writable → no branch trusts it

	link := filepath.Join(dir, "wash-priv")
	if err := os.Symlink("wash", link); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	if r.isTrustedBinary(link) {
		t.Fatalf("symlink %s pointing at world-writable target must not be trusted", link)
	}
}

func TestIsTrustedBinary_RejectsRegularWorldWritable(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "wash-priv")
	writeExec(t, bin, 0o777)

	r := NewRegistry()
	if r.isTrustedBinary(bin) {
		t.Fatalf("0777 binary must not be trusted")
	}
}

func TestIsTrustedBinary_AcceptsTrustedDir(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "wash-priv")
	// 0775 (group-writable) — branch (a) and (b) reject it, but
	// branch (c) accepts via the trusted-dir relaxation.
	writeExec(t, bin, 0o775)

	r := NewRegistry()
	r.SetTrustedDirs([]string{dir})
	if !r.isTrustedBinary(bin) {
		t.Fatalf("0775 binary under trusted dir should be accepted")
	}
}

// TestCatalogFiltersSurfaceBackground confirms surface=background
// entries don't appear in the shell catalog — alongside surface=
// desktop and Hidden, which the same filter already excluded.
//
// Lives in the router package (not registry) because the filter
// itself is on Router.catalog(), not Registry. The registry holds
// every entry; the catalog is what the shell renders.
func TestCatalogFiltersSurfaceBackground(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterEntry(&Entry{
		Path: "/tmp/wash-about",
		Manifest: &Manifest{
			ID: "com.wash.about", Name: "About wash", Version: "0.8.0",
			ProtocolVersion: ProtocolVersion, Element: "wash-app-about",
			Surface: SurfaceWindow, Icon: "data:image/svg+xml,W",
			Instancing: InstancingMulti,
		},
	})
	reg.RegisterEntry(&Entry{
		Path: "/tmp/wash-notify",
		Manifest: &Manifest{
			ID: "com.wash.notify", Name: "Notifications", Version: "0.8.0",
			ProtocolVersion: ProtocolVersion,
			Surface:         SurfaceBackground, Instancing: InstancingSingleton,
		},
	})
	reg.RegisterEntry(&Entry{
		Path: "/tmp/wash-session",
		Manifest: &Manifest{
			ID: "com.wash.session", Name: "Session", Version: "0.8.0",
			ProtocolVersion: ProtocolVersion, Element: "wash-app-session",
			Surface: SurfaceDesktop, Icon: "data:image/svg+xml,S",
			Instancing: InstancingSingleton,
		},
	})

	r := NewRouter(Config{}, reg, nil)
	cat := r.catalog()

	if len(cat) != 1 {
		t.Fatalf("catalog len=%d, want 1 (only wash-about visible); got %+v", len(cat), cat)
	}
	if cat[0].ID != "com.wash.about" {
		t.Fatalf("catalog[0]=%q, want com.wash.about", cat[0].ID)
	}
}
