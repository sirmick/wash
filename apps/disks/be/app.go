// wash-disks — storage information app for the wash desktop.
//
// One Go BE per window (instancing=multi). Reads /sys/block, /proc/mounts,
// /proc/diskstats and statfs(2) directly — no router service involved — and
// pushes a periodic snapshot of the box's physical disks, their partitions,
// fullness, and live I/O to the FE. Logical-storage managers (md software
// RAID now; LVM/btrfs/ZFS later) are layered on top as detected "providers".
//
// The unprivileged layer (disks, partitions, mounts, I/O, md) drives the
// always-on poll. Privileged providers (SMART, LVM, btrfs, ZFS) run through
// wash-priv on demand and never in the poll loop, so a background Disks
// window never triggers a recurring sudo prompt.
//
// The snapshot stream pauses while the window is unmapped (minimize / blur),
// mirroring wash-top — a hidden Disks window doesn't walk /sys every couple
// seconds for no viewer.
package disks

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

const (
	version = "0.8.0"

	// defaultIntervalMS is the snapshot cadence. Disk topology and
	// fullness change slowly; 3s keeps I/O rates lively without churn.
	defaultIntervalMS = 3000
	minIntervalMS     = 1000
	maxIntervalMS     = 30_000

	// disksIcon — Lucide sprite symbol name (see wash-top's topIcon).
	disksIcon = "hard-drive"
)

// ---- BE state ----

type be struct {
	conn       *sdk.Conn
	intervalMS atomic.Int64
	active     atomic.Bool   // set when the window is mapped
	wake       chan struct{} // pokes the snapshot loop after a config change
}

var (
	st  *be
	def *sdk.AppDef
)

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-disks: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.disks",
			Name:            "Disks",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-disks",
			Surface:         sdk.SurfaceWindow,
			Icon:            disksIcon,
			Accent:          "#3a9d8f",
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 900, DefaultHeight: 580},
		},
		Assets:   sub,
		OnReady:  onReady,
		OnMapped: onMapped,
		OnState:  onState,
	}
	registry.Register(&registry.App{
		Name:     "wash-disks",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

// run is the registered app entrypoint. It honors --dump-snapshot (a
// one-shot, router-free JSON dump used by the real-kernel VM tests and
// for debugging) before falling through to the normal event loop.
func run(ctx context.Context) error {
	if wantsDump() {
		return DumpSnapshot(ctx, os.Stdout)
	}
	return sdk.Run(ctx, def)
}

// wantsDump reports whether --dump-snapshot was passed.
func wantsDump() bool {
	for _, a := range os.Args[1:] {
		if a == "--dump-snapshot" {
			return true
		}
	}
	return false
}

// DumpSnapshot collects one snapshot and writes it as JSON. Runs every
// collector + provider once with no router/priv/UI. When invoked as root
// the privileged providers shell out directly (see directRunner); as a
// normal user they're skipped and reported only in capabilities.
func DumpSnapshot(ctx context.Context, w io.Writer) error {
	snap := collectSnapshot(ctx, directRunner)
	snap.Kind = "snapshot"
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(snap)
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-disks ready instance=%s window=%d", instanceID, windowID)
	st = &be{
		conn: c,
		wake: make(chan struct{}, 1),
	}
	st.intervalMS.Store(defaultIntervalMS)
	// Start active: the FE listens on mount and the window is mapped by
	// the time the loop pushes its first frame.
	st.active.Store(true)
	bus := sdk.NewBus(c)
	registerHandlers(bus)
	go snapshotLoop()
}

// ----- bus types + handlers -----

type setIntervalReq struct {
	MS int `json:"ms"`
}

func registerHandlers(b *sdk.Bus) {
	sdk.HandleVoid(b, "set_interval", func(_ *sdk.Conn, _ string, req setIntervalReq) error {
		ms := req.MS
		if ms < minIntervalMS {
			ms = minIntervalMS
		}
		if ms > maxIntervalMS {
			ms = maxIntervalMS
		}
		st.intervalMS.Store(int64(ms))
		poke()
		return nil
	})
	sdk.HandleVoid(b, "request_snapshot", func(_ *sdk.Conn, _ string, _ struct{}) error {
		poke()
		return nil
	})
	registerSmart(b)
}

// onMapped resumes the stream; onState pauses on minimize.
func onMapped(_ *sdk.Conn, _ uint32) {
	if st == nil {
		return
	}
	st.active.Store(true)
	poke()
}

func onState(_ *sdk.Conn, _ uint32, state string) {
	if st == nil {
		return
	}
	switch state {
	case "minimized":
		st.active.Store(false)
	default:
		st.active.Store(true)
		poke()
	}
}

func poke() {
	select {
	case st.wake <- struct{}{}:
	default:
	}
}

// snapshotLoop is the sole driver of the unprivileged reads. Single
// goroutine by design.
func snapshotLoop() {
	pushSnapshot()
	for {
		interval := time.Duration(st.intervalMS.Load()) * time.Millisecond
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
			if st.active.Load() {
				pushSnapshot()
			}
		case <-st.wake:
			timer.Stop()
			if st.active.Load() {
				pushSnapshot()
			}
		}
	}
}

func pushSnapshot() {
	// The poll loop never runs privileged providers — privRunner is the
	// wash-priv path used only by on-demand FE requests (M2+).
	snap := collectSnapshot(context.Background(), nil)
	snap.Kind = "snapshot"
	snap.IntMS = int(st.intervalMS.Load())
	if err := st.conn.SendAppMsg(snap); err != nil {
		log.Printf("wash-disks send snapshot: %v", err)
	}
}
