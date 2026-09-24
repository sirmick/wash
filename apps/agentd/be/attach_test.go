package agentd

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b64 of n bytes, which is what an image attachment is on the wire.
func b64n(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }

// A picked file becomes a resource_link — a REFERENCE — not its bytes. The
// distinction is the whole security story: the agent still has to read it
// through the fs capability, with the permission ask that entails, rather
// than receiving the contents because someone clicked Attach.
func TestAttachFileBecomesAResourceLink(t *testing.T) {
	withStateDir(t)
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &hosted{key: "acp:att", cwd: root}

	blocks := h.attachmentBlocks([]promptAttachment{{Type: "file", Path: path}})
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	b := blocks[0]
	if b.Type != "resource_link" {
		t.Errorf("type = %q, want resource_link", b.Type)
	}
	if !strings.HasPrefix(b.URI, "file://") || !strings.HasSuffix(b.URI, "/notes.txt") {
		t.Errorf("uri = %q", b.URI)
	}
	if b.Name != "notes.txt" {
		t.Errorf("name = %q", b.Name)
	}
	if b.Text != "" || b.Data != "" {
		t.Errorf("the file's bytes rode along: %+v", b)
	}

	// The transcript records what the agent was given, so a reloaded
	// window shows it too.
	evs := snapshot(h.key)
	if len(evs) != 1 || evs[0].Kind != EventTool || evs[0].Path != path {
		t.Errorf("transcript = %+v, want one row naming the file", evs)
	}
}

// A path outside the session cwd is refused here, not passed on for the fs
// layer to catch later: an attachment names a file the agent has not asked
// for, so the confinement has to hold at the point of attaching.
func TestAttachFileOutsideTheSessionRootIsRefused(t *testing.T) {
	withStateDir(t)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &hosted{key: "acp:att2", cwd: root}

	if blocks := h.attachmentBlocks([]promptAttachment{{Type: "file", Path: outside}}); len(blocks) != 0 {
		t.Fatalf("a file outside the root was attached: %+v", blocks)
	}
	// Refused out loud: a silent drop is indistinguishable from a bug.
	evs := snapshot(h.key)
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "outside this session's folder") {
		t.Errorf("no note explaining the refusal: %+v", evs)
	}
}

// An image travels by value — the bytes came from a clipboard and exist
// nowhere on disk — under a cap, and lands in the transcript so the
// conversation shows what was sent.
func TestAttachImageIsSentInlineAndShown(t *testing.T) {
	withStateDir(t)
	h := &hosted{key: "acp:att3", cwd: t.TempDir()}
	data := b64n(1024)

	blocks := h.attachmentBlocks([]promptAttachment{{Type: "image", Mime: "image/png", Data: data}})
	if len(blocks) != 1 || blocks[0].Type != "image" || blocks[0].Data != data || blocks[0].MimeType != "image/png" {
		t.Fatalf("blocks = %+v", blocks)
	}
	evs := snapshot(h.key)
	if len(evs) != 1 || evs[0].Kind != EventImage || evs[0].Text != data {
		t.Errorf("transcript = %+v, want the image", evs)
	}
}

func TestAttachRefusesOversizeAndUnknownKinds(t *testing.T) {
	withStateDir(t)
	h := &hosted{key: "acp:att4", cwd: t.TempDir()}

	blocks := h.attachmentBlocks([]promptAttachment{
		{Type: "image", Mime: "image/png", Data: b64n(maxAttachImageBytes + 1)},
		{Type: "image", Mime: "text/plain", Data: b64n(16)},
		{Type: "video", Path: "/x"},
	})
	if len(blocks) != 0 {
		t.Fatalf("blocks = %+v, want none", blocks)
	}
	evs := snapshot(h.key)
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "paste limit") {
		t.Errorf("the oversize paste was dropped without saying so: %+v", evs)
	}
}

// A file:// URI survives a path a naive concatenation would mangle.
func TestFileURIEscapes(t *testing.T) {
	if got := fileURI("/tmp/my notes/a#b.txt"); got != "file:///tmp/my%20notes/a%23b.txt" {
		t.Errorf("fileURI = %q", got)
	}
}
