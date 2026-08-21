package router

import (
	"encoding/json"
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// The window attention flag (docs/AGENT_UX.md N6). The invariant worth
// protecting is that the APP raises it and the DESKTOP settles it: an app
// cannot leave a taskbar pill pulsing at a window the human has looked at.

func newTestWindows(t *testing.T) *windowSession {
	t.Helper()
	return &windowSession{
		windows:  map[uint32]*wire.SessionWindow{},
		appState: map[string]json.RawMessage{},
	}
}

func addWindow(s *windowSession, id uint32) {
	s.windows[id] = &wire.SessionWindow{WindowID: id, InstanceID: "i", State: wire.WindowStateNormal}
}

func TestAttentionSetsAndPatches(t *testing.T) {
	s := newTestWindows(t)
	addWindow(s, 1)

	patches := s.setAttention(1, true)
	if len(patches) != 1 || patches[0].Window == nil || !patches[0].Window.Attention {
		t.Fatalf("setAttention(true) = %+v, want one upsert with attention set", patches)
	}
	// Idempotent: saying it twice is not news for the shell to render.
	if got := s.setAttention(1, true); got != nil {
		t.Fatalf("re-setting attention produced patches: %+v", got)
	}
	if got := s.setAttention(1, false); len(got) != 1 || got[0].Window.Attention {
		t.Fatalf("clearing attention = %+v, want one upsert with it unset", got)
	}
}

func TestAttentionIgnoredForUnknownWindow(t *testing.T) {
	s := newTestWindows(t)
	if got := s.setAttention(99, true); got != nil {
		t.Fatalf("attention on a window that does not exist = %+v, want nil", got)
	}
}

func TestAttentionOnTheFocusedWindowIsANoOp(t *testing.T) {
	// Pulsing at the window someone is typing in teaches them to ignore
	// the pulse.
	s := newTestWindows(t)
	addWindow(s, 1)
	s.focus(1)

	if got := s.setAttention(1, true); got != nil {
		t.Fatalf("attention on the focused window = %+v, want nil", got)
	}
	if s.windows[1].Attention {
		t.Fatal("focused window carries attention")
	}
}

func TestFocusClearsAttention(t *testing.T) {
	s := newTestWindows(t)
	addWindow(s, 1)
	addWindow(s, 2)
	s.focus(2) // 1 is unfocused, so it may be flagged

	if got := s.setAttention(1, true); len(got) != 1 {
		t.Fatalf("setAttention on the unfocused window = %+v, want a patch", got)
	}
	patches := s.focus(1)
	if s.windows[1].Attention {
		t.Fatal("attention survived focus: a window you are looking at is still asking for you")
	}
	// The clear must reach the shell, or the pill keeps pulsing.
	var sawTarget bool
	for _, p := range patches {
		if p.Window != nil && p.Window.WindowID == 1 {
			sawTarget = true
			if p.Window.Attention {
				t.Fatal("focus patch still carries attention")
			}
		}
	}
	if !sawTarget {
		t.Fatalf("no patch for the focused window: %+v", patches)
	}
}

func TestAttentionSurvivesMinimize(t *testing.T) {
	// A minimized window that needs you is exactly the case the pill is
	// for — it has no other way of saying so.
	s := newTestWindows(t)
	addWindow(s, 1)
	addWindow(s, 2)
	s.focus(2)
	s.setAttention(1, true)

	s.setState(1, wire.WindowStateMinimized)
	if !s.windows[1].Attention {
		t.Fatal("minimize cleared attention")
	}
}
