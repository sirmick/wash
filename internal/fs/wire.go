// Reply schema for fs-touching apps (wash-fm, wash-edit). These
// structs were duplicated across the two BEs; they live here so a
// new consumer doesn't grow a third copy that drifts.
//
// Every reply carries an optional ID echoing the requesting message's
// id field — when present, FE side code can correlate the answer
// with its in-flight request (the wash-launch msg --await-id path
// uses the same convention router-side).
//
// SendErr is the canonical "build and ship an *_err envelope" helper.
// Callers pass a kind ("list_err", "rename_err", …), the id to echo,
// the path the error is about, a short code, and the human message.

package fs

import "encoding/json"

// ListReply is the BE → FE response to a list request.
type ListReply struct {
	Kind      string  `json:"kind"`
	ID        string  `json:"id,omitempty"`
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// ReadReply is the BE → FE response to a read request. Content is
// empty when Binary is true (the FE shows a placeholder instead of
// trying to decode bytes).
type ReadReply struct {
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

// WriteReply is the BE → FE response to a write request.
type WriteReply struct {
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// PathReply is the generic single-path success envelope (delete_ok,
// create_file_ok, create_dir_ok, chmod_ok, chown_ok …). Apps that
// need extra fields define their own struct; the bulk of fm/edit
// mutations fit here.
type PathReply struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path"`
}

// RenameReply is the rename_ok envelope. From/To are the canonical
// post-rename paths (after Confine + Clean).
type RenameReply struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	From string `json:"from"`
	To   string `json:"to"`
}

// SymlinkReply is the symlink_ok envelope. Target is the link's
// stored target string (not dereferenced); LinkPath is where the
// link was created.
type SymlinkReply struct {
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`
	Target   string `json:"target"`
	LinkPath string `json:"link_path"`
}

// ErrReply is the *_err envelope every fs op uses on failure.
type ErrReply struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path,omitempty"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

// SendErr ships an ErrReply via the supplied send function. The
// send is typed as `func(any) error` so this stays decoupled from
// the SDK package (avoiding an internal/fs → internal/sdk import
// cycle). Apps pass `c.SendAppMsg` directly.
func SendErr(send func(any) error, kind, id, path, code, msg string) error {
	return send(ErrReply{Kind: kind, ID: id, Path: path, Code: code, Msg: msg})
}

// MarshalReply marshals a reply for callers that need raw JSON
// (e.g. the test app's debug surface). Most callers just hand the
// struct to SendAppMsg and let CBOR encode it.
func MarshalReply(v any) ([]byte, error) {
	return json.Marshal(v)
}
