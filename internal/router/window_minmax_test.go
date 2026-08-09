package router

import (
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// TestCreateWindowThreadsMinMax is the regression for the REVIEW-X11-WAYLAND
// min/max gap (router half): a window's client size hints must reach the
// SessionWindow the shell renders from, so the FE resize can clamp to them.
func TestCreateWindowThreadsMinMax(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	patches := r.winSession.createWindow(
		5, "i-1", "wash-app-x", "", "", "Title",
		400, 300, /* default w/h */
		200, 150, /* min w/h */
		800, 600, /* max w/h */
		false, false)

	var w *wire.SessionWindow
	for _, p := range patches {
		if p.Op == wire.SessionPatchWindowUpsert && p.Window != nil {
			w = p.Window
		}
	}
	if w == nil {
		t.Fatal("no window upsert patch")
	}
	if w.MinW != 200 || w.MinH != 150 || w.MaxW != 800 || w.MaxH != 600 {
		t.Fatalf("size hints dropped: got min=%dx%d max=%dx%d, want min=200x150 max=800x600",
			w.MinW, w.MinH, w.MaxW, w.MaxH)
	}
}
