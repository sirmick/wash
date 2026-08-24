package router

import (
	"encoding/json"
	"sync"

	"github.com/sirmick/wash/pkg/wire"
)

// windowSession is the router's canonical view of WM state — one
// entry per live window. Shells are observers: on connect they get
// the full snapshot, then a stream of patches as state changes.
//
// All exported methods take the lock and return a copy of the
// patches to broadcast. The router's broadcastPatches helper sends
// them; keeping I/O outside the lock means a slow shell can't stall
// other mutations.
type windowSession struct {
	mu      sync.Mutex
	windows map[uint32]*wire.SessionWindow
	// attnWanted is who has ASKED for the human, which outlives whether
	// the ask is currently shown: a window that asks while focused shows
	// nothing, and must light the moment focus leaves. Cleared by focus
	// (the human looked) and by the app withdrawing the request.
	attnWanted map[uint32]bool
	appState   map[string]json.RawMessage // by instance_id; opaque to router
	nextZ      uint32
	nextOffset int32
}

// snapshot returns a stable, lock-released copy of the windows
// plus the saved app-state blobs. Shells use this to seed their
// view on (re)connect.
func (s *windowSession) snapshot() ([]wire.SessionWindow, map[string]json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]wire.SessionWindow, 0, len(s.windows))
	for _, w := range s.windows {
		out = append(out, *w)
	}
	var st map[string]json.RawMessage
	if len(s.appState) > 0 {
		st = make(map[string]json.RawMessage, len(s.appState))
		for k, v := range s.appState {
			st[k] = v
		}
	}
	return out, st
}

// setAppState replaces (or clears, when state is nil) the FE-state
// blob for instance_id and returns the patch to broadcast.
func (s *windowSession) setAppState(instanceID string, state json.RawMessage) []wire.SessionPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appState == nil {
		s.appState = make(map[string]json.RawMessage)
	}
	if state == nil {
		delete(s.appState, instanceID)
	} else {
		s.appState[instanceID] = state
	}
	return []wire.SessionPatch{{Op: wire.SessionPatchAppState, InstanceID: instanceID, State: state}}
}

// dropAppState removes the state for instance_id. Called when the
// instance exits — no patch is broadcast since the instance is
// also being torn down (the window.delete patch covers cleanup).
func (s *windowSession) dropAppState(instanceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.appState, instanceID)
}

// focusedWindowID returns the id of the focused window, or 0 if none.
// Used by handlers that need the pre-mutation focus to relay
// EvtWindowUnfocus to the loser.
func (s *windowSession) focusedWindowID() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, w := range s.windows {
		if w.Focused {
			return id
		}
	}
	return 0
}

// createWindow allocates the initial geometry for a fresh window and
// returns the upsert patch. The window is NOT auto-focused — the FE
// calls window.focus on mount, which goes through the normal focus
// path (router emits Evt events to the affected apps). This keeps
// one canonical focus-change codepath.
func (s *windowSession) createWindow(windowID uint32, instanceID, element, icon, accent, title string, defaultW, defaultH, minW, minH, maxW, maxH uint32, isRoot, chromeless bool) []wire.SessionPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.windows == nil {
		s.windows = make(map[uint32]*wire.SessionWindow)
	}
	if defaultW == 0 {
		defaultW = 480
	}
	if defaultH == 0 {
		defaultH = 320
	}
	// Cascade initial position. Roll over after a few steps so new
	// windows don't march off the desktop.
	x := int32(40) + s.nextOffset
	y := int32(40) + s.nextOffset
	s.nextOffset = (s.nextOffset + 24) % 200
	s.nextZ++
	w := &wire.SessionWindow{
		WindowID:   windowID,
		InstanceID: instanceID,
		Element:    element,
		Icon:       icon,
		Accent:     accent,
		Title:      title,
		X:          x,
		Y:          y,
		W:          defaultW,
		H:          defaultH,
		Z:          s.nextZ,
		State:      wire.WindowStateNormal,
		Focused:    false,
		IsRoot:     isRoot,
		Chromeless: chromeless,
		MinW:       minW,
		MinH:       minH,
		MaxW:       maxW,
		MaxH:       maxH,
	}
	s.windows[windowID] = w
	return []wire.SessionPatch{{Op: wire.SessionPatchWindowUpsert, Window: w}}
}

// destroyWindow removes a window. If the window doesn't exist, returns
// a nil patch.
func (s *windowSession) destroyWindow(windowID uint32) []wire.SessionPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.windows[windowID]; !ok {
		return nil
	}
	delete(s.windows, windowID)
	delete(s.attnWanted, windowID)
	return []wire.SessionPatch{{Op: wire.SessionPatchWindowDelete, WindowID: windowID}}
}

// setTitle updates a window's title and returns the upsert patch.
func (s *windowSession) setTitle(windowID uint32, title string) []wire.SessionPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.windows[windowID]
	if w == nil || w.Title == title {
		return nil
	}
	w.Title = title
	cp := *w
	return []wire.SessionPatch{{Op: wire.SessionPatchWindowUpsert, Window: &cp}}
}

// setAttention flags (or clears) "this window needs the human"
// (docs/AGENT_UX.md N6). Asking for attention on the window that already
// has focus shows nothing: the human is looking at it, and a pill that
// pulses at the window you are typing in teaches people to ignore the
// pulse.
//
// But the REQUEST is remembered, which the first cut of this got wrong.
// An agent that asks a question while you are watching it, and then you
// switch away, must still mark its pill — the question is unanswered and
// the pill is the standing version of the interrupt. Dropping the request
// on the floor because you happened to be looking at the moment it
// arrived meant the pill never lit in exactly the case it exists for.
// So: wanted is recorded always, shown only while unfocused, and cleared
// by focus (see focus()).
func (s *windowSession) setAttention(windowID uint32, on bool) []wire.SessionPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyAttention(windowID, on)
}

// applyAttention is setAttention's body, for callers already holding mu.
func (s *windowSession) applyAttention(windowID uint32, on bool) []wire.SessionPatch {
	w := s.windows[windowID]
	if w == nil {
		return nil
	}
	if on {
		// Lazily made, like s.windows and s.appState: winSession is a
		// zero-value struct field on Router, so writing a nil map here
		// would panic on the first request.
		if s.attnWanted == nil {
			s.attnWanted = map[uint32]bool{}
		}
		s.attnWanted[windowID] = true
	} else {
		delete(s.attnWanted, windowID)
	}
	// Shown only while the window is not the one being looked at.
	show := on && !w.Focused
	if w.Attention == show {
		return nil
	}
	w.Attention = show
	cp := *w
	return []wire.SessionPatch{{Op: wire.SessionPatchWindowUpsert, Window: &cp}}
}

// blurred takes focus away from a window and, if that window had asked
// for the human while it held focus, makes the ask visible now.
//
// Every path that removes focus goes through here — another window taking
// it, and minimize clearing it — because they are the same event from the
// window's point of view, and getting only one of them was the bug: a
// single Agent window that asked a question and was then minimized never
// lit its pill, since nothing else ever took focus.
//
// Caller holds mu and is responsible for emitting the patch.
func (s *windowSession) blurred(windowID uint32, w *wire.SessionWindow) {
	w.Focused = false
	if s.attnWanted[windowID] {
		w.Attention = true
	}
}

// move updates a window's position. State==maximized/minimized
// windows ignore moves (the FE doesn't let you drag them anyway).
func (s *windowSession) move(windowID uint32, x, y int32) []wire.SessionPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.windows[windowID]
	if w == nil || w.State != wire.WindowStateNormal {
		return nil
	}
	if w.X == x && w.Y == y {
		return nil
	}
	w.X = x
	w.Y = y
	cp := *w
	return []wire.SessionPatch{{Op: wire.SessionPatchWindowUpsert, Window: &cp}}
}

// resize updates a window's size.
func (s *windowSession) resize(windowID, width, height uint32) []wire.SessionPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.windows[windowID]
	if w == nil || w.State != wire.WindowStateNormal {
		return nil
	}
	if w.W == width && w.H == height {
		return nil
	}
	w.W = width
	w.H = height
	cp := *w
	return []wire.SessionPatch{{Op: wire.SessionPatchWindowUpsert, Window: &cp}}
}

// focus raises a window to the top of the z-stack and marks it
// focused, clearing Focused on whoever held it before. Returns
// patches for every window whose state actually changed.
func (s *windowSession) focus(windowID uint32) []wire.SessionPatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := s.windows[windowID]
	if target == nil {
		return nil
	}
	var patches []wire.SessionPatch
	for id, w := range s.windows {
		if id == windowID {
			continue
		}
		if w.Focused {
			s.blurred(id, w)
			cp := *w
			patches = append(patches, wire.SessionPatch{Op: wire.SessionPatchWindowUpsert, Window: &cp})
		}
	}
	if !target.Focused || target.Z != s.nextZ {
		target.Focused = true
		s.nextZ++
		target.Z = s.nextZ
	}
	// Looking at a window settles what it was asking for — the standing
	// request too, not just the mark, or the next time you switched away
	// it would light again for something you have already read. Clearing
	// here rather than in the app keeps "seen it" a fact about the
	// desktop: however the window came forward — taskbar, toast, alt-tab,
	// an app raising itself — the pulse stops (docs/AGENT_UX.md N6).
	target.Attention = false
	delete(s.attnWanted, windowID)
	cp := *target
	patches = append(patches, wire.SessionPatch{Op: wire.SessionPatchWindowUpsert, Window: &cp})
	return patches
}

// setState transitions a window between normal/minimized/maximized.
// Going OUT of normal saves the current frame as Restore*; going
// back to normal restores from Restore*. Side effects:
//   - minimize while focused clears focus
//   - restore from min/max raises (caller's job — focus() separately)
func (s *windowSession) setState(windowID uint32, target string) []wire.SessionPatch {
	switch target {
	case wire.WindowStateNormal, wire.WindowStateMinimized, wire.WindowStateMaximized:
	default:
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.windows[windowID]
	if w == nil || w.State == target {
		return nil
	}
	prev := w.State
	if prev == wire.WindowStateNormal && target != wire.WindowStateNormal {
		w.RestoreX, w.RestoreY = w.X, w.Y
		w.RestoreW, w.RestoreH = w.W, w.H
	}
	if target == wire.WindowStateNormal && prev != wire.WindowStateNormal {
		if w.RestoreW != 0 {
			w.X, w.Y = w.RestoreX, w.RestoreY
			w.W, w.H = w.RestoreW, w.RestoreH
		}
	}
	w.State = target
	patches := []wire.SessionPatch{}
	if target == wire.WindowStateMinimized && w.Focused {
		s.blurred(windowID, w)
	}
	cp := *w
	patches = append(patches, wire.SessionPatch{Op: wire.SessionPatchWindowUpsert, Window: &cp})
	return patches
}
