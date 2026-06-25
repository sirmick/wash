// wash-vscode — the VS Code service: a windowless background singleton
// that owns code-server (the single process + ingress route for the
// session) and brokers it to workbench windows. It has no window and no
// FE of its own; its control UI (status / start / stop / install /
// update / restart) lives in the settings app, driven over cross-app
// app_msg. Restart is the router's app.restart verb (docs/SETTINGS.md
// §5), not a message this service handles.
//
// Owns: detect/install/upgrade (PTY stream → install.go), the
// code-server process + ingress (server.go). Talks to:
//   - the settings panel (cross-app app_msg): status / start / stop /
//     install / update, via subscribe.
//   - workbench windows (cross-app app_msg): subscribe / ensure. Each
//     workbench picks its own folder (folder prompt on cold launch) and
//     opens path?folder=… ; this service just ensures code-server is up
//     and hands back the ingress path.
//
// Pushes status / log / ready / exited / error to every subscriber.
package vscode

import (
	"context"
	"embed"
	"encoding/base64"
	"github.com/sirmick/wash/internal/version"
	"io/fs"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// assetsFS embeds the settings-panel bundle (panel.js) the Makefile
// stages from apps/vscode/fe/dist. The service has no window of its
// own; this is only the panel the settings app hosts.
//
//go:embed all:assets
var assetsFS embed.FS

const logRingMax = 4096

var def *sdk.AppDef

type manager struct {
	bus        *sdk.Bus
	root       string
	instanceID string

	launchMu sync.Mutex

	mu         sync.Mutex
	proc       *exec.Cmd
	sock       string
	path       string
	installing bool
	latest     string
	subs       map[string]struct{} // subscribed instance ids (settings panel + workbenches)
	logBuf     []string
}

// theManager is the live singleton.
var theManager *manager

func init() {
	// Background service: no window. It does, however, supply the
	// settings "Developer" panel — panel.js embedded via assetsFS, shipped
	// raw in the probe and loaded on demand by the settings host.
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Printf("wash-vscode: assets sub: %v", err)
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.vscode",
			Name:            "VS Code Service",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			// Singleton: one service owns code-server; the settings
			// panel and workbench windows address it cross-app by id.
			Instancing: sdk.InstancingSingleton,
			// Developer panel for the settings app (docs/SETTINGS.md).
			SettingsPanel: &sdk.SettingsPanel{
				Section: "Developer",
				Element: "wash-settings-panel-vscode",
			},
		},
		Assets:  sub,
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-vscode",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	m := &manager{
		root:       c.Session().Root,
		instanceID: instanceID,
		subs:       map[string]struct{}{},
	}
	m.bus = sdk.NewBus(c)
	theManager = m
	registerHandlers(m)

	// The router stops us with SIGTERM; without a handler we'd die before
	// killing code-server, orphaning its node workers (Pdeathsig only reaps
	// the direct child). Group-kill the whole code-server tree on shutdown.
	sdk.OnTerminate(m.killChild)

	// Best-effort newest-release lookup once at startup (cached, so we
	// don't hit GitHub's rate limit on every status request).
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if v, err := latestVersion(ctx); err == nil {
			m.mu.Lock()
			m.latest = v
			m.mu.Unlock()
			m.broadcastStatus()
		}
	}()

	log.Printf("wash-vscode (service) ready instance=%s root=%q", instanceID, m.root)
}

// ----- request types -----

type emptyReq struct{}
type installReq struct {
	Version string `json:"version"`
}

func registerHandlers(m *manager) {
	b := m.bus

	// --- control commands (settings panel, cross-app) ---
	sdk.HandleFromVoid(b, "status", func(_ *sdk.Conn, _ string, _ emptyReq, from wire.Sender) error {
		go m.sendTo(from.InstanceID, m.statusPayload())
		return nil
	})
	sdk.HandleVoid(b, "start", func(_ *sdk.Conn, _ string, _ emptyReq) error {
		go m.startServer()
		return nil
	})
	sdk.HandleVoid(b, "stop", func(_ *sdk.Conn, _ string, _ emptyReq) error {
		m.stop()
		m.broadcastStatus()
		return nil
	})
	sdk.HandleVoid(b, "install", func(_ *sdk.Conn, _ string, req installReq) error {
		go m.runInstall(req.Version)
		return nil
	})
	sdk.HandleVoid(b, "update", func(_ *sdk.Conn, _ string, _ emptyReq) error {
		m.mu.Lock()
		v := m.latest
		m.mu.Unlock()
		go m.runInstall(v)
		return nil
	})

	// --- subscriber lifecycle (settings panel + workbench windows) ---
	sdk.HandleFromVoid(b, "subscribe", func(_ *sdk.Conn, _ string, _ emptyReq, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		m.mu.Lock()
		m.subs[from.InstanceID] = struct{}{}
		replay := append([]string(nil), m.logBuf...)
		m.mu.Unlock()
		m.sendTo(from.InstanceID, m.statusPayload())
		for _, chunk := range replay {
			m.sendTo(from.InstanceID, map[string]any{"kind": "log", "bytes": chunk})
		}
		return nil
	})
	sdk.HandleFromVoid(b, "unsubscribe", func(_ *sdk.Conn, _ string, _ emptyReq, from wire.Sender) error {
		m.mu.Lock()
		delete(m.subs, from.InstanceID)
		m.mu.Unlock()
		return nil
	})
	sdk.HandleFromVoid(b, "ensure", func(_ *sdk.Conn, _ string, _ emptyReq, from wire.Sender) error {
		go m.handleEnsure(from.InstanceID)
		return nil
	})
}

// ----- orchestration -----

func (m *manager) startServer() {
	if _, err := m.launch(context.Background()); err != nil {
		m.broadcast(errPayload(err))
		return
	}
	m.broadcastStatus()
}

// handleEnsure brings code-server up (idempotent) and hands the
// requesting workbench the ingress path. The workbench owns its own
// folder — it opens path?folder=… — so this reply carries only the
// path, not a folder.
func (m *manager) handleEnsure(fromInst string) {
	st := detect()
	if !st.Installed {
		m.sendTo(fromInst, m.statusPayload())
		return
	}
	path, err := m.launch(context.Background())
	if err != nil {
		m.sendTo(fromInst, errPayload(err))
		return
	}
	m.sendTo(fromInst, map[string]any{"kind": "ready", "path": path})
	m.broadcastStatus()
}

func (m *manager) runInstall(version string) {
	m.mu.Lock()
	if m.installing {
		m.mu.Unlock()
		return
	}
	m.installing = true
	m.logBuf = nil
	m.mu.Unlock()
	m.broadcastStatus()

	err := runInstallPTY(context.Background(), version, func(b []byte) { m.streamLog(b) })

	m.mu.Lock()
	m.installing = false
	wasRunning := m.proc != nil
	m.mu.Unlock()

	if err != nil {
		m.broadcast(errPayload(err))
		m.broadcastStatus()
		return
	}
	m.broadcastStatus()
	// On an update, restart so the new binary takes effect.
	if wasRunning {
		m.stop()
	}
	path, lerr := m.launch(context.Background())
	if lerr != nil {
		m.broadcast(errPayload(lerr))
		return
	}
	m.broadcast(map[string]any{"kind": "ready", "path": path})
}

// ----- fan-out (every subscriber) -----

func (m *manager) sendTo(inst string, payload map[string]any) {
	if inst == "" {
		return
	}
	_ = m.bus.Conn().SendAppMsgTo(wire.Recipient{InstanceID: inst}, payload)
}

func (m *manager) broadcast(payload map[string]any) {
	m.mu.Lock()
	subs := make([]string, 0, len(m.subs))
	for k := range m.subs {
		subs = append(subs, k)
	}
	m.mu.Unlock()
	for _, inst := range subs {
		m.sendTo(inst, payload)
	}
}

func (m *manager) broadcastStatus() { m.broadcast(m.statusPayload()) }

func (m *manager) streamLog(b []byte) {
	enc := base64.StdEncoding.EncodeToString(b)
	m.mu.Lock()
	if len(m.logBuf) >= logRingMax {
		m.logBuf = m.logBuf[len(m.logBuf)-logRingMax+1:]
	}
	m.logBuf = append(m.logBuf, enc)
	m.mu.Unlock()
	m.broadcast(map[string]any{"kind": "log", "bytes": enc})
}

func (m *manager) statusPayload() map[string]any {
	st := detect()
	m.mu.Lock()
	running := m.path != ""
	installing := m.installing
	latest := m.latest
	m.mu.Unlock()
	return map[string]any{
		"kind":       "status",
		"installed":  st.Installed,
		"version":    st.Version,
		"managed":    st.Managed,
		"arch_ok":    st.ArchOK,
		"latest":     latest,
		"running":    running,
		"installing": installing,
	}
}

func errPayload(err error) map[string]any {
	return map[string]any{"kind": "error", "msg": err.Error()}
}
