package wire

import (
	"bytes"
	"testing"
)

func validPanelManifest() Manifest {
	return Manifest{
		ID:              "com.wash.display",
		Name:            "Wash Display",
		Version:         "0.8.0",
		ProtocolVersion: ProtocolVersion,
		Surface:         SurfaceBackground,
		Instancing:      InstancingSingleton,
		SettingsPanel: &SettingsPanel{
			Section: "Display",
			Element: "wash-settings-panel-display",
		},
	}
}

// TestProbeRoundTrip writes a framed probe with both a main and a panel
// bundle and reads it back, asserting the raw bytes survive verbatim and
// the header carries the frame descriptors in order.
func TestProbeRoundTrip(t *testing.T) {
	main := []byte("export const x = 1;\n// main bundle\x00\x01\xff")
	panel := []byte("customElements.define('wash-settings-panel-display', P);")

	var buf bytes.Buffer
	if err := WriteProbe(&buf, validPanelManifest(), []NamedBundle{
		{Kind: BundleMain, Bytes: main},
		{Kind: BundlePanel, Bytes: panel},
	}); err != nil {
		t.Fatalf("WriteProbe: %v", err)
	}

	hdr, bundles, err := ReadProbe(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadProbe: %v", err)
	}
	if hdr.Manifest.ID != "com.wash.display" {
		t.Fatalf("manifest id: %q", hdr.Manifest.ID)
	}
	if hdr.Manifest.SettingsPanel == nil || hdr.Manifest.SettingsPanel.Section != "Display" {
		t.Fatalf("settings_panel not round-tripped: %+v", hdr.Manifest.SettingsPanel)
	}
	if !bytes.Equal(bundles[BundleMain], main) {
		t.Fatalf("main bundle mismatch: %q", bundles[BundleMain])
	}
	if !bytes.Equal(bundles[BundlePanel], panel) {
		t.Fatalf("panel bundle mismatch: %q", bundles[BundlePanel])
	}
}

// TestProbeNoBundles is the background-service / CLI-helper case: a
// header line with no frames and no payload.
func TestProbeNoBundles(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteProbe(&buf, validPanelManifest(), nil); err != nil {
		t.Fatalf("WriteProbe: %v", err)
	}
	// Empty/nil bundles must be skipped — header line only.
	if got := bytes.Count(buf.Bytes(), []byte{'\n'}); got != 1 {
		t.Fatalf("want exactly one newline (header only), got %d in %q", got, buf.String())
	}
	hdr, bundles, err := ReadProbe(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadProbe: %v", err)
	}
	if len(hdr.Bundles) != 0 || len(bundles) != 0 {
		t.Fatalf("expected no bundles, got hdr=%v map=%v", hdr.Bundles, bundles)
	}
}

// TestProbeSkipsEmptyBundle ensures a zero-length bundle is dropped from
// the header (no frame, no payload) rather than emitting a len:0 frame.
func TestProbeSkipsEmptyBundle(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteProbe(&buf, validPanelManifest(), []NamedBundle{
		{Kind: BundleMain, Bytes: nil},
		{Kind: BundlePanel, Bytes: []byte("p")},
	}); err != nil {
		t.Fatalf("WriteProbe: %v", err)
	}
	hdr, bundles, err := ReadProbe(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadProbe: %v", err)
	}
	if len(hdr.Bundles) != 1 || hdr.Bundles[0].Kind != BundlePanel {
		t.Fatalf("expected only the panel frame, got %+v", hdr.Bundles)
	}
	if _, ok := bundles[BundleMain]; ok {
		t.Fatal("empty main bundle should be absent")
	}
}

// TestProbeTruncated rejects a payload shorter than the header declares.
func TestProbeTruncated(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteProbe(&buf, validPanelManifest(), []NamedBundle{
		{Kind: BundleMain, Bytes: []byte("0123456789")},
	}); err != nil {
		t.Fatalf("WriteProbe: %v", err)
	}
	// Lop off the last 3 payload bytes.
	chopped := buf.Bytes()[:buf.Len()-3]
	if _, _, err := ReadProbe(chopped); err == nil {
		t.Fatal("expected truncation error")
	}
}

// TestSettingsPanelValidation covers the new manifest constraints.
func TestSettingsPanelValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*SettingsPanel)
		ok   bool
	}{
		{"valid", func(*SettingsPanel) {}, true},
		{"empty section", func(p *SettingsPanel) { p.Section = "" }, false},
		{"bad element prefix", func(p *SettingsPanel) { p.Element = "wash-app-display" }, false},
		{"bare prefix only", func(p *SettingsPanel) { p.Element = "wash-settings-panel-" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validPanelManifest()
			tc.mut(m.SettingsPanel)
			err := ValidateManifest(&m)
			if tc.ok && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}
