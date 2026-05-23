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
	// Both should show up; symlink-name and the real binary.
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 entries", got)
	}
	// Sorted: wash, wash-about.
	if got[0] != target || got[1] != link {
		t.Fatalf("got %v, want [%s %s]", got, target, link)
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
