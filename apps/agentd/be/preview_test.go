package agentd

import (
	"testing"
	"time"
)

func TestLiveTranscriptPreviewUsesTwoRecentConversationLines(t *testing.T) {
	resetTranscripts()
	appendPrompt("acp:preview", "old question", t0)
	appendPrompt("acp:preview", "new question", t0.Add(time.Second))
	appendEvent("acp:preview", Event{Kind: EventMessage, Text: "latest answer"}, t0.Add(2*time.Second))

	got := liveTranscriptPreview("acp:preview", 2)
	if got != "new question\nlatest answer" {
		t.Fatalf("preview = %q, want two newest conversation lines", got)
	}
}

func TestPreviewPatchCoalescesToLatestBoundedValue(t *testing.T) {
	resetTranscripts()
	stopPreviewPatches()
	resetControllersForTest()
	controllerState.Lock()
	controllerState.managers["manager"] = struct{}{}
	controllerState.Unlock()
	oldDelay, oldPublish := previewDelay, previewPublish
	previewDelay = 5 * time.Millisecond
	result := make(chan previewPatch, 1)
	previewPublish = func(p previewPatch) { result <- p }
	t.Cleanup(func() {
		stopPreviewPatches()
		resetControllersForTest()
		previewDelay, previewPublish = oldDelay, oldPublish
	})

	appendPrompt("acp:preview", "question", t0)
	queuePreviewPatch("acp:preview")
	appendEvent("acp:preview", Event{Kind: EventMessage, Text: "answer"}, t0.Add(time.Second))
	queuePreviewPatch("acp:preview")

	select {
	case p := <-result:
		if len(p.Rows) != 1 || p.Rows[0].Key != "acp:preview" || p.Rows[0].Preview != "question\nanswer" {
			t.Fatalf("patch = %#v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for preview patch")
	}
}

// With no manager open nobody renders previews, so streaming must not
// start the patch timer at all.
func TestPreviewPatchIdleWithoutManager(t *testing.T) {
	stopPreviewPatches()
	resetControllersForTest()
	t.Cleanup(stopPreviewPatches)

	queuePreviewPatch("acp:preview")
	previewMu.Lock()
	armed := previewTimer != nil || len(previewPending) > 0
	previewMu.Unlock()
	if armed {
		t.Fatal("preview patch queued with no manager subscribed")
	}
}
