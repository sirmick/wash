package router

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

func dispatchCtrl(t *testing.T, s *ShellSession, msg any) {
	t.Helper()
	payload, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.dispatch(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: payload}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

// TestShellStateSetStoresAndBroadcasts: a shell.state_set lands in the
// session's app_state map (so a reconnecting shell's snapshot carries it)
// and reaches every OTHER shell as an app_state patch — that is how the
// window manager's cell layouts stay the same in every tab
// (docs/WM_LAYOUT.md §6).
func TestShellStateSetStoresAndBroadcasts(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	s1, _, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	s2, fe2, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()
	r.registerShell(s1)
	r.registerShell(s2)

	got := make(chan json.RawMessage, 1)
	go func() {
		for {
			f, err := fe2.ReadFrame()
			if err != nil {
				return
			}
			if f.Channel != ChannelControl {
				continue
			}
			var p wire.ShellSessionPatch
			if json.Unmarshal(f.Payload, &p) != nil || p.T != wire.TShellSessionPatch {
				continue
			}
			for _, op := range p.Patches {
				if op.Op == wire.SessionPatchAppState && op.InstanceID == "shell:layout:1,0" {
					got <- op.State
					return
				}
			}
		}
	}()

	blob := json.RawMessage(`{"shape":"r2","slots":[]}`)
	dispatchCtrl(t, s1, wire.NewShellStateSet("shell:layout:1,0", blob))

	select {
	case st := <-got:
		if string(st) != string(blob) {
			t.Errorf("patch state = %s, want %s", st, blob)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the other shell never received the app_state patch")
	}
	_, appState := r.winSession.snapshot()
	if string(appState["shell:layout:1,0"]) != string(blob) {
		t.Errorf("snapshot app_state = %s, want %s", appState["shell:layout:1,0"], blob)
	}

	// null clears it.
	dispatchCtrl(t, s1, wire.NewShellStateSet("shell:layout:1,0", json.RawMessage(`null`)))
	if _, appState = r.winSession.snapshot(); appState["shell:layout:1,0"] != nil {
		t.Errorf("after null, snapshot still has %s", appState["shell:layout:1,0"])
	}
}

// TestShellStateSetRefusesAppKeys: the reserved prefix is what keeps a
// shell from overwriting an app instance's saved state.
func TestShellStateSetRefusesAppKeys(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	s, _, c := newTestShellSession(t)
	s.router = r
	defer c()
	r.registerShell(s)

	r.broadcastPatches(r.winSession.setAppState("inst-1", json.RawMessage(`{"path":"/etc"}`)))
	dispatchCtrl(t, s, wire.NewShellStateSet("inst-1", json.RawMessage(`{"x":1}`)))
	dispatchCtrl(t, s, wire.NewShellStateSet("shell:", json.RawMessage(`{"x":1}`)))

	_, appState := r.winSession.snapshot()
	if string(appState["inst-1"]) != `{"path":"/etc"}` {
		t.Errorf("app blob overwritten: %s", appState["inst-1"])
	}
	if _, ok := appState["shell:"]; ok {
		t.Error("bare prefix key was stored")
	}
}

// TestSnapshotNamesShippedPacksOnlyWhenTrimmed: a size-trimmed build (the
// in-browser VM) tells the shell which theme packs it carries, so Settings
// never offers one whose wallpaper is missing. A full build says nothing —
// the field is omitted and the snapshot is exactly what it was.
func TestSnapshotNamesShippedPacksOnlyWhenTrimmed(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	full := r.shellSnapshot()
	if raw, _ := json.Marshal(full); strings.Contains(string(raw), `"packs"`) {
		t.Errorf("full build's snapshot carries packs: %s", raw)
	}
	r.SetShellPacks([]string{"midnight", "copland"})
	got := r.shellSnapshot()
	if strings.Join(got.Packs, ",") != "midnight,copland" {
		t.Errorf("snapshot packs = %v", got.Packs)
	}
}
