// Router-held clipboard. v0.1 keeps the state inside the router as a
// single (mime, data) pair; apps set/get/observe via the event
// channel. The ARCHITECTURE.md intent of "shell-owned clipboard" can
// move state into the shell later — the wire surface stays the same.

package router

import (
	"sync"

	"github.com/sirmick/wash/internal/wire"
)

type clipboardState struct {
	mu   sync.RWMutex
	mime string
	data []byte
}

func (c *clipboardState) set(mime string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mime = mime
	// Copy so a later caller mutating their byte slice doesn't shift
	// our store underneath us.
	c.data = append(c.data[:0:0], data...)
}

func (c *clipboardState) get() (string, []byte) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := append([]byte(nil), c.data...)
	return c.mime, cp
}

// handleClipboardSet stores new content and broadcasts a "changed"
// event to every connected app except the one that set it.
func (inst *AppInstance) handleClipboardSet(m wire.EvtClipboardSet) error {
	inst.router.clipboard.set(m.Mime, m.Data)
	inst.router.broadcastClipboardChanged(inst, m.Mime)
	return nil
}

// handleClipboardGet replies on the requester's event channel.
func (inst *AppInstance) handleClipboardGet(m wire.EvtClipboardGet) error {
	mime, data := inst.router.clipboard.get()
	return inst.WriteEvt(wire.NewEvtClipboardData(m.ReqID, mime, data))
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
