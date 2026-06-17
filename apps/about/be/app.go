// Package about is the wash-about app's compiled-in form. The
// standalone shim (cmd/wash-about/main.go) imports this package
// blank so its init() registers; sdk.Main does the rest. The
// multi-call dispatcher (cmd/wash) blank-imports it too — same
// registration, same Run, exec'd via argv[0] = "wash-about".
//
// About pushes two app_msg kinds to its FE:
//
//   - about.info: build/router/host facts. Static for the process's
//     lifetime; emitted on OnReady and on FE "refresh" requests.
//   - runtime.table: the live process table. Polled from the
//     com.wash.router synthetic peer every few seconds while the
//     window is mapped; paused on minimize.
package about

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"
	"os/user"
	"sync/atomic"
	"time"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.9.1"

// washIcon — Lucide sprite symbol name. The shell's sprite already
// includes "info" via web/shell/build-icons.mjs.
const washIcon = "info"

// runtimePollInterval is the cadence at which the BE queries the
// router for the runtime table. Matches the SDK heartbeat (5s) so
// the table is at most one tick stale.
const runtimePollInterval = 5 * time.Second

// startedAt is captured at init so the uptime field reports the
// process's lifetime (since spawn) rather than just the wall clock.
var startedAt = time.Now()

// active tracks whether the window is currently mapped — the runtime
// poller pauses while the window is minimized so a backgrounded
// about doesn't spin up router work for a viewer that can't see it.
var active atomic.Bool

// def is built once at init and reused by Def (for sdk.Main in the
// standalone shim) and by run (for the registered Run callback).
var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		// Panic instead of log+continue: a missing asset sub-fs means
		// the //go:embed pattern produced an empty FS, which never
		// works for an app. Fail loudly at startup.
		panic("wash-about: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.about",
			Name:            "About wash",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-about",
			Surface:         sdk.SurfaceWindow,
			Icon:            washIcon,
			Accent:          "#5cb0b0",
			Instancing:      sdk.InstancingMulti,
			Capabilities:    []string{},
			Window:          &sdk.WindowHints{DefaultWidth: 760, DefaultHeight: 640},
		},
		Assets:   sub,
		OnReady:  onReady,
		OnMapped: onMapped,
		OnState:  onState,
	}
	registry.Register(&registry.App{
		Name:     "wash-about",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def returns the *sdk.AppDef for the standalone shim to hand to
// sdk.Main. Keeps the manifest/Assets/callbacks construction in
// exactly one place — shim and registry both reference the same struct.
func Def() *sdk.AppDef { return def }

// run is the post-handshake event loop used by the multi-call
// dispatcher (which has already handled --wash-manifest itself).
func run(ctx context.Context) error { return sdk.Run(ctx, def) }

// onReady ships the static about.info payload, installs the bus
// handler for FE-initiated refresh, and starts the runtime-table
// poller goroutine.
func onReady(c *sdk.Conn, _ string, _ uint32) {
	active.Store(true)
	sendInfo(c)
	bus := sdk.NewBus(c)
	sdk.HandleVoid(bus, "refresh", func(conn *sdk.Conn, _ string, _ struct{}) error {
		sendInfo(conn)
		// Refresh also re-queries the router so the table is fresh
		// when the user clicks the button (not just static info).
		go queryRuntime(context.Background(), bus, conn)
		return nil
	})
	go runtimeLoop(bus, c)
}

// onMapped + onState pause the runtime poll while the window is
// minimized; resume on restore. Matches the wash-top pattern.
func onMapped(_ *sdk.Conn, _ uint32) { active.Store(true) }
func onState(_ *sdk.Conn, _ uint32, state string) {
	switch state {
	case "minimized":
		active.Store(false)
	default:
		active.Store(true)
	}
}

// runtimeLoop polls the router's synthetic peer every
// runtimePollInterval while the window is mapped, forwarding each
// reply to the FE as runtime.table. Exits when the connection closes
// — about is InstancingMulti, so without the Done() select every
// closed window would leak the goroutine and ticker for the lifetime
// of the process.
func runtimeLoop(bus *sdk.Bus, c *sdk.Conn) {
	if active.Load() {
		queryRuntime(context.Background(), bus, c)
	}
	t := time.NewTicker(runtimePollInterval)
	defer t.Stop()
	for {
		select {
		case <-c.Done():
			return
		case <-t.C:
			if !active.Load() {
				continue
			}
			queryRuntime(context.Background(), bus, c)
		}
	}
}

// queryRuntime does one cross-app bus call to com.wash.router and
// forwards the reply payload verbatim to the FE under the kind
// "runtime.table". On router error (timeout, peer disconnected) the
// FE keeps the last good table — no error envelope needed.
func queryRuntime(ctx context.Context, bus *sdk.Bus, c *sdk.Conn) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var resp runtimeTableMsg
	err := sdk.Call(ctx, bus, wire.Recipient{AppID: "com.wash.router"},
		"runtime.query", struct{}{}, &resp)
	if err != nil {
		log.Printf("wash-about: runtime.query: %v", err)
		return
	}
	resp.Kind = "runtime.table"
	if err := c.SendAppMsg(resp); err != nil {
		log.Printf("wash-about: send runtime.table: %v", err)
	}
}

// runtimeTableMsg mirrors the router's runtimeTable wire shape with
// a kind tag tacked on so the FE can switch on it like any other
// app_msg.
type runtimeTableMsg struct {
	Kind          string       `json:"kind"`
	Rows          []runtimeRow `json:"rows"`
	TotalRSS      uint64       `json:"total_rss"`
	CapturedAtSec int64        `json:"captured_at_sec"`
}

// runtimeRow mirrors router.runtimeRow. Stays a separate struct so
// the app doesn't depend on the router's internal types — the wire
// shape is the contract.
type runtimeRow struct {
	InstanceID     string `json:"instance_id"`
	AppID          string `json:"app_id,omitempty"`
	Name           string `json:"name"`
	PID            int    `json:"pid"`
	HasHeartbeat   bool   `json:"has_heartbeat"`
	RuntimeKind    string `json:"runtime_kind,omitempty"`
	GoVersion      string `json:"go_version,omitempty"`
	NumGoroutine   int    `json:"num_goroutine,omitempty"`
	NumCgoCall     int64  `json:"num_cgo_call,omitempty"`
	HeapAlloc      uint64 `json:"heap_alloc,omitempty"`
	HeapInuse      uint64 `json:"heap_inuse,omitempty"`
	HeapObjects    uint64 `json:"heap_objects,omitempty"`
	StackInuse     uint64 `json:"stack_inuse,omitempty"`
	Sys            uint64 `json:"sys,omitempty"`
	NumGC          uint32 `json:"num_gc,omitempty"`
	GCPauseTotal   uint64 `json:"gc_pause_total_ns,omitempty"`
	UptimeSec      int64  `json:"uptime_sec,omitempty"`
	RSSBytes       uint64 `json:"rss_bytes,omitempty"`
	VSizeBytes     uint64 `json:"vsize_bytes,omitempty"`
	LastSeenAgeSec int64  `json:"last_seen_age_sec,omitempty"`
}

func sendInfo(c *sdk.Conn) {
	info := gatherInfo()
	if err := c.SendAppMsg(info); err != nil {
		log.Printf("wash-about: send about.info: %v", err)
	}
}

// ----- about.info wire payload (build + host only; per-process Go
// runtime lives in the runtime.table) -----

type aboutInfo struct {
	Kind  string     `json:"kind"`
	Build buildBlock `json:"build"`
	Host  hostBlock  `json:"host"`
}

type buildBlock struct {
	AppVersion      string `json:"app_version"`
	ProtocolVersion int    `json:"protocol_version"`
	RouterVersion   string `json:"router_version,omitempty"`
	RouterCommit    string `json:"router_commit,omitempty"`
	RouterBuilt     string `json:"router_built,omitempty"`
	RouterDev       bool   `json:"router_dev,omitempty"`
}

type hostBlock struct {
	UID       int    `json:"uid"`
	Username  string `json:"username,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	Workdir   string `json:"workdir,omitempty"`
	UptimeSec int64  `json:"uptime_sec"`
}

func gatherInfo() aboutInfo {
	return aboutInfo{
		Kind:  "about.info",
		Build: gatherBuild(),
		Host:  gatherHost(),
	}
}

func gatherBuild() buildBlock {
	return buildBlock{
		AppVersion:      version,
		ProtocolVersion: sdk.ProtocolVersion,
		RouterVersion:   os.Getenv("WASH_ROUTER_VERSION"),
		RouterCommit:    os.Getenv("WASH_ROUTER_COMMIT"),
		RouterBuilt:     os.Getenv("WASH_ROUTER_BUILT"),
		RouterDev:       os.Getenv("WASH_ROUTER_DEV") == "1",
	}
}

func gatherHost() hostBlock {
	h := hostBlock{
		UID:       os.Getuid(),
		UptimeSec: int64(time.Since(startedAt).Seconds()),
	}
	if hn, err := os.Hostname(); err == nil {
		h.Hostname = hn
	}
	if u, err := user.Current(); err == nil {
		h.Username = u.Username
	}
	if wd, err := os.Getwd(); err == nil {
		h.Workdir = wd
	}
	return h
}
