package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/wire"
)

// envelopedManifest wraps a bare manifest JSON in the framed probe
// header line (no bundles) so the validation tests can drive
// ParseProbe — the production probe-output parser — directly. The
// fixture JSON is compacted first (production emits compact json.Marshal
// output) so the header is a single line; the trailing newline is what
// ReadProbe splits the header on.
func envelopedManifest(manifestJSON string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(manifestJSON)); err != nil {
		panic("envelopedManifest: compact: " + err.Error())
	}
	return `{"manifest":` + buf.String() + "}\n"
}

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
	m, _, _, err := ParseProbe([]byte(envelopedManifest(validManifestJSON())))
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
			m, _, _, err := ParseProbe([]byte(envelopedManifest(validManifestJSON())))
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
	if _, _, _, err := ParseProbe([]byte("not json\n")); err == nil {
		t.Fatal("expected parse error")
	}
}

// TestValidateBackgroundSurface confirms surface=background validates
// even without an element or icon — services have no FE so requiring
// them would be wrong. Other manifest fields still enforced normally.
func TestValidateBackgroundSurface(t *testing.T) {
	m := &Manifest{
		ID:              "com.wash.notify",
		Name:            "Notifications",
		Version:         "0.9.0",
		ProtocolVersion: ProtocolVersion,
		Surface:         SurfaceBackground,
		Instancing:      InstancingSingleton,
	}
	if err := wire.ValidateManifest(m); err != nil {
		t.Fatalf("background manifest must validate without element/icon: %v", err)
	}
	// A background manifest with a junk element should still be
	// accepted — the field is ignored on this surface.
	m.Element = "not-prefixed"
	if err := wire.ValidateManifest(m); err != nil {
		t.Fatalf("background manifest should ignore element prefix: %v", err)
	}
	// Empty icon is fine on background.
	m.Icon = ""
	if err := wire.ValidateManifest(m); err != nil {
		t.Fatalf("background manifest should accept empty icon: %v", err)
	}
	// Bad ID still rejected — surface doesn't excuse it.
	m.ID = "BadID"
	if err := wire.ValidateManifest(m); err == nil {
		t.Fatal("background surface must not bypass id validation")
	}
}

func TestHasCapability(t *testing.T) {
	m, _, _, err := ParseProbe([]byte(envelopedManifest(validManifestJSON())))
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
