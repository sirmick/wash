package agentd

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Running previews are manager context, not roster state. Streamed replies
// update them at human-readable cadence as tiny Bulk patches, preserving the
// one-backing-store-per-controller rule and avoiding full-state fanout.
const previewPatchInterval = 500 * time.Millisecond

type previewPatch struct {
	Kind string            `json:"kind"`
	Rows []previewPatchRow `json:"rows"`
}

type previewPatchRow struct {
	Key     string `json:"key"`
	Preview string `json:"preview,omitempty"`
}

var (
	previewMu      sync.Mutex
	previewPending = map[string]struct{}{}
	previewTimer   *time.Timer
	previewSending bool
	previewDelay   = previewPatchInterval
	previewPublish = publishManagerPreviews
)

func liveTranscriptPreview(key string, limit int) string {
	if limit <= 0 {
		return ""
	}
	transMu.Lock()
	defer transMu.Unlock()
	t := trans[key]
	if t == nil {
		return ""
	}
	lines := make([]string, 0, limit)
	for i := len(t.events) - 1; i >= 0 && len(lines) < limit; i-- {
		e := t.events[i]
		if e.Kind != EventUser && e.Kind != EventMessage {
			continue
		}
		if line := previewLine(e.Text); line != "" {
			lines = append(lines, line)
		}
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

func queuePreviewPatch(key string) {
	if key == "" {
		return
	}
	previewMu.Lock()
	previewPending[key] = struct{}{}
	if previewTimer == nil && !previewSending {
		previewTimer = time.AfterFunc(previewDelay, flushPreviewPatches)
	}
	previewMu.Unlock()
}

func flushPreviewPatches() {
	previewMu.Lock()
	if previewSending || len(previewPending) == 0 {
		previewTimer = nil
		previewMu.Unlock()
		return
	}
	keys := make([]string, 0, len(previewPending))
	for key := range previewPending {
		keys = append(keys, key)
	}
	previewPending = map[string]struct{}{}
	previewTimer = nil
	previewSending = true
	previewMu.Unlock()

	sort.Strings(keys)
	rows := make([]previewPatchRow, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, previewPatchRow{Key: key, Preview: liveTranscriptPreview(key, 2)})
	}
	previewPublish(previewPatch{Kind: "preview_patch", Rows: rows})

	previewMu.Lock()
	previewSending = false
	if len(previewPending) > 0 && previewTimer == nil {
		previewTimer = time.AfterFunc(previewDelay, flushPreviewPatches)
	}
	previewMu.Unlock()
}

func stopPreviewPatches() {
	previewMu.Lock()
	if previewTimer != nil {
		previewTimer.Stop()
	}
	previewTimer = nil
	previewPending = map[string]struct{}{}
	previewMu.Unlock()
}
