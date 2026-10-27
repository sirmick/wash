package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// TestClipboardSlotsAreSeparate: the selection is its own slot — setting it
// leaves the clipboard alone, other shells hear about it with its slot, and
// app BEs (none reads the selection) are not told.
func TestClipboardSlotsAreSeparate(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	setter, _, c1 := newTestShellSession(t)
	setter.router = r
	defer c1()
	other, fe, c2 := newTestShellSession(t)
	other.router = r
	defer c2()
	r.registerShell(setter)
	r.registerShell(other)

	got := make(chan wire.ShellClipboardChanged, 4)
	go func() {
		for {
			f, err := fe.ReadFrame()
			if err != nil {
				return
			}
			var m wire.ShellClipboardChanged
			if f.Channel == ChannelControl && json.Unmarshal(f.Payload, &m) == nil && m.T == wire.TShellClipboardChanged {
				got <- m
			}
		}
	}()

	if err := setter.handleShellClipboardSet(wire.ShellClipboardSet{Mime: "text/plain", Text: "copied", Source: "Terminal"}); err != nil {
		t.Fatal(err)
	}
	if err := setter.handleShellClipboardSet(wire.ShellClipboardSet{Slot: wire.SlotSelection, Mime: "text/plain", Text: "highlighted", Source: "Editor"}); err != nil {
		t.Fatal(err)
	}
	if e := r.clipboard.getSlot(""); string(e.data) != "copied" || e.source != "Terminal" || e.at == 0 {
		t.Errorf("clipboard = %q from %q at %d, want copied from Terminal, stamped", e.data, e.source, e.at)
	}
	if e := r.clipboard.getSlot(wire.SlotSelection); string(e.data) != "highlighted" {
		t.Errorf("selection = %q, want highlighted", e.data)
	}
	for _, want := range []struct{ slot, text string }{{"", "copied"}, {wire.SlotSelection, "highlighted"}} {
		select {
		case m := <-got:
			if m.Slot != want.slot || m.Text != want.text {
				t.Errorf("changed = slot %q text %q, want slot %q text %q", m.Slot, m.Text, want.slot, want.text)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("other shell never heard of the %q slot", want.slot)
		}
	}

	// A slot that doesn't exist is refused rather than created.
	_ = setter.handleShellClipboardSet(wire.ShellClipboardSet{Slot: "bogus", Mime: "text/plain", Text: "x"})
	if e := r.clipboard.getSlot("bogus"); e.at != 0 {
		t.Error("an unknown slot was stored")
	}
}

// TestSavePasteImage: a pasted image lands as a private file named by
// time, a non-image is refused, and only the newest pasteKeep stay.
func TestSavePasteImage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "paste")
	png := []byte("\x89PNG\r\n\x1a\nfake")
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	path, err := savePasteImage(dir, "image/png", png, base)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) != ".png" || filepath.Dir(path) != dir {
		t.Errorf("path = %s, want a .png in %s", path, dir)
	}
	if b, _ := os.ReadFile(path); string(b) != string(png) {
		t.Error("saved bytes differ")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	if _, err := savePasteImage(dir, "text/plain", []byte("x"), base); err == nil {
		t.Error("a text paste was saved as an image")
	}

	for i := 1; i <= pasteKeep+5; i++ {
		if _, err := savePasteImage(dir, "image/png", png, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	names, _ := filepath.Glob(filepath.Join(dir, "paste-*"))
	if len(names) != pasteKeep {
		t.Fatalf("%d files kept, want %d", len(names), pasteKeep)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the oldest paste was not pruned")
	}
}
