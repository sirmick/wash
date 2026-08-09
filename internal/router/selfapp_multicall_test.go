//go:build multicall

package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/sirmick/wash/pkg/apps/registry"
	"github.com/sirmick/wash/pkg/wire"
)

// makeValidApp builds a minimally-valid registry.App named after this
// test binary's basename so selfApp can match it.
func makeValidApp(name string) *registry.App {
	return &registry.App{
		Name: name,
		Manifest: wire.Manifest{
			ID:              "com.wash.fake-" + name,
			Name:            name,
			Version:         "0.0.1",
			ProtocolVersion: wire.ProtocolVersion,
			Element:         "wash-app-fake",
			Surface:         wire.SurfaceWindow,
			Instancing:      wire.InstancingMulti,
			Icon:            "data:image/svg+xml,W",
		},
		Assets: fstest.MapFS{},
		Run:    func(context.Context) error { return nil },
	}
}

// resetRegistry empties the in-process registry between tests. The
// real registry has no exported reset; we re-register fresh apps
// per test via subtest scopes that don't conflict.
func resetRegistry(t *testing.T) {
	t.Helper()
	// nothing — each test uses a unique name to avoid duplicate panics.
}

// In multi-call builds, a symlink in some apps dir whose target is
// the running binary should resolve to the registered App. We exploit
// the fact that under `go test`, os.Executable() is the test binary,
// and we can symlink to it from a tmpdir.
func TestSelfApp_SymlinkToSelfResolvesRegisteredApp(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "wash-faketest1"
	link := filepath.Join(dir, name)
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}

	a := makeValidApp(name)
	registry.Register(a)

	got := selfApp(link)
	if got == nil {
		t.Fatalf("selfApp(%q) = nil, want registered app", link)
	}
	if got.Name != name {
		t.Fatalf("selfApp returned %q, want %q", got.Name, name)
	}
}

// A non-symlink path that isn't our executable returns nil; nothing
// happens to fall through to the registered app.
func TestSelfApp_UnrelatedPathReturnsNil(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "wash-faketest2")
	// Real file with arbitrary contents — not the test binary.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	registry.Register(makeValidApp("wash-faketest2"))

	if got := selfApp(bin); got != nil {
		t.Fatalf("selfApp(%q) = %v, want nil (target is not our executable)", bin, got)
	}
}

// Symlink to self, but the registry has no entry for the basename →
// nil. The fast path is opt-in per registered name; missing entries
// fall through to the exec-probe path (which would then fail
// gracefully in real use).
func TestSelfApp_SymlinkToSelfWithoutRegistrationReturnsNil(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "wash-unregistered-faketest3")
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	if got := selfApp(link); got != nil {
		t.Fatalf("selfApp(%q) = %v, want nil (no registration)", link, got)
	}
}

// probeAndRegister's fast-path branch produces an Entry whose manifest
// is the in-process registration's. No exec happens (the test binary
// would otherwise be invoked with --wash-manifest, which it doesn't
// understand and would fail probe).
func TestProbeAndRegister_FastPathSkipsExec(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "wash-faketest4"
	link := filepath.Join(dir, name)
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	a := makeValidApp(name)
	registry.Register(a)

	r := NewRegistry()
	r.probeAndRegister(context.Background(), link)

	e := r.ByID(a.Manifest.ID)
	if e == nil {
		t.Fatalf("entry not registered under id %q", a.Manifest.ID)
	}
	if e.Reason != "" {
		t.Fatalf("entry has Reason %q, want empty (fast-path success)", e.Reason)
	}
	if e.Path != link {
		t.Fatalf("entry.Path = %q, want %q", e.Path, link)
	}
	if e.Manifest.Name != name {
		t.Fatalf("manifest came from elsewhere: Name=%q want %q", e.Manifest.Name, name)
	}
}
