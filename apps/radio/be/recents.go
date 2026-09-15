package radio

import (
	"log"
	"sync"
	"time"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// The start menu's Radio flyout (apps/session/be/launcher_bus.go) reads
// and replays stations through two hooks here: this app notes each station
// it plays, and tunes to one the desktop names.

const sessionAppID = "com.wash.session"

// noteRepeatWindow is how long a replay of the same station stays one
// entry's worth of noise. <audio> reconnects after a network hiccup, and
// each reconnect is a new /stream request — without this every blip would
// rewrite the recent file.
const noteRepeatWindow = 60 * time.Second

type playStationReq struct {
	Name string `json:"name"`
}

// noteStation tells the session BE this station played. The session reads
// the app id from the router-attested sender, so none is sent.
func noteStation(c *sdk.Conn, name string) {
	if err := c.SendAppMsgTo(wire.Recipient{AppID: sessionAppID}, map[string]any{
		"kind": "recent.note",
		"name": name,
	}); err != nil {
		log.Printf("wash-radio: recent note name=%q: %v", name, err)
		return
	}
	log.Printf("wash-radio: recent note name=%q", name)
}

type noteGate struct {
	mu   sync.Mutex
	last string
	at   time.Time
}

func newNoteGate() *noteGate { return &noteGate{} }

// allow reports whether a play of name at now is worth noting: a
// different station always is, the same one only once per window.
func (g *noteGate) allow(name string, now time.Time) bool {
	if name == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if name == g.last && now.Sub(g.at) < noteRepeatWindow {
		return false
	}
	g.last, g.at = name, now
	return true
}

// tuner holds a start-menu tune request until the FE can act on it. A
// freshly started Radio has no station list yet, and a name means nothing
// to it until one arrives, so the request rides the first stations_ok.
type tuner struct {
	mu      sync.Mutex
	ready   bool
	pending string
}

// request records name. It reports true when the FE already has its list
// and should be told now; false when the name will ride the next list.
func (t *tuner) request(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ready {
		return true
	}
	t.pending = name
	return false
}

// loaded marks the station list as delivered and returns any held name,
// once.
func (t *tuner) loaded() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ready = true
	name := t.pending
	t.pending = ""
	return name
}
