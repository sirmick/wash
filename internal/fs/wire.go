// Reply payloads for fs-touching apps (wash-fm, wash-edit). Hosting
// them here keeps the wire shape consistent across both BEs.
//
// Kind and ID are NOT included — the sdk.Bus envelope owns those.
// A handler returns ListReply{Path: ..., Entries: ...} and the bus
// stamps {kind:"list_ok", id:<echoed>} on the way out.

package fs

import "encoding/json"

// ListReply is the BE → FE response payload for a list request.
type ListReply struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
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
}

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
