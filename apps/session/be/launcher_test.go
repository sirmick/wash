package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLauncherStore_NoteDedupesNewestFirstAndCaps(t *testing.T) {
	s := newLauncherStore("")
	s.exists = func(string) bool { return true }
	s.Note("/a", "com.wash.edit")
	s.Note("/b", "com.wash.edit")
	s.Note("/a", "com.wash.imageview") // re-open moves /a to the top, one entry
	snap := s.Snapshot()
	if len(snap.Recent) != 2 || snap.Recent[0].Path != "/a" || snap.Recent[1].Path != "/b" {
		t.Fatalf("recent = %+v", snap.Recent)
	}
	if snap.Recent[0].AppID != "com.wash.imageview" {
		t.Errorf("re-open should carry the latest handler, got %s", snap.Recent[0].AppID)
	}
	for i := 0; i < maxRecentPerApp+10; i++ {
		s.Note(fmt.Sprintf("/f%d", i), "x")
	}
	snap = s.Snapshot()
	count := map[string]int{}
	for _, e := range snap.Recent {
		count[e.AppID]++
	}
	if count["x"] != maxRecentPerApp {
		t.Errorf("cap: %d entries for app x, want %d", count["x"], maxRecentPerApp)
	}
	if snap.Recent[0].Path != fmt.Sprintf("/f%d", maxRecentPerApp+9) {
		t.Errorf("newest first: got %s", snap.Recent[0].Path)
	}
	// The cap is per app: a flood from one app keeps every other app's.
	if count["com.wash.edit"] != 1 || count["com.wash.imageview"] != 1 {
		t.Errorf("per-app cap evicted another app's entries: %v", count)
	}
}

// A station has no path. It dedupes by app and name, is never hidden as a
// "missing file", and is removable by name.
func TestLauncherStore_NameEntries(t *testing.T) {
	s := newLauncherStore("")
	s.exists = func(string) bool { return false } // every PATH is missing
	s.NoteName("Groove Salad", "com.wash.radio")
	s.NoteName("Drone Zone", "com.wash.radio")
	s.NoteName("Groove Salad", "com.wash.radio")
	s.NoteName("Groove Salad", "com.other") // same name, other app: distinct
	s.Note("/gone.txt", "com.wash.edit")
	snap := s.Snapshot()
	if len(snap.Recent) != 3 {
		t.Fatalf("recent = %+v, want 3 name entries and the missing file hidden", snap.Recent)
	}
	if snap.Recent[0].Name != "Groove Salad" || snap.Recent[0].AppID != "com.other" ||
		snap.Recent[1].Name != "Groove Salad" || snap.Recent[1].AppID != "com.wash.radio" ||
		snap.Recent[2].Name != "Drone Zone" {
		t.Errorf("order/dedupe = %+v", snap.Recent)
	}
	s.Remove("", "Groove Salad", "com.wash.radio")
	snap = s.Snapshot()
	if len(snap.Recent) != 2 || snap.Recent[0].AppID != "com.other" || snap.Recent[1].Name != "Drone Zone" {
		t.Errorf("remove by name = %+v", snap.Recent)
	}
	s.NoteName("", "com.wash.radio")
	s.NoteName("x", "")
	if got := len(s.Snapshot().Recent); got != 2 {
		t.Errorf("empty name or app should be ignored, have %d entries", got)
	}
}

func TestLauncherStore_SnapshotDropsMissingFilesButKeepsThemOnDisk(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "gone.txt")
	path := filepath.Join(dir, "state", "recent.json")
	s := newLauncherStore(path)
	s.Note(gone, "com.wash.edit")
	s.Note(keep, "com.wash.edit")
	snap := s.Snapshot()
	if len(snap.Recent) != 1 || snap.Recent[0].Path != keep {
		t.Fatalf("snapshot should hide the missing file: %+v", snap.Recent)
	}
	// Reload from disk: the missing entry is still there (read-side filter only).
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st launcherState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Recent) != 2 {
		t.Errorf("on disk: %d entries, want 2 (missing file kept)", len(st.Recent))
	}
	// And it comes back once the file exists again.
	if err := os.WriteFile(gone, []byte("back"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := newLauncherStore(path).Snapshot().Recent; len(got) != 2 {
		t.Errorf("after the file returns: %d entries, want 2", len(got))
	}
}

func TestLauncherStore_RemoveClearAndPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent.json")
	s := newLauncherStore(path)
	s.exists = func(string) bool { return true }
	s.Note("/a", "e")
	s.Note("/b", "e")
	s.Remove("/a", "", "")
	if got := s.Snapshot().Recent; len(got) != 1 || got[0].Path != "/b" {
		t.Fatalf("after remove: %+v", got)
	}
	s.SetPinned("com.wash.term", true)
	s.SetPinned("com.wash.fm", true)
	s.SetPinned("com.wash.term", true) // idempotent, keeps slot
	s.Clear()
	snap := s.Snapshot()
	if len(snap.Recent) != 0 {
		t.Errorf("clear left %d recent", len(snap.Recent))
	}
	if len(snap.Pinned) != 2 || snap.Pinned[0] != "com.wash.term" || snap.Pinned[1] != "com.wash.fm" {
		t.Errorf("pins after clear = %v (clear must not touch pins)", snap.Pinned)
	}
	s.SetPinned("com.wash.term", false)
	if got := s.Snapshot().Pinned; len(got) != 1 || got[0] != "com.wash.fm" {
		t.Errorf("unpin = %v", got)
	}
	// Persisted: a fresh store sees the same pins.
	r := newLauncherStore(path)
	if got := r.Snapshot().Pinned; len(got) != 1 || got[0] != "com.wash.fm" {
		t.Errorf("reloaded pins = %v", got)
	}
}

func TestLauncherStore_ClearWritesEmptyArraysNotNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent.json")
	s := newLauncherStore(path)
	s.Note("/a", "e")
	s.Clear()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["recent"]) != "[]" || string(m["pinned"]) != "[]" {
		t.Errorf("on disk after clear: recent=%s pinned=%s, want [] and []", m["recent"], m["pinned"])
	}
}

func TestLauncherStore_CorruptFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newLauncherStore(path)
	if snap := s.Snapshot(); len(snap.Recent) != 0 || len(snap.Pinned) != 0 {
		t.Errorf("corrupt file should yield empty state, got %+v", snap)
	}
	s.Note("/x", "e") // and it is writable again
	if _, err := os.Stat(path); err != nil {
		t.Errorf("rewrite after corrupt: %v", err)
	}
}

func TestNormalizeRecent_OrdersByTime(t *testing.T) {
	now := time.Now()
	in := []recentEntry{
		{Path: "/old", At: now.Add(-time.Hour)},
		{Path: "/new", At: now},
		{Path: "", At: now}, // empty path dropped
	}
	out := normalizeRecent(in)
	if len(out) != 2 || out[0].Path != "/new" || out[1].Path != "/old" {
		t.Errorf("normalize = %+v", out)
	}
}

func TestLauncherStatePath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	if got := launcherStatePath(); got != "/tmp/xdg-state/wash/recent.json" {
		t.Errorf("xdg: %s", got)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got := launcherStatePath(); got != "/home/u/.local/state/wash/recent.json" {
		t.Errorf("fallback: %s", got)
	}
}

// Two quick Radio clicks before the router answers tune to the second
// station, once — not both in turn.
func TestPlayQueueLatestWinsAndTakesOnce(t *testing.T) {
	q := &playQueue{names: map[string]string{}}
	q.set(RadioAppID, "one")
	q.set(RadioAppID, "two")
	if got := q.take(RadioAppID); got != "two" {
		t.Errorf("take = %q, want two", got)
	}
	if got := q.take(RadioAppID); got != "" {
		t.Errorf("second take = %q, want empty (an unrelated spawn result must not replay it)", got)
	}
}
