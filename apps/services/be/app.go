// wash-services — frontend for the init system. systemd + openrc
// today, OpenWRT procd ready to slot in (see procd.go).
//
// One BE per window (instancing=multi). Detects the host's init at
// onReady, lists units, and routes action verbs (start, stop,
// restart, enable, disable, reload) through wash-priv so the
// password prompt + audit trail live in one place rather than being
// reinvented here.
//
// Distro-specific work is fully behind the Backend interface
// (backend.go). app.go is init-system-agnostic: it asks the backend
// for the catalog, asks for an argv when the user clicks an action,
// and forwards the argv to priv. New init systems drop in next to
// systemd.go / openrc.go / procd.go without touching this file.
//
// Logs deeplink as a separate window: backend.LogAppID() returns
// com.wash.journal on systemd, com.wash.syslogs elsewhere. The
// spawned app starts at its catalog and (on systemd only) gets a
// cmd.select_unit follow-up so the chosen unit is pre-selected.

package services

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.8.0"

// servicesIcon — lucide sprite name. server-cog reads as "init
// system" without colliding with com.wash.settings's gear-shaped
// glyph (settings uses "settings").
const servicesIcon = "server-cog"

// ---- wire types ----

type Service struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Active is systemd's high-level state — "active", "inactive",
	// "failed", "activating", "deactivating", "reloading". For openrc
	// we synthesize "active" / "inactive" / "failed" from the more
	// granular sub status so the FE doesn't need two badge ladders.
	Active string `json:"active"`
	// Sub is the granular sub-state. systemd: "running", "exited",
	// "dead", "start-pre", … openrc: "started", "crashed", "stopped".
	Sub string `json:"sub"`
	// Load is systemd's load state — "loaded", "not-found", "masked"
	// — or "loaded" / "not-loaded" for openrc (synthesized).
	Load string `json:"load"`
	// Enabled is the unit-file state. systemd: "enabled", "disabled",
	// "static", "masked", "alias", "generated", "indirect", "linked".
	// openrc: "enabled" / "disabled" — anything assigned to any
	// runlevel is "enabled".
	Enabled string `json:"enabled"`
}

type ListResp struct {
	Kind     string    `json:"kind"`
	Init     string    `json:"init"`
	Services []Service `json:"services"`
}

type ActionReq struct {
	Name string `json:"name"`
	Op   string `json:"op"`
}

type ActionResp struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Op     string `json:"op"`
	OK     bool   `json:"ok"`
	Exit   int    `json:"exit"`
	Stderr string `json:"stderr"`
}

type ShowLogsReq struct {
	Name string `json:"name"`
}

// ---- BE state ----

type be struct {
	conn    *sdk.Conn
	backend Backend // nil ⇒ no supported init detected; FE shows banner

	// pendingJournalUnits is the FIFO of unit names waiting for their
	// spawned wash-journal instance id to arrive. show_logs pushes
	// the chosen unit before firing SpawnRequest; OnSpawnResult pops
	// the front entry once the router replies and routes a select
	// app_msg to the new instance. The router's spawn dispatch is
	// FIFO with respect to a single requester so the queue stays in
	// the right order even under back-to-back clicks.
	pendingMu          sync.Mutex
	pendingJournalUnit []string
}

var st *be

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-services: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.services",
			Name:            "Services",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-services",
			Surface:         sdk.SurfaceWindow,
			Icon:            servicesIcon,
			Accent:          "#5cb0d0",
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 820, DefaultHeight: 600},
			// spawn for the log deeplink (wash-journal / wash-syslogs).
			// The action verbs go through wash-priv, which doesn't need
			// the spawn capability on us — wash-priv owns the spawn.
			Capabilities: []string{sdk.CapSpawn},
		},
		Assets:        sub,
		OnReady:       onReady,
		OnSpawnResult: onSpawnResult,
	}
	registry.Register(&registry.App{
		Name:     "wash-services",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	backend := Detect()
	name := "(none)"
	if backend != nil {
		name = backend.Name()
	}
	log.Printf("wash-services ready instance=%s window=%d init=%s", instanceID, windowID, name)
	st = &be{conn: c, backend: backend}
	bus := sdk.NewBus(c)
	registerHandlers(bus)
	// Push the first listing immediately so the FE has data on mount
	// without a round-trip.
	go pushList()
}

func registerHandlers(b *sdk.Bus) {
	sdk.HandleVoid(b, "list", func(_ *sdk.Conn, _ string, _ struct{}) error {
		go pushList()
		return nil
	})
	sdk.HandleVoid(b, "action", func(_ *sdk.Conn, _ string, req ActionReq) error {
		// PrivRunInlineSync blocks on wash-priv's reply stream which
		// arrives on the same read goroutine that runs this callback —
		// see the SDK doc comment. Spawn a goroutine to avoid deadlock.
		go runAction(req)
		return nil
	})
	sdk.HandleVoid(b, "show_logs", func(_ *sdk.Conn, _ string, req ShowLogsReq) error {
		if st == nil || st.backend == nil {
			return nil
		}
		logApp := st.backend.LogAppID()
		if logApp == "" {
			return nil
		}
		// Only systemd carries unit-scoped logs that pre-select
		// cleanly on the deeplink target. For other backends we
		// just open the log viewer at its default state — the user
		// picks from there.
		if logApp == "com.wash.journal" {
			st.pendingMu.Lock()
			st.pendingJournalUnit = append(st.pendingJournalUnit, req.Name)
			st.pendingMu.Unlock()
		}
		return st.conn.SpawnRequest(logApp)
	})
}

// onSpawnResult deeplinks a freshly-spawned wash-journal to the unit
// the user clicked. The pending FIFO matches show_logs's order: each
// show_logs pushes its unit, and the router's per-requester serial
// spawn dispatch means OnSpawnResult fires in the same order. If the
// spawn failed (err != nil), pop the front entry without sending —
// the unit name belongs to a journal that never came up.
func onSpawnResult(c *sdk.Conn, appID, instanceID string, spawnErr error) {
	if st == nil || appID != "com.wash.journal" {
		return
	}
	st.pendingMu.Lock()
	if len(st.pendingJournalUnit) == 0 {
		st.pendingMu.Unlock()
		return
	}
	unit := st.pendingJournalUnit[0]
	st.pendingJournalUnit = st.pendingJournalUnit[1:]
	st.pendingMu.Unlock()
	if spawnErr != nil {
		log.Printf("wash-services: journal spawn failed for unit=%s: %v", unit, spawnErr)
		return
	}
	// Match journal's selectReq shape (apps/journal/be/app.go).
	// range="day" gives a useful default window; priority=0 disables
	// the level filter; as_root=false tries unprivileged first
	// (journal's perm_denied path will flip to root if needed).
	// Router enforces exactly one of instance_id / app_id on the
	// recipient. We have the freshly-spawned instance id, so address
	// it directly — app_id-only would broadcast to every journal
	// window, including unrelated ones the user already had open.
	//
	// We send cmd.select_unit (not the raw `select`) so journal's
	// cross-app handler echoes it down to its own FE, which then
	// runs the same onPickUnit code path a sidebar click would —
	// toolbar range/priority/as_root stay on the user's own
	// defaults, and the sidebar row highlights correctly.
	log.Printf("wash-services: deeplink unit=%q → journal instance=%s", unit, instanceID)
	if err := c.SendAppMsgTo(
		wire.Recipient{InstanceID: instanceID},
		map[string]any{
			"kind": "cmd.select_unit",
			"unit": unit,
		},
	); err != nil {
		log.Printf("wash-services: journal deeplink send: %v", err)
	}
}

// pushList asks the backend for the catalog and ships the result
// to the FE. Safe to call from any goroutine; called both on
// demand (FE refresh) and as a side effect of a successful action.
func pushList() {
	if st == nil {
		return
	}
	resp := ListResp{Kind: "services_list"}
	if st.backend != nil {
		resp.Init = st.backend.Name()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		services, err := st.backend.List(ctx)
		if err != nil {
			log.Printf("wash-services: list: %v", err)
		}
		resp.Services = services
	}
	if err := st.conn.SendAppMsg(resp); err != nil {
		log.Printf("wash-services: list send: %v", err)
	}
}

// runAction asks the backend for the argv, runs it through priv,
// posts the result back, and refreshes the listing on success so
// the row's active/enabled badges update without another FE round-
// trip.
func runAction(req ActionReq) {
	if st == nil {
		return
	}
	ok, exit, stderr := dispatchAction(req)
	if err := st.conn.SendAppMsg(ActionResp{
		Kind: "action_done", Name: req.Name, Op: req.Op,
		OK: ok, Exit: exit, Stderr: stderr,
	}); err != nil {
		log.Printf("wash-services: action_done send: %v", err)
	}
	if ok {
		pushList()
	}
}

func dispatchAction(req ActionReq) (ok bool, exit int, stderr string) {
	if st.backend == nil {
		return false, -1, "no supported init system detected"
	}
	argv := st.backend.ActionArgv(req.Op, req.Name)
	if len(argv) == 0 {
		return false, -1, "unsupported op for " + st.backend.Name() + ": " + req.Op
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := st.conn.PrivRunInlineSync(ctx, argv, "services: "+req.Op+" "+req.Name)
	if err != nil {
		return false, -1, err.Error()
	}
	return r.Exit == 0, r.Exit, string(r.Stderr)
}
