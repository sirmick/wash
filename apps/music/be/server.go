package music

import (
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirmick/wash/internal/audiorelay"
	"github.com/sirmick/wash/internal/medialib"
	"github.com/sirmick/wash/internal/sdk"
)

// player holds the per-instance scan root + ingress base. The FE asks for
// a scan (optionally with a new root); the reply may race ingress
// publishing, so the handler waits on ready.
type player struct {
	mu    sync.Mutex
	root  string // current os scan root
	base  string // ingress base, set once before ready closes
	ready chan struct{}
}

func (p *player) getRoot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.root
}

func (p *player) setRoot(r string) {
	p.mu.Lock()
	p.root = r
	p.mu.Unlock()
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-music ready instance=%s window=%d", instanceID, windowID)
	bus := sdk.NewBus(c)
	// Folder browsing (the @wash/ui FilePicker talks fs.* to us).
	sdk.EnableFilePicker(c)
	// Producer bridge to com.wash.audio (now-playing/transport/sidebar).
	audiorelay.Register(bus)

	p := &player{root: medialib.DefaultDir(), ready: make(chan struct{})}
	sessionRoot := c.Session().Root

	// scan: (re)scan the current — or a newly-picked — folder and hand the
	// FE the resolved track list. Replies from a goroutine because ingress
	// may not be published yet.
	sdk.HandleVoid(bus, "scan", func(conn *sdk.Conn, id string, req scanReq) error {
		go func() {
			<-p.ready
			if req.Root != "" {
				// The FilePicker path is confined to the session fs root;
				// map it back to an os path for medialib.
				osRoot := filepath.Join(sessionRoot, req.Root)
				if isDir(osRoot) {
					p.setRoot(osRoot)
				}
			}
			root := p.getRoot()
			entries := medialib.Scan(root)
			tracks := make([]trackMsg, len(entries))
			for i, e := range entries {
				tracks[i] = trackMsg{URL: p.base + "lib/" + medialib.EscapePath(e.Rel), Title: e.Title}
			}
			_ = conn.SendAppMsg(map[string]any{
				"kind": "scan_ok", "id": id, "root": root, "tracks": tracks,
			})
			log.Printf("wash-music: %d track(s) from %q", len(tracks), root)
		}()
		return nil
	})

	go func() {
		base, err := medialib.Serve(c, instanceID, "wash-music-", p.getRoot)
		if err != nil {
			log.Printf("wash-music: serve: %v", err)
			return
		}
		p.base = base // single writer, before ready closes (happens-before)
		close(p.ready)
	}()
}

func isDir(pth string) bool {
	fi, err := os.Stat(pth)
	return err == nil && fi.IsDir()
}

type scanReq struct {
	Root string `json:"root"`
}

type trackMsg struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}
