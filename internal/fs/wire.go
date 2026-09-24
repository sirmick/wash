// Reply payloads for fs-touching apps (wash-fm, wash-edit). Hosting
// them here keeps the wire shape consistent across both BEs.
//
// Kind and ID are NOT included — the sdk.Bus envelope owns those.
// A handler returns ListReply{Path: ..., Entries: ...} and the bus
// stamps {kind:"list_ok", id:<echoed>} on the way out.

package fs

import "encoding/json"

// Request payloads shared by wash-fm and wash-edit (FE → BE). The two BEs
// register identical handlers for these verbs; the shapes live here next to
// the matching *Reply types so they can't drift.

// ListReq / ReadReq / PathReq are single-path requests. PathReq backs every
// generic path verb (delete, create_file, create_dir, watch, unwatch).
type ListReq struct {
	Path string `json:"path"`
}

// ReadReq requests a file's contents.
type ReadReq struct {
	Path string `json:"path"`
}

// PathReq is a bare path argument shared by the path-only verbs.
type PathReq struct {
	Path string `json:"path"`
}

// WriteReq writes Content to Path.
type WriteReq struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// RenameReq renames From → To; Replace allows clobbering an existing dest.
type RenameReq struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Replace bool   `json:"replace"`
}

// ListReply is the BE → FE response payload for a list request.
type ListReply struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
	// Total is the directory's full entry count when Truncated (the FE
	// says "showing first N of Total"); omitted otherwise.
	Total int `json:"total,omitempty"`
}

// ReadReply is the BE → FE response payload for a read request.
// Content is empty when Binary is true (the FE shows a placeholder
// instead of trying to decode bytes).
type ReadReply struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	// Blocked names why Content must not be edited and written back:
	// "binary" (NUL bytes), "too_large" (past the caller's read cap, so
	// Content is only a prefix), "encoding" (not valid UTF-8 — the JSON
	// wire would replace bytes with U+FFFD and a save would persist the
	// replacements). Empty means the file is an ordinary editable text
	// file. Binary stays set alongside Blocked=="binary" for callers that
	// predate this field.
	Blocked string `json:"blocked,omitempty"`
}

// Read-only reasons for ReadReply.Blocked.
const (
	BlockedBinary   = "binary"
	BlockedTooLarge = "too_large"
	BlockedEncoding = "encoding"
)

// WriteReply is the BE → FE response payload for a write request.
type WriteReply struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// PathReply is the generic single-path success payload (delete_ok,
// create_file_ok, create_dir_ok, chmod_ok, chown_ok …).
type PathReply struct {
	Path string `json:"path"`
}

// RenameReply is the rename_ok payload. From/To are the canonical
// post-rename paths (after Confine + Clean).
type RenameReply struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// SymlinkReply is the symlink_ok payload. Target is the link's
// stored target string (not dereferenced); LinkPath is where the
// link was created.
type SymlinkReply struct {
	Target   string `json:"target"`
	LinkPath string `json:"link_path"`
}

// MarshalReply marshals a reply payload for callers that need raw
// JSON (e.g. test harness debug surfaces).
func MarshalReply(v any) ([]byte, error) {
	return json.Marshal(v)
}

// LooksBinary reports whether a byte slice appears to be binary content,
// using the heuristic wash-fm and wash-edit both need: a NUL byte in the
// sampled prefix means "don't render as text" (it sets ReadReply.Binary).
// Callers pass the chunk they already read.
func LooksBinary(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}
