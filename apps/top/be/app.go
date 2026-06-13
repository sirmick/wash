// wash-top — Linux process monitor for the wash desktop.
//
// One Go BE per window (instancing=multi). Reads /proc directly — no
// router service involved. Pushes a periodic snapshot of cpu + mem +
// procs to the FE, replies to per-pid detail requests, and signals
// processes on the FE's behalf (kill/term).
//
// The snapshot stream pauses while the window is unmapped (after
// minimize / blur of the whole desktop), so a background top doesn't
// pin a core walking /proc twice a second for no viewer.

package top

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

const (
	version = "0.9.0"

	// defaultIntervalMS is the snapshot cadence in milliseconds.
	// 2s = htop's default; brisk enough to feel live, slow enough
	// that a 500-proc box ships ~50 KB/s.
	defaultIntervalMS = 2000
	minIntervalMS     = 500
	maxIntervalMS     = 10_000
)

// ---- wire payloads ----
//
// Plain Go scalars + maps + slices. The whole pipeline is JSON: the
// bus marshals via encoding/json and the FE parses the same.

type Snapshot struct {
	Kind     string     `json:"kind"`
	TS       int64      `json:"ts"`
	CPUCount int        `json:"cpu_count"`
	CPU      cpuRow     `json:"cpu"`
	PerCPU   []cpuRow   `json:"per_cpu"`
	Mem      memRow     `json:"mem"`
	Load     loadRow    `json:"load"`
	Net      netAgg     `json:"net"`
	PerNet   []netDev   `json:"per_net"`
	Disk     diskAgg    `json:"disk"`
	PerDisk  []diskDev  `json:"per_disk"`
	Procs    []ProcInfo `json:"procs"`
	IntMS    int        `json:"interval_ms"`
}

type cpuRow struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

type memRow struct {
	Total, Available, Free, Buffers, Cached uint64
	SwapTotal, SwapFree                     uint64
}

type loadRow struct {
	L1, L5, L15      float64
	Running, Threads uint32
}

// netAgg / netDev: cumulative byte counters. The FE diffs against the
// previous tick to render bytes/s. Aggregate excludes loopback and
// virtual interfaces (docker0, br-*, veth, tun/tap, wg, etc.); the
// per_net array carries every interface for the popover.
type netAgg struct {
	RxBytes, TxBytes uint64
}

type netDev struct {
	Name             string `json:"name"`
	RxBytes, TxBytes uint64
	Real             bool `json:"real"`
}

// diskAgg / diskDev: cumulative read/write bytes per block device.
// Aggregate covers whole physical disks only (sd*, vd*, nvme*,
// mmcblk*, xvd*, hd*) so dm-* / md* don't double-count.
type diskAgg struct {
	ReadBytes, WriteBytes uint64
}

type diskDev struct {
	Name                  string `json:"name"`
	ReadBytes, WriteBytes uint64
	Real                  bool `json:"real"`
}

// ProcInfo's JSON tags live on the struct (in proc.go) — explicit
// lowercase names so the FE reads consistent keys regardless of
// Go's CamelCase field naming.

// ---- BE state ----

type be struct {
	conn      *sdk.Conn
	sampler   *Sampler
	intervalMS atomic.Int64
	active    atomic.Bool // set when window is mapped
	wake      chan struct{} // pokes the snapshot loop after config change
}

var st *be

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-top: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.top",
			Name:            "System Monitor",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-top",
			Surface:         sdk.SurfaceWindow,
			Icon:            topIcon,
			Accent:          "#b070d0",
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 880, DefaultHeight: 560},
		},
		Assets:   sub,
		OnReady:  onReady,
		OnMapped: onMapped,
		OnState:  onState,
	}
	registry.Register(&registry.App{
		Name:     "wash-top",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-top ready instance=%s window=%d", instanceID, windowID)
	st = &be{
		conn:    c,
		sampler: NewSampler(),
		wake:    make(chan struct{}, 1),
	}
	st.intervalMS.Store(defaultIntervalMS)
	// Start active so the FE sees a snapshot immediately — the FE
	// listens on mount and the window is always at least already
	// mapped by the time we call SendAppMsg in the loop.
	st.active.Store(true)
	bus := sdk.NewBus(c)
	registerHandlers(bus)
	go snapshotLoop()
}

// ----- bus types + handlers -----

type setIntervalReq struct {
	MS int `json:"ms"`
}

type detailsReq struct {
	PID int32 `json:"pid"`
}

// detailsResp mirrors the Details struct in proc.go with JSON tags
// so the bus encodes it cleanly. Field types match Details.
type detailsResp struct {
	PID      int32             `json:"pid"`
	Found    bool              `json:"found"`
	Comm     string            `json:"comm"`
	Cmd      string            `json:"cmd"`
	Exe      string            `json:"exe"`
	Cwd      string            `json:"cwd"`
	Status   map[string]string `json:"status"`
	IO       map[string]string `json:"io"`
	Limits   []limitRow        `json:"limits"`
	Cgroup   string            `json:"cgroup"`
	FDs      int32             `json:"fds"`
	Wchan    string            `json:"wchan"`
	OOMScore string            `json:"oom_score"`
}

type signalReq struct {
	PID int32  `json:"pid"`
	Sig string `json:"sig"`
}

type signalResp struct {
	PID int32  `json:"pid"`
	Sig string `json:"sig"`
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
	sdk.Handle(b, "details", func(_ *sdk.Conn, _ string, req detailsReq) (detailsResp, error) {
		d := readDetails(req.PID)
		return detailsResp{
			PID: req.PID, Found: d.Found, Comm: d.Comm, Cmd: d.Cmd, Exe: d.Exe,
			Cwd: d.Cwd, Status: d.Status, IO: d.IO, Limits: d.Limits,
			Cgroup: d.Cgroup, FDs: d.FDs, Wchan: d.Wchan, OOMScore: d.OOMScore,
		}, nil
	})
	sdk.Handle(b, "signal", func(_ *sdk.Conn, _ string, req signalReq) (signalResp, error) {
		if err := sendSignal(req.PID, req.Sig); err != nil {
			return signalResp{}, sdk.Err{Code: classifySignalErr(err), Msg: err.Error()}
		}
		// Push a follow-up snapshot so the row clears in the UI.
		poke()
		return signalResp{PID: req.PID, Sig: req.Sig}, nil
	})
}

// onMapped resumes the stream. onState handles minimize/restore.
func onMapped(_ *sdk.Conn, _ uint32) {
	if st == nil {
		return
	}
	st.active.Store(true)
	poke()
}

// onState pauses on minimize. Maximize/restore stays active.
// Window-close → process exit handles the rest.
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

// snapshotLoop is the sole driver of /proc reads. Single-threaded by
// design — the Sampler holds previous-tick state that doesn't need a
// mutex when only one goroutine touches it.
func snapshotLoop() {
	// Push one immediately so the FE doesn't render an empty
	// chrome for 2s on first mount.
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
	snap, err := st.sampler.Snapshot()
	if err != nil {
		log.Printf("wash-top snapshot: %v", err)
		return
	}
	snap.Kind = "snapshot"
	snap.IntMS = int(st.intervalMS.Load())
	if err := st.conn.SendAppMsg(snap); err != nil {
		log.Printf("wash-top send snapshot: %v", err)
	}
}

// sendSignal maps a name to a signum and delivers it. We deliberately
// support only the small useful set; obscure signals can be added
// when there's a reason.
func sendSignal(pid int32, name string) error {
	var sig syscall.Signal
	switch name {
	case "TERM", "":
		sig = syscall.SIGTERM
	case "KILL":
		sig = syscall.SIGKILL
	case "INT":
		sig = syscall.SIGINT
	case "HUP":
		sig = syscall.SIGHUP
	case "QUIT":
		sig = syscall.SIGQUIT
	case "STOP":
		sig = syscall.SIGSTOP
	case "CONT":
		sig = syscall.SIGCONT
	default:
		return &signalError{code: "bad_signal", msg: "unknown signal " + name}
	}
	// os.FindProcess on Linux just wraps the pid (no syscall); the
	// actual existence check happens in Signal.
	p, err := os.FindProcess(int(pid))
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

type signalError struct{ code, msg string }

func (e *signalError) Error() string { return e.msg }

func classifySignalErr(err error) string {
	if se, ok := err.(*signalError); ok {
		return se.code
	}
	if err == syscall.EPERM || (err != nil && err.Error() == "operation not permitted") {
		return "permission_denied"
	}
	if err == syscall.ESRCH || (err != nil && err.Error() == "no such process") {
		return "no_such_process"
	}
	return "signal_failed"
}

// topIcon — Lucide sprite symbol name. See cmd/wash-fm/main.go's
// fmIcon for the resolution scheme.
const topIcon = "activity"
