package sdk

import (
	"testing"

	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// pickerConn returns a minimally-wired Conn whose SendAppMsg replies
// land on the returned transport end, plus that end for reading them.
// dispatchPicker only touches the transport (via pickerReply), so no
// handshake or dispatch loop is needed.
func pickerConn(t *testing.T) (*Conn, wire.FrameTransport) {
	t.Helper()
	pp := wiretest.NewPipePair()
	t.Cleanup(pp.Close)
	return &Conn{transport: pp.EndA()}, pp.EndB()
}

// pickerList drives one fs.list through dispatchPicker and returns
// the decoded reply payload.
func pickerList(t *testing.T, fsa *wfs.FS, path string) map[string]any {
	t.Helper()
	c, other := pickerConn(t)
	handled := dispatchPicker(c, fsa, nil, map[string]any{
		"kind": "fs.list", "path": path, "id": "t1",
	})
	if !handled {
		t.Fatalf("dispatchPicker did not handle fs.list %q", path)
	}
	m, ok := readEvt(t, other).(wire.EvtAppMsg)
	if !ok {
		t.Fatalf("expected EvtAppMsg reply, got %T", m)
	}
	return decodeAppMsgData(t, m.Data)
}

// Regression for issue #17: an unconfined picker's fs.list of "/"
// used to be silently rewritten to $HOME, so pressing Up at /home
// bounced the user back into their home directory — the picker could
// never climb above it. An explicit "/" must list the real root.
func TestPickerListSlashListsFilesystemRoot(t *testing.T) {
	data := pickerList(t, wfs.New(""), "/")
	if data["kind"] != "fs.list_ok" {
		t.Fatalf("fs.list / replied %v (msg=%v), want fs.list_ok", data["kind"], data["msg"])
	}
	if got := data["path"]; got != "/" {
		t.Fatalf("fs.list / listed %v — '/' must be honored literally, not rewritten", got)
	}
}

// The empty path is the picker's "default start" request: on an
// unconfined host it resolves to the user's home.
func TestPickerListEmptyPathLandsAtDefaultStart(t *testing.T) {
	data := pickerList(t, wfs.New(""), "")
	if data["kind"] != "fs.list_ok" {
		t.Fatalf("fs.list \"\" replied %v (msg=%v), want fs.list_ok", data["kind"], data["msg"])
	}
	if got, want := data["path"], wfs.DefaultStart(); got != want {
		t.Fatalf("fs.list \"\" listed %v, want default start %q", got, want)
	}
}

// On a confined host the empty path resolves to the sandbox root.
func TestPickerListEmptyPathLandsAtSandboxRoot(t *testing.T) {
	root := t.TempDir()
	data := pickerList(t, wfs.New(root), "")
	if data["kind"] != "fs.list_ok" {
		t.Fatalf("fs.list \"\" replied %v (msg=%v), want fs.list_ok", data["kind"], data["msg"])
	}
	if got := data["path"]; got != root {
		t.Fatalf("fs.list \"\" listed %v, want sandbox root %q", got, root)
	}
}

// Confined hosts still reject "/" with outside_root — the FE's
// recovery path (fs.root → re-list) depends on that error code.
func TestPickerListSlashOutsideSandboxErrors(t *testing.T) {
	data := pickerList(t, wfs.New(t.TempDir()), "/")
	if data["kind"] != "fs.list_err" {
		t.Fatalf("fs.list / in a sandbox replied %v, want fs.list_err", data["kind"])
	}
	if got := data["code"]; got != "outside_root" {
		t.Fatalf("fs.list / in a sandbox error code %v, want outside_root", got)
	}
}
