package sessionsummary

import (
	"strings"
	"testing"

	"github.com/sirmick/wash/pkg/sdk"
)

// Every byte that gets past these bounds leaves the machine.
func TestPrepareSourcesBoundsWhatLeaves(t *testing.T) {
	if _, err := prepareSources(nil); err == nil {
		t.Fatal("an empty capture was accepted")
	}
	// A window the router could not observe is not sent as an empty one.
	if _, err := prepareSources([]source{{AppID: "com.wash.priv", Source: "none"}, {AppID: "com.wash.fm", Source: "app-state"}}); err == nil {
		t.Fatal("unobservable windows were accepted as content")
	}

	many := make([]source, maxWindows+5)
	for i := range many {
		many[i] = source{AppID: "com.wash.term", Source: "pty-tail", Content: "x"}
	}
	got, err := prepareSources(many)
	if err != nil || len(got) != maxWindows {
		t.Fatalf("windows=%d err=%v, want %d", len(got), err, maxWindows)
	}

	// One oversized window is cut to the per-window cap and SAYS it was.
	got, err = prepareSources([]source{{Source: "pty-tail", Content: strings.Repeat("x", maxContentBytes*2)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Content) != maxContentBytes || !got[0].Truncated {
		t.Fatalf("content=%d truncated=%v", len(got[0].Content), got[0].Truncated)
	}

	// Many merely-large windows are refused rather than silently trimmed:
	// the person asked for a summary of everything, and gets told instead.
	big := make([]source, 32)
	for i := range big {
		big[i] = source{Source: "app-state", Content: strings.Repeat("x", maxContentBytes)}
	}
	if _, err := prepareSources(big); err == nil {
		t.Fatal("a capture over the combined cap was accepted")
	} else if e, ok := err.(sdk.Err); !ok || e.Code != "too_large" {
		t.Fatalf("err=%v, want too_large", err)
	}
}
