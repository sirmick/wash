package router

import (
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// A tagged move is echoed as the window's GeomTok, and a tagged move the
// router will not apply (a no-op, a non-normal window) still answers — the
// shell is holding its own geometry until the token comes back.
func TestMoveEchoesGeomTok(t *testing.T) {
	s := newTestWindows(t)
	addWindow(s, 1)

	got := s.move(1, 10, 20, 7)
	if len(got) != 1 || got[0].Window.X != 10 || got[0].Window.Y != 20 || got[0].Window.GeomTok != 7 {
		t.Fatalf("tagged move = %+v, want upsert (10,20) tok=7", got)
	}
	// Untagged no-op: silent, as before.
	if got := s.move(1, 10, 20, 0); got != nil {
		t.Fatalf("untagged no-op move produced patches: %+v", got)
	}
	// Tagged no-op: still echoed, so the shell's pending commit clears.
	got = s.move(1, 10, 20, 8)
	if len(got) != 1 || got[0].Window.GeomTok != 8 {
		t.Fatalf("tagged no-op move = %+v, want an echo with tok=8", got)
	}
	// A focus patch carries the last token, not a fresh one: the shell
	// tells "confirming my move" from "something else changed" by it.
	if got := s.focus(1); len(got) != 1 || got[0].Window.GeomTok != 8 {
		t.Fatalf("focus after move = %+v, want tok=8 carried", got)
	}
	// Refused (maximized) but tagged: echo the router's truth so the
	// shell reverts its optimistic move.
	s.setState(1, wire.WindowStateMaximized)
	got = s.move(1, 99, 99, 9)
	if len(got) != 1 || got[0].Window.X != 10 || got[0].Window.GeomTok != 9 {
		t.Fatalf("tagged move on maximized = %+v, want echo of (10,20) tok=9", got)
	}
	if got := s.resize(1, 300, 200, 11); len(got) != 1 || got[0].Window.W != 0 || got[0].Window.GeomTok != 11 {
		t.Fatalf("tagged resize on maximized = %+v, want echo with tok=11", got)
	}
}
