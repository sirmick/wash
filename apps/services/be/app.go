// wash-services — frontend for the init system. systemd + openrc.
//
// One BE per window (instancing=multi). Detects the host's init at
// onReady, lists units, and routes the action verbs (start, stop,
// restart, enable, disable) through wash-priv so the password prompt
// + audit trail land in one place rather than reinventing them here.
//
// systemd is the JSON path: `systemctl list-units --output=json`
// returns load/active/sub/description in one round, merged with
// `list-unit-files` for the enabled-vs-disabled state. openrc has no
// JSON; we walk /etc/init.d for the catalog and `rc-status -f ini`
// for runtime state. Descriptions on openrc would require a per-
// service exec of `rc-service <name> describe` — skipped in v1; the
// name carries enough.
//
// Logs deeplink as a separate window: SpawnRequest com.wash.journal
// (systemd) or com.wash.syslogs (openrc). SpawnRequest doesn't ship
// an args payload yet, so the spawned app starts at its catalog and
// the user picks the unit there. Worth revisiting once spawn args
// land.

package services

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.0.0"

// servicesIcon — lucide sprite name. server-cog reads as "init
// system" without colliding with com.wash.settings's gear-shaped
// glyph (settings uses "settings").
const servicesIcon = "server-cog"

// initSystem is the detected init flavour. Empty means we couldn't
// identify one — the FE will show a placeholder.
type initSystem string

const (
	initUnknown initSystem = ""
	initSystemd initSystem = "systemd"
	initOpenRC  initSystem = "openrc"
)

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
	conn *sdk.Conn
	init initSystem

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
	log.Printf("wash-services ready instance=%s window=%d", instanceID, windowID)
	st = &be{conn: c, init: detectInit()}
	log.Printf("wash-services init system: %q", string(st.init))
	bus := sdk.NewBus(c)
	registerHandlers(bus)
	// Push the first listing immediately so the FE has data on mount
	// without a round-trip.
	go pushList()
}

// detectInit probes the host. systemd wins if both systemctl and the
// runtime directory exist (otherwise we'd mis-detect on a box that
// has systemctl installed but isn't running it as PID 1, like inside
// some Docker images).
func detectInit() initSystem {
	if _, err := exec.LookPath("systemctl"); err == nil {
		if _, err := os.Stat("/run/systemd/system"); err == nil {
			return initSystemd
		}
	}
	if _, err := exec.LookPath("rc-service"); err == nil {
		return initOpenRC
	}
	return initUnknown
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
		if st == nil {
			return nil
		}
		switch st.init {
		case initSystemd:
			// systemd: deeplink the unit. We queue the name before
			// firing SpawnRequest because the spawn ack (with the new
			// instance id) lands on OnSpawnResult, off this goroutine.
			// FIFO ordering is preserved by the router's serial spawn
			// dispatch for a given requester.
			st.pendingMu.Lock()
			st.pendingJournalUnit = append(st.pendingJournalUnit, req.Name)
			st.pendingMu.Unlock()
			return st.conn.SpawnRequest("com.wash.journal")
		case initOpenRC:
			// openrc: syslogs is path-based, not unit-based — no clean
			// deeplink mapping for v1. The user picks the file in
			// syslogs' own sidebar.
			return st.conn.SpawnRequest("com.wash.syslogs")
		default:
			return nil
		}
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

// pushList runs whichever lister matches the detected init and ships
// the result to the FE. Safe to call from any goroutine; called both
// on demand (FE refresh) and as a side effect of a successful action.
func pushList() {
	if st == nil {
		return
	}
	resp := ListResp{Kind: "services_list", Init: string(st.init)}
	switch st.init {
	case initSystemd:
		resp.Services = listSystemd()
	case initOpenRC:
		resp.Services = listOpenRC()
	}
	if err := st.conn.SendAppMsg(resp); err != nil {
		log.Printf("wash-services: list send: %v", err)
	}
}

// runAction dispatches to the per-init action helper, posts the
// result back, and refreshes the listing on success so the row's
// active/enabled badges update without another FE round-trip.
func runAction(req ActionReq) {
	if st == nil {
		return
	}
	var ok bool
	var exit int
	var stderr string
	switch st.init {
	case initSystemd:
		ok, exit, stderr = runActionSystemd(req)
	case initOpenRC:
		ok, exit, stderr = runActionOpenRC(req)
	default:
		ok, exit, stderr = false, -1, "no supported init system detected"
	}
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

// ---- systemd ----

func listSystemd() []Service {
	type sysdRow struct {
		Unit, Load, Active, Sub, Description string
	}
	out, err := exec.Command("systemctl", "list-units", "--type=service", "--all",
		"--no-legend", "--plain", "--no-pager", "--output=json").Output()
	if err != nil {
		log.Printf("wash-services: list-units: %v", err)
		return nil
	}
	var rows []sysdRow
	if err := json.Unmarshal(out, &rows); err != nil {
		log.Printf("wash-services: list-units json: %v", err)
		return nil
	}
	// list-unit-files for the enabled/disabled state — list-units
	// doesn't carry it. First column is the unit, second is the
	// unit-file state; we ignore the preset column (3rd).
	enabled := map[string]string{}
	if out2, err := exec.Command("systemctl", "list-unit-files", "--type=service",
		"--no-legend", "--plain", "--no-pager").Output(); err == nil {
		for _, line := range strings.Split(string(out2), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				enabled[fields[0]] = fields[1]
			}
		}
	}
	services := make([]Service, 0, len(rows)+len(enabled))
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		seen[r.Unit] = true
		services = append(services, Service{
			Name:        r.Unit,
			Description: r.Description,
			Load:        r.Load,
			Active:      r.Active,
			Sub:         r.Sub,
			Enabled:     enabled[r.Unit],
		})
	}
	// Installed-but-not-loaded units: include so the user can enable
	// or start them without first knowing they exist.
	for name, e := range enabled {
		if seen[name] {
			continue
		}
		services = append(services, Service{
			Name:    name,
			Active:  "inactive",
			Sub:     "dead",
			Load:    "not-loaded",
			Enabled: e,
		})
	}
	return services
}

func runActionSystemd(req ActionReq) (ok bool, exit int, stderr string) {
	var argv []string
	switch req.Op {
	case "start", "stop", "restart", "reload":
		argv = []string{"systemctl", req.Op, req.Name}
	case "enable":
		argv = []string{"systemctl", "enable", req.Name}
	case "disable":
		argv = []string{"systemctl", "disable", req.Name}
	default:
		return false, 0, "unknown op: " + req.Op
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := st.conn.PrivRunInlineSync(ctx, argv, "services: "+req.Op+" "+req.Name)
	if err != nil {
		return false, -1, err.Error()
	}
	return r.Exit == 0, r.Exit, string(r.Stderr)
}

// ---- openrc ----

func listOpenRC() []Service {
	entries, err := os.ReadDir("/etc/init.d")
	if err != nil {
		log.Printf("wash-services: /etc/init.d: %v", err)
		return nil
	}
	services := make([]Service, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		services = append(services, Service{Name: e.Name(), Load: "loaded"})
	}
	// rc-status -f ini: groups services under [runlevel] headers,
	// "name = status" lines underneath. A single name can appear in
	// multiple runlevels; last-write-wins on status is fine because
	// the status is identical (it's the live state, not per-runlevel).
	running := map[string]string{}
	if out, err := exec.Command("rc-status", "-a", "-f", "ini").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "[") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			running[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	// rc-update show -v: "name | runlevel [runlevel ...]" — service is
	// enabled if the runlevel column is non-empty for any runlevel.
	enabled := map[string]bool{}
	if out, err := exec.Command("rc-update", "show", "-v").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			parts := strings.SplitN(line, "|", 2)
			if len(parts) != 2 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			rls := strings.TrimSpace(parts[1])
			if name != "" && rls != "" {
				enabled[name] = true
			}
		}
	}
	for i := range services {
		if status, ok := running[services[i].Name]; ok {
			services[i].Sub = status
			switch status {
			case "started", "running":
				services[i].Active = "active"
			case "crashed":
				services[i].Active = "failed"
			default:
				services[i].Active = "inactive"
			}
		} else {
			services[i].Active = "inactive"
			services[i].Sub = "stopped"
		}
		if enabled[services[i].Name] {
			services[i].Enabled = "enabled"
		} else {
			services[i].Enabled = "disabled"
		}
	}
	// Descriptions skipped in v1 — `rc-service <name> describe` is one
	// exec per service and the catalog can run into the hundreds.
	return services
}

func runActionOpenRC(req ActionReq) (ok bool, exit int, stderr string) {
	var argv []string
	switch req.Op {
	case "start", "stop", "restart":
		argv = []string{"rc-service", req.Name, req.Op}
	case "enable":
		argv = []string{"rc-update", "add", req.Name}
	case "disable":
		argv = []string{"rc-update", "del", req.Name}
	default:
		return false, 0, "unknown op: " + req.Op
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := st.conn.PrivRunInlineSync(ctx, argv, "services: "+req.Op+" "+req.Name)
	if err != nil {
		return false, -1, err.Error()
	}
	return r.Exit == 0, r.Exit, string(r.Stderr)
}
