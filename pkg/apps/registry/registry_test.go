package registry

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sirmick/wash/internal/wire"
)

func validApp(name string) *App {
	return &App{
		Name: name,
		Manifest: wire.Manifest{
			ID:              "com.wash." + name,
			Name:            name,
			Version:         "0.0.1",
			ProtocolVersion: wire.ProtocolVersion,
			Element:         "wash-app-" + name,
			Surface:         wire.SurfaceWindow,
			Instancing:      wire.InstancingMulti,
			Icon:            "data:image/svg+xml,W",
		},
		Assets: fstest.MapFS{},
		Run:    func(context.Context) error { return nil },
	}
}

func TestRegisterAndGet(t *testing.T) {
	reset()
	a := validApp("about")
	Register(a)
	if got := Get("about"); got != a {
		t.Fatalf("Get: got %v, want %v", got, a)
	}
	if got := Get("missing"); got != nil {
		t.Fatalf("Get(missing) = %v, want nil", got)
	}
}

func TestAllSnapshot(t *testing.T) {
	reset()
	Register(validApp("a"))
	Register(validApp("b"))
	snap := All()
	if len(snap) != 2 {
		t.Fatalf("All: %d entries, want 2", len(snap))
	}
	// Mutating the snapshot must not affect the registry.
	delete(snap, "a")
	if Get("a") == nil {
		t.Fatal("snapshot mutation leaked into registry")
	}
}

func TestDuplicatePanics(t *testing.T) {
	reset()
	Register(validApp("about"))
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("duplicate Register should panic")
		} else if !strings.Contains(r.(string), "duplicate app") {
			t.Fatalf("panic message %q missing 'duplicate app'", r)
		}
	}()
	Register(validApp("about"))
}

func TestInvalidManifestPanics(t *testing.T) {
	reset()
	a := validApp("bad")
	a.Manifest.Element = "no-prefix" // fails ValidateManifest
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("invalid manifest should panic at Register")
		} else if !strings.Contains(r.(string), "invalid manifest") {
			t.Fatalf("panic %q missing 'invalid manifest'", r)
		}
	}()
	Register(a)
}

func TestMissingNamePanics(t *testing.T) {
	reset()
	a := validApp("x")
	a.Name = ""
	defer func() {
		if recover() == nil {
			t.Fatal("empty Name should panic")
		}
	}()
	Register(a)
}

func TestMissingRunPanics(t *testing.T) {
	reset()
	a := validApp("x")
	a.Run = nil
	defer func() {
		if recover() == nil {
			t.Fatal("nil Run should panic")
		}
	}()
	Register(a)
}
