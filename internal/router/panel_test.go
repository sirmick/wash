package router

import (
	"testing"

	"github.com/sirmick/wash/internal/wire"
)

// TestPanelCatalog asserts panelCatalog surfaces exactly the enabled
// entries that declare a SettingsPanel — regardless of surface — and
// sorts them by (Order, Section).
func TestPanelCatalog(t *testing.T) {
	reg := NewRegistry()
	panel := func(section string, order int) *wire.SettingsPanel {
		return &wire.SettingsPanel{Section: section, Element: "wash-settings-panel-x", Order: order}
	}
	// Background service with a panel (the common case) — kept out of
	// the launcher catalog but must appear here.
	reg.RegisterEntry(&Entry{Path: "/x/display", Manifest: &Manifest{
		ID: "com.wash.display", Name: "Display", Version: "1", ProtocolVersion: ProtocolVersion,
		Surface: SurfaceBackground, Instancing: InstancingSingleton, SettingsPanel: panel("Display", 10),
	}})
	// Window app with a panel and a lower order — should sort first.
	reg.RegisterEntry(&Entry{Path: "/x/vscode", Manifest: &Manifest{
		ID: "com.wash.vscode", Name: "VS Code", Version: "1", ProtocolVersion: ProtocolVersion,
		Surface: SurfaceBackground, Instancing: InstancingSingleton, SettingsPanel: panel("Developer", 5),
	}})
	// App without a panel — excluded.
	reg.RegisterEntry(&Entry{Path: "/x/about", Manifest: &Manifest{
		ID: "com.wash.about", Name: "About", Version: "1", ProtocolVersion: ProtocolVersion,
		Surface: SurfaceWindow, Instancing: InstancingMulti, Element: "wash-app-about", Icon: "data:,",
	}})
	// Disabled entry with a panel — excluded (Enabled() is false).
	reg.RegisterEntry(&Entry{Path: "/x/broken", Reason: "bad bundle", Manifest: &Manifest{
		ID: "com.wash.broken", Name: "Broken", Version: "1", ProtocolVersion: ProtocolVersion,
		Surface: SurfaceBackground, Instancing: InstancingSingleton, SettingsPanel: panel("Broken", 1),
	}})

	r := NewRouter(Config{}, reg, func(string, ...any) {})
	got := r.panelCatalog()

	if len(got) != 2 {
		t.Fatalf("want 2 panels, got %d: %+v", len(got), got)
	}
	if got[0].AppID != "com.wash.vscode" || got[0].Section != "Developer" {
		t.Fatalf("order[0] should be vscode/Developer (lower Order), got %+v", got[0])
	}
	if got[1].AppID != "com.wash.display" {
		t.Fatalf("order[1] should be display, got %+v", got[1])
	}
}
