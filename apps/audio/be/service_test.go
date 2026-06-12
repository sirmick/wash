package audio

import "testing"

func statusOf(s *State, id string) string {
	for i := range s.Sources {
		if s.Sources[i].ID == id {
			return s.Sources[i].Status
		}
	}
	return "<missing>"
}

// A producer going `playing` becomes active and pauses every other
// currently-playing source (single-play exclusivity, docs/AUDIO.md §3).
func TestApplyReportExclusivity(t *testing.T) {
	s := &State{Sources: []Source{
		{ID: "b", Status: "playing"},
		{ID: "a", Status: "stopped"},
	}}
	pause := applyReport(s, "a", "com.wash.music", reportReq{Status: "playing", Title: "Track"})

	if s.ActiveID != "a" {
		t.Fatalf("ActiveID = %q, want a", s.ActiveID)
	}
	if len(pause) != 1 || pause[0] != "b" {
		t.Fatalf("pause = %v, want [b]", pause)
	}
	if got := statusOf(s, "a"); got != "playing" {
		t.Fatalf("a status = %q, want playing", got)
	}
	if got := statusOf(s, "b"); got != "paused" {
		t.Fatalf("b status = %q, want paused", got)
	}
	if got := statusOf(s, "a"); s.Sources[0].ID != "b" || got != "playing" {
		// order preserved (front stays b); a updated in place
		t.Fatalf("unexpected order/state: %+v", s.Sources)
	}
}

// ActiveID is kept across pause so the sidebar still targets the source
// you'd resume.
func TestApplyReportActiveStickyOnPause(t *testing.T) {
	s := &State{Sources: []Source{{ID: "a", Status: "stopped"}}}
	applyReport(s, "a", "com.wash.music", reportReq{Status: "playing"})
	if s.ActiveID != "a" {
		t.Fatalf("ActiveID = %q, want a", s.ActiveID)
	}
	applyReport(s, "a", "com.wash.music", reportReq{Status: "paused"})
	if s.ActiveID != "a" {
		t.Fatalf("ActiveID after pause = %q, want a (sticky)", s.ActiveID)
	}
}

// A report from an unknown producer (report-before-register race) adopts
// it as a source.
func TestApplyReportAdoptsUnknown(t *testing.T) {
	s := &State{}
	applyReport(s, "x", "com.wash.radio", reportReq{Status: "playing", Title: "Stn"})
	if len(s.Sources) != 1 || s.Sources[0].ID != "x" || s.Sources[0].Title != "Stn" {
		t.Fatalf("adopt failed: %+v", s.Sources)
	}
	if s.Sources[0].Kind != KindFEDecoded {
		t.Fatalf("kind = %q, want fe-decoded", s.Sources[0].Kind)
	}
}
