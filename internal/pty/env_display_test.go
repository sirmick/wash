package pty

import "testing"

// has reports whether env contains exactly key=val.
func has(env []string, key, val string) bool {
	want := key + "=" + val
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// TestWithWashEnvMapsDisplay covers the docs/DISPLAY_ENV.md §2b mapping:
// the router's WASH_*-namespaced display hints become the real DISPLAY /
// WAYLAND_DISPLAY / XDG_RUNTIME_DIR for a terminal's shell, so a GUI
// client typed at a wash prompt finds the compositor.
func TestWithWashEnvMapsDisplay(t *testing.T) {
	in := []string{
		"WASH_X_DISPLAY=:2",
		"WASH_WAYLAND_DISPLAY=wayland-0",
		"WASH_XDG_RUNTIME_DIR=/run/user/1000",
	}
	out := WithWashEnv(in)
	if !has(out, "DISPLAY", ":2") {
		t.Errorf("DISPLAY not mapped from WASH_X_DISPLAY: %v", out)
	}
	if !has(out, "WAYLAND_DISPLAY", "wayland-0") {
		t.Errorf("WAYLAND_DISPLAY not mapped: %v", out)
	}
	if !has(out, "XDG_RUNTIME_DIR", "/run/user/1000") {
		t.Errorf("XDG_RUNTIME_DIR not mapped: %v", out)
	}
}

// A user-exported real var must not be clobbered by the WASH_* hint.
func TestWithWashEnvDoesNotClobberExistingDisplay(t *testing.T) {
	in := []string{
		"WASH_X_DISPLAY=:2",
		"DISPLAY=:99",
	}
	out := WithWashEnv(in)
	if has(out, "DISPLAY", ":2") {
		t.Errorf("WASH_X_DISPLAY clobbered an explicit DISPLAY: %v", out)
	}
	if !has(out, "DISPLAY", ":99") {
		t.Errorf("explicit DISPLAY=:99 was lost: %v", out)
	}
}

// Absent WASH_* hints, no display vars are synthesized — a plain wash
// terminal stays display-free.
func TestWithWashEnvNoDisplayWhenUnset(t *testing.T) {
	out := WithWashEnv([]string{"HOME=/home/x"})
	for _, kv := range out {
		if kv[:min(len(kv), 8)] == "DISPLAY=" {
			t.Errorf("synthesized DISPLAY with no WASH_X_DISPLAY: %v", out)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
