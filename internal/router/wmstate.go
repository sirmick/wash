package router

import (
	"encoding/json"
	"sync"

	"github.com/sirmick/wash/internal/wire"
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
	mu         sync.Mutex
	windows    map[uint32]*wire.SessionWindow
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
func (s *windowSession) createWindow(windowID uint32, instanceID, element, icon, accent, title string, defaultW, defaultH uint32, isRoot bool) []wire.SessionPatch {
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
			w.Focused = false
			cp := *w
			patches = append(patches, wire.SessionPatch{Op: wire.SessionPatchWindowUpsert, Window: &cp})
		}
	}
	if !target.Focused || target.Z != s.nextZ {
		target.Focused = true
		s.nextZ++
		target.Z = s.nextZ
	}
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
		w.Focused = false
	}
	cp := *w
	patches = append(patches, wire.SessionPatch{Op: wire.SessionPatchWindowUpsert, Window: &cp})
	return patches
}
