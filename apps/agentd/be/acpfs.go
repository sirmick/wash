// ACP's filesystem capability (fs/read_text_file, fs/write_text_file).
//
// Advertising it changes who touches the disk. Without it an agent opens
// files with its own I/O: it reads whatever is on disk, writes behind wash's
// back, and the desktop finds out later — if at all. With it, every file the
// agent reads or writes comes through wash:
//
//   - **Confined.** Paths resolve through internal/fs against the session's
//     cwd, so an agent working in one project cannot read your ssh key by
//     asking nicely. The agent is not trusted to stay inside the folder it
//     was given; it is held there.
//   - **Watched.** A write goes through the same fs layer the desktop
//     watches, so wash-fm and wash-edit see the change as it happens rather
//     than on the next manual refresh.
//   - **Auditable.** One place logs what the agent touched.
//
// What it does NOT yet do is serve unsaved editor buffers — an agent asking
// for a file you have modified in wash-edit still gets the bytes on disk.
// That needs the editor to publish its dirty buffers to agentd, and is the
// piece that makes an agent tab inside the editor genuinely coherent
// (docs/AGENT_TABS.md).

package agentd

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirmick/wash/internal/acp"
	wfs "github.com/sirmick/wash/internal/fs"
)

// maxAgentReadBytes caps a single fs/read_text_file. An agent asking for a
// 2GB log must get an error, not take the desktop down with it.
const maxAgentReadBytes = 8 << 20 // 8 MiB

// maxAgentWriteBytes caps a single fs/write_text_file for the same reason.
const maxAgentWriteBytes = 8 << 20

// fsFor builds the confined filesystem for this session's PRIMARY root:
// the cwd the user chose when starting the agent. Extra roots (roots.go)
// are resolved by h.confine, so anything that must honour all of them
// goes through that and hands the resolved absolute path to a wfs rooted
// where it landed — see writerFor.
func (h *hosted) fsFor() *wfs.FS { return wfs.New(h.cwd) }

// writerFor returns a filesystem that will accept `abs`, which h.confine
// has already established is inside one of this session's roots (or was
// approved by the person). Rooted at the file's own directory: the check
// has happened, and re-deriving which root matched is a second chance to
// get it wrong.
func writerFor(abs string) *wfs.FS { return wfs.New(filepath.Dir(abs)) }

// ReadTextFile answers fs/read_text_file. Line/Limit are the agent asking
// for a window into a large file; both are 1-based line numbers per ACP.
func (h *hosted) ReadTextFile(ctx context.Context, req acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	// Every root, then the person. An agent reaching outside the folders
	// it was given is worth SEEING — it used to be a line in a log the
	// person watching the window never read, which looked to them like
	// the agent being broken and to the agent like the file not existing.
	abs, err := h.confineOrAsk(ctx, "Read", req.Path)
	if err != nil {
		log.Printf("agentd: acp fs read REFUSED key=%s path=%q roots=%v: %v", h.key, req.Path, h.roots(), err)
		return acp.ReadTextFileResponse{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	if st.IsDir() {
		return acp.ReadTextFileResponse{}, fmt.Errorf("%s is a directory", req.Path)
	}
	if st.Size() > maxAgentReadBytes {
		return acp.ReadTextFileResponse{}, fmt.Errorf("%s is %d bytes, over the %d-byte read limit", req.Path, st.Size(), maxAgentReadBytes)
	}
	text, err := os.ReadFile(abs)
	if err != nil {
		log.Printf("agentd: acp fs read key=%s path=%q: %v", h.key, req.Path, err)
		return acp.ReadTextFileResponse{}, err
	}
	out := sliceLines(string(text), req.Line, req.Limit)
	log.Printf("agentd: acp fs read key=%s path=%q bytes=%d", h.key, req.Path, len(out))
	return acp.ReadTextFileResponse{Content: out}, nil
}

// WriteTextFile answers fs/write_text_file.
func (h *hosted) WriteTextFile(ctx context.Context, req acp.WriteTextFileRequest) error {
	if len(req.Content) > maxAgentWriteBytes {
		return fmt.Errorf("write of %d bytes exceeds the %d-byte limit", len(req.Content), maxAgentWriteBytes)
	}
	target, err := h.confineOrAsk(ctx, "Write", req.Path)
	if err != nil {
		log.Printf("agentd: acp fs write REFUSED key=%s path=%q roots=%v: %v", h.key, req.Path, h.roots(), err)
		return err
	}
	abs, n, err := writerFor(target).Write(target, []byte(req.Content), maxAgentWriteBytes)
	if err != nil {
		log.Printf("agentd: acp fs write FAILED key=%s path=%q: %v", h.key, target, err)
		return err
	}
	log.Printf("agentd: acp fs write key=%s path=%s bytes=%d", h.key, abs, n)
	return nil
}

// sliceLines applies ACP's optional line window. line is 1-based and
// inclusive; limit counts lines, not bytes. Out-of-range asks return what
// exists rather than an error — the agent is probing, not violating.
func sliceLines(text string, line, limit *int) string {
	if line == nil && limit == nil {
		return text
	}
	lines := strings.Split(text, "\n")
	start := 0
	if line != nil && *line > 1 {
		start = *line - 1
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit != nil && *limit >= 0 && start+*limit < end {
		end = start + *limit
	}
	return strings.Join(lines[start:end], "\n")
}
