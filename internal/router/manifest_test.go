package router

import (
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/wire"
)

func validManifestJSON() string {
	return `{
		"id":"com.wash.about",
		"name":"About wash",
		"version":"0.0.1",
		"protocol_version":1,
		"element":"wash-app-about",
		"surface":"window",
		"icon":"data:image/svg+xml,W",
		"instancing":"multi",
		"capabilities":[],
		"window":{"default_width":480,"default_height":320}
	}`
}

func TestParseValidManifest(t *testing.T) {
	m, err := ParseManifest([]byte(validManifestJSON()))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if m.ID != "com.wash.about" || m.Element != "wash-app-about" {
		t.Fatalf("parsed wrong: %+v", m)
	}
	if m.Window == nil || m.Window.DefaultWidth != 480 {
		t.Fatalf("window hints lost: %+v", m.Window)
	}
}

func TestValidateRejects(t *testing.T) {
	tweaks := []struct {
		name string
		want string
		mut  func(*Manifest)
	}{
		{"empty id", "invalid id", func(m *Manifest) { m.ID = "" }},
		{"bare id (no dot)", "invalid id", func(m *Manifest) { m.ID = "about" }},
		{"uppercase id", "invalid id", func(m *Manifest) { m.ID = "Com.wash.about" }},
		{"id starts with dash", "invalid id", func(m *Manifest) { m.ID = "-foo.bar" }},
		{"empty name", "name is empty", func(m *Manifest) { m.Name = "" }},
		{"empty version", "version is empty", func(m *Manifest) { m.Version = "" }},
		{"bad proto", "protocol_version", func(m *Manifest) { m.ProtocolVersion = 99 }},
		{"bad element prefix", "must start with", func(m *Manifest) { m.Element = "my-app" }},
		{"bad surface", "invalid surface", func(m *Manifest) { m.Surface = "panel" }},
		{"bad instancing", "invalid instancing", func(m *Manifest) { m.Instancing = "always" }},
		{"empty icon", "icon is empty", func(m *Manifest) { m.Icon = "" }},
		{"oversize icon", "icon is", func(m *Manifest) { m.Icon = strings.Repeat("x", MaxIconBytes+1) }},
	}
	for _, tc := range tweaks {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ParseManifest([]byte(validManifestJSON()))
			if err != nil {
				t.Fatalf("base manifest must parse: %v", err)
			}
			tc.mut(m)
			err = wire.ValidateManifest(m)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestParseRejectsNonJSON(t *testing.T) {
	if _, err := ParseManifest([]byte("not json")); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestHasCapability(t *testing.T) {
	m, err := ParseManifest([]byte(validManifestJSON()))
	if err != nil {
		t.Fatal(err)
	}
	m.Capabilities = []string{CapSpawn}
	if !m.HasCapability(CapSpawn) {
		t.Fatal("expected capability present")
	}
	if m.HasCapability("write") {
		t.Fatal("did not expect capability present")
	}
}
