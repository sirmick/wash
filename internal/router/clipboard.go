// Router-held clipboard: two slots, the clipboard and the selection
// (docs/CLIPBOARD.md). Each is a single (mime, data) entry plus where it
// came from and when. Apps reach the clipboard slot over the event
// channel (§9); shells reach both over the shell channel.

package router

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

type clipboardEntry struct {
	mime   string
	data   []byte
	source string
	at     int64 // unix ms; 0 = never set
}

type clipboardState struct {
	mu    sync.RWMutex
	slots map[string]clipboardEntry // "" = clipboard, wire.SlotSelection
}

func (c *clipboardState) setSlot(slot, mime string, data []byte, source string) clipboardEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slots == nil {
		c.slots = make(map[string]clipboardEntry)
	}
	// Copy so a later caller mutating their byte slice doesn't shift
	// our store underneath us.
	e := clipboardEntry{mime: mime, data: append([]byte(nil), data...), source: source, at: time.Now().UnixMilli()}
	c.slots[slot] = e
	return e
}

func (c *clipboardState) getSlot(slot string) clipboardEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e := c.slots[slot]
	e.data = append([]byte(nil), e.data...)
	return e
}

func (c *clipboardState) set(mime string, data []byte) { c.setSlot("", mime, data, "") }

func (c *clipboardState) get() (string, []byte) {
	e := c.getSlot("")
	return e.mime, e.data
}

// isTextMime is what rides the shell channel as Text rather than Data: text,
// and fm's files clipboard (a list of paths).
func isTextMime(mime string) bool {
	return mime == "" || strings.HasPrefix(mime, "text/") || mime == "application/x-wash-paths"
}

// slotOK accepts the two slots and nothing else, so a typo can't grow a
// third slot nobody reads.
func slotOK(slot string) bool { return slot == "" || slot == wire.SlotSelection }

// handleClipboardSet stores new content and broadcasts a "changed"
// event to every connected app except the one that set it, plus
// every attached shell (the FE-side consumers).
func (inst *AppInstance) handleClipboardSet(m wire.EvtClipboardSet) error {
	source := inst.AppID
	if e := inst.router.reg.ByID(inst.AppID); e != nil && e.Manifest.Name != "" {
		source = e.Manifest.Name
	}
	e := inst.router.clipboard.setSlot("", m.Mime, m.Data, source)
	inst.router.broadcastClipboardChanged(inst, m.Mime)
	inst.router.broadcastShellClipboardChanged(nil, "", e)
	return nil
}

// handleShellClipboardSet is the window.wash path: a browser shell
// wrote a slot. App BEs get the §9 notice for the clipboard only — none
// reads the selection; other shells get the shell-channel notice (the
// setter already knows).
func (s *ShellSession) handleShellClipboardSet(m wire.ShellClipboardSet) error {
	if !slotOK(m.Slot) {
		s.router.log("clipboard.set: unknown slot %q", m.Slot)
		return nil
	}
	data := m.Data
	if isTextMime(m.Mime) {
		data = []byte(m.Text)
	}
	e := s.router.clipboard.setSlot(m.Slot, m.Mime, data, m.Source)
	if m.Slot == "" {
		s.router.broadcastClipboardChanged(nil, m.Mime)
	}
	s.router.broadcastShellClipboardChanged(s, m.Slot, e)
	return nil
}

// handleShellClipboardGet replies with a slot's current content on the
// requesting shell's control channel.
func (s *ShellSession) handleShellClipboardGet(m wire.ShellClipboardGet) error {
	if !slotOK(m.Slot) {
		return s.WriteCtrl(wire.NewShellClipboardData(m.ReqID, "", ""))
	}
	e := s.router.clipboard.getSlot(m.Slot)
	d := wire.ShellClipboardData{T: wire.TShellClipboardData, ReqID: m.ReqID, Slot: m.Slot, Mime: e.mime, Source: e.source, At: e.at}
	if isTextMime(e.mime) {
		d.Text = string(e.data)
	} else {
		d.Data = e.data
	}
	return s.WriteCtrl(d)
}

// handleClipboardGet replies on the requester's event channel.
func (inst *AppInstance) handleClipboardGet(m wire.EvtClipboardGet) error {
	mime, data := inst.router.clipboard.get()
	return inst.WriteEvt(wire.NewEvtClipboardData(m.ReqID, mime, data))
}

// pasteKeep is how many saved image pastes stay on disk.
const pasteKeep = 20

// pasteDir is where pasted images are written: the user's runtime dir,
// which is per-user, tmpfs on most systems and gone at reboot.
func pasteDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "wash", "paste")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("wash-paste-%d", os.Getuid()))
}

var imageExt = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
	"image/bmp":  ".bmp",
}

// handleShellClipboardSaveImage writes a pasted image where the terminal's
// programs can read it, makes it the clipboard, and answers with its path.
// A terminal can't take image bytes, but a CLI can take a file: Claude Code
// attaches an image given its path.
func (s *ShellSession) handleShellClipboardSaveImage(m wire.ShellClipboardSaveImage) error {
	reply := wire.ShellClipboardSaved{T: wire.TShellClipboardSaved, ReqID: m.ReqID}
	path, err := savePasteImage(pasteDir(), m.Mime, m.Data, time.Now())
	if err != nil {
		reply.Error = err.Error()
		s.router.log("clipboard.save_image: %v", err)
		return s.WriteCtrl(reply)
	}
	reply.Path = path
	e := s.router.clipboard.setSlot("", m.Mime, m.Data, m.Source)
	s.router.broadcastClipboardChanged(nil, m.Mime)
	s.router.broadcastShellClipboardChanged(s, "", e)
	return s.WriteCtrl(reply)
}

func savePasteImage(dir, mime string, data []byte, now time.Time) (string, error) {
	ext, ok := imageExt[mime]
	if !ok {
		return "", fmt.Errorf("not an image type: %q", mime)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty image")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var tag [3]byte
	_, _ = rand.Read(tag[:])
	path := filepath.Join(dir, "paste-"+now.Format("20060102-150405")+"-"+hex.EncodeToString(tag[:])+ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	prunePastes(dir, pasteKeep)
	return path, nil
}

// prunePastes keeps the newest keep files; names sort by time.
func prunePastes(dir string, keep int) {
	names, err := filepath.Glob(filepath.Join(dir, "paste-*"))
	if err != nil || len(names) <= keep {
		return
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-keep] {
		_ = os.Remove(n)
	}
}

func (r *Router) broadcastClipboardChanged(except *AppInstance, mime string) {
	r.mu.Lock()
	snap := make([]*AppInstance, 0, len(r.apps))
	for _, inst := range r.apps {
		if inst != except {
			snap = append(snap, inst)
		}
	}
	r.mu.Unlock()
	notice := wire.NewEvtClipboardChanged(mime)
	for _, inst := range snap {
		if err := inst.WriteEvt(notice); err != nil {
			r.log("clipboard.changed → %s: %v", inst.AppID, err)
		}
	}
}

// broadcastShellClipboardChanged pushes a slot's new content to every
// attached shell except the setter (nil = notify all shells, used when
// the change came from an app BE rather than a shell). Non-text content
// is announced with its size, not pushed.
func (r *Router) broadcastShellClipboardChanged(except *ShellSession, slot string, e clipboardEntry) {
	notice := wire.ShellClipboardChanged{T: wire.TShellClipboardChanged, Slot: slot, Mime: e.mime, Source: e.source, At: e.at}
	if isTextMime(e.mime) {
		notice.Text = string(e.data)
	} else {
		notice.Size = len(e.data)
	}
	for _, s := range r.shellList() {
		if s == except {
			continue
		}
		if err := s.WriteCtrl(notice); err != nil {
			r.log("clipboard.changed → shell: %v", err)
		}
	}
}
