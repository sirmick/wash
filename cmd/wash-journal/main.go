// wash-journal — a windowed log viewer for systemd-journald.
//
// One BE per window (instancing=multi). Spawns a single journalctl
// subprocess at a time and streams parsed JSON entries to the FE.
// Service list comes from `systemctl list-units` parsed on demand.
//
// Privilege model: try unprivileged first. journalctl + the
// systemd-journal group covers ~all units. When the unprivileged
// subprocess exits with a permission-denied error, the BE flips into
// "perm_denied" state and the FE shows a "Read with root" banner; on
// click the FE sends select with as_root=true, which routes the
// stream through wash-priv (run_inline + priv.stream / priv.result).
//
// Streaming priv lives inline in this app per project convention —
// the SDK only has the buffered PrivRunInlineSync helper. If a second
// app needs streaming priv, lift it into internal/sdk.

package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"sync"
	"sync/atomic"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.0.0"

// journalIcon — lucide sprite name. scroll-text reads as "log" without
// looking like a generic document.
const journalIcon = "scroll-text"

type be struct {
	conn *sdk.Conn

	mu      sync.Mutex
	stream  *streamCtl // currently active stream; nil if none
	nextGen atomic.Int64
}

var st *be

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-journal: assets: %v", err)
	}
	sdk.Main(&sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.journal",
			Name:            "Journal",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-journal",
			Surface:         sdk.SurfaceWindow,
			Icon:            journalIcon,
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 900, DefaultHeight: 560},
		},
		Assets:       sub,
		OnReady:      onReady,
		OnAppMsgFrom: onAppMsgFrom,
	})
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-journal ready instance=%s window=%d", instanceID, windowID)
	st = &be{conn: c}
	bus := sdk.NewBus(c)
	registerHandlers(bus)
	// Push an initial unit list so the sidebar paints with content.
	go pushUnits(c)
	// Auto-start the system view so the first window is immediately
	// useful — matches how wash-top starts ticking on mount.
	go startStream(c, selectReq{Range: "boot"})
}

// ----- FE → BE bus -----

// selectReq's wire shape — Priority is `any` so JS Numbers (which
// arrive as CBOR float64 after the router's JSON→CBOR re-encode) and
// the occasional integer encoding both decode without complaint.
// startStream coerces it via sdk.ToInt64.
type selectReq struct {
	Unit     string `cbor:"unit"`     // empty = system view (no -u)
	Priority any    `cbor:"priority"` // 0..7; 0 means no filter
	Range    string `cbor:"range"`    // "boot" | "hour" | "day" | "all"
	AsRoot   bool   `cbor:"as_root"`  // route via wash-priv run_inline
}

type emptyReq struct{}

func registerHandlers(b *sdk.Bus) {
	sdk.HandleVoid(b, "select", func(c *sdk.Conn, _ string, req selectReq) error {
		// Spawning blocks on the stream stop, so off-goroutine.
		go startStream(c, req)
		return nil
	})
	sdk.HandleVoid(b, "refresh_units", func(c *sdk.Conn, _ string, _ emptyReq) error {
		go pushUnits(c)
		return nil
	})
	sdk.HandleVoid(b, "stop", func(_ *sdk.Conn, _ string, _ emptyReq) error {
		stopStream()
		return nil
	})
}

// onAppMsgFrom dispatches messages from wash-priv into the active
// stream when we're in privileged mode. Non-priv senders are ignored —
// wash-journal doesn't accept cross-app commands from arbitrary apps.
func onAppMsgFrom(_ *sdk.Conn, _ uint32, data any, from wire.Sender) {
	if from.AppID != sdk.PrivAppID {
		return
	}
	m := sdk.AsMap(data)
	if m == nil {
		return
	}
	reqID := sdk.ToString(m["req_id"])
	if reqID == "" {
		return
	}
	st.mu.Lock()
	s := st.stream
	st.mu.Unlock()
	if s == nil || s.privReqID != reqID {
		return
	}
	switch sdk.ToString(m["kind"]) {
	case "priv.stream":
		stream := sdk.ToString(m["stream"])
		raw := sdk.DecodeBase64(m["bytes"])
		if len(raw) > 0 {
			s.feedPriv(stream, raw)
		}
	case "priv.result":
		s.finishPriv(int(sdk.ToInt64(m["exit_code"])), sdk.ToString(m["error"]))
	case "rejected":
		s.finishPriv(1, "rejected: "+sdk.ToString(m["reason"]))
	}
}

// stopStream tears down the active stream (if any). Called when a new
// select supersedes the current view, or explicitly via the bus.
func stopStream() {
	st.mu.Lock()
	s := st.stream
	st.stream = nil
	st.mu.Unlock()
	if s != nil {
		s.cancel()
	}
}

func startStream(c *sdk.Conn, req selectReq) {
	gen := st.nextGen.Add(1)
	stopStream()

	s := newStream(c, gen, req)
	st.mu.Lock()
	st.stream = s
	st.mu.Unlock()

	if req.AsRoot {
		s.runPriv(context.Background())
	} else {
		s.runDirect(context.Background())
	}
}
