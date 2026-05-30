// wash-vscode — the VS Code manager: a singleton window that BOTH
// owns code-server (the single process + ingress route for the
// session) AND is the control panel. Folding the former background
// daemon into this window means there are just two apps — this and
// the hidden workbench — and a simple lifecycle: code-server lives as
// long as this window is open, and closing it (after a prompt) tears
// down code-server and every workbench window.
//
// Owns: detect/install/upgrade (PTY stream → install.go), the
// code-server process + ingress (server.go). Talks to:
//   - its own control-panel FE (own-FE app_msg): status / start /
//     stop / install / update / open_window / force_quit, plus fs.*
//     for the folder picker (EnableFilePicker).
//   - workbench windows (cross-app app_msg): subscribe / ensure.
// Pushes status / log / ready / exited / error to its FE and to every
// subscribed workbench.
package vscode

import (
	"context"
	"embed"
	"encoding/base64"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.1.0"

// workbenchAppID is the hidden window this manager spawns per folder.
const workbenchAppID = "com.wash.vscode.workbench"

const logRingMax = 4096

// managerIcon names a symbol in the shell's icon sprite (/icons.svg);
// the chrome renders it as <use href="icons.svg#<icon>"> and tints it
// with the manifest accent. square-code reads as "the code IDE app".
// Add the name to web/shell/build-icons.mjs's ICONS list.
const managerIcon = "square-code"

var def *sdk.AppDef

type manager struct {
	bus        *sdk.Bus
	root       string
	instanceID string

	launchMu sync.Mutex

	mu               sync.Mutex
	proc             *exec.Cmd
	sock             string
	path             string
	installing       bool
	latest           string
	subs             map[string]struct{} // subscribed workbench instance ids
	logBuf           []string
	pendingFolders   []string
	folderByInstance map[string]string
}

// theManager is the live singleton; the package-level OnSpawnResult
// callback routes through it.
var theManager *manager

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-vscode: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.vscode",
			Name:            "VS Code Manager",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-vscode",
			Surface:         sdk.SurfaceWindow,
			Icon:            managerIcon,
			Accent:          "#007acc",
			// Singleton: one manager owns code-server; workbench
			// windows address it cross-app by this id.
			Instancing:   sdk.InstancingSingleton,
			Capabilities: []string{sdk.CapSpawn}, // spawns workbench windows
			Window:       &sdk.WindowHints{DefaultWidth: 560, DefaultHeight: 460},
		},
		Assets:           sub,
		OnReady:          onReady,
		OnSpawnResult:    onSpawnResult,
		OnCloseRequested: onCloseRequested,
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
		root:             c.Session().Root,
		instanceID:       instanceID,
		subs:             map[string]struct{}{},
		folderByInstance: map[string]string{},
	}
	// Folder picker (fs.*) first; the Bus then wraps it for everything
	// else.
	sdk.EnableFilePicker(c)
	m.bus = sdk.NewBus(c)
	theManager = m
	registerHandlers(m)

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

	// Opening the manager launches a VS Code workspace at the last
	// folder used (or home on first run) — the manager is the hub,
	// but you get an editor straight away.
	go func() {
		folder := m.readLastFolder()
		if folder == "" {
			folder = m.defaultFolder()
		}
		if err := m.openWorkspace(folder); err != nil {
			log.Printf("wash-vscode: auto-open workspace: %v", err)
		}
	}()

	log.Printf("wash-vscode (manager) ready instance=%s window=%d root=%q", instanceID, windowID, m.root)
}

// onCloseRequested always prompts: ask the FE to confirm, and veto the
// close for now. The FE's confirm sends force_quit, which tears
// everything down.
func onCloseRequested(c *sdk.Conn, _ uint32) bool {
	_ = c.SendAppMsg(map[string]any{"kind": "confirm_close"})
	return false
}

// ----- request types -----

type emptyReq struct{}
type installReq struct {
	Version string `json:"version"`
}
type openWindowReq struct {
	Folder string `json:"folder"`
}

func registerHandlers(m *manager) {
	b := m.bus

	// --- control-panel FE commands (own-FE) ---
	sdk.HandleVoid(b, "status", func(_ *sdk.Conn, _ string, _ emptyReq) error {
		go m.emitStatusToFE()
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
	sdk.HandleVoid(b, "open_window", func(_ *sdk.Conn, _ string, req openWindowReq) error {
		return m.openWorkspace(req.Folder)
	})
	sdk.HandleVoid(b, "force_quit", func(c *sdk.Conn, _ string, _ emptyReq) error {
		go m.quit(c)
		return nil
	})

	// --- workbench cross-app messages ---
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

// onSpawnResult binds a pending open_window folder to the freshly
// spawned workbench instance.
func onSpawnResult(_ *sdk.Conn, _, instanceID string, err error) {
	m := theManager
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pendingFolders) == 0 {
		return
	}
	folder := m.pendingFolders[0]
	m.pendingFolders = m.pendingFolders[1:]
	if err != nil || instanceID == "" {
		return
	}
	m.folderByInstance[instanceID] = folder
}

// ----- orchestration -----

// openWorkspace spawns a workbench window for folder, remembering it
// as the last-opened so the manager reopens it next launch.
func (m *manager) openWorkspace(folder string) error {
	m.mu.Lock()
	m.pendingFolders = append(m.pendingFolders, folder)
	m.mu.Unlock()
	m.writeLastFolder(folder)
	return m.bus.Conn().SpawnRequest(workbenchAppID)
}

func (m *manager) startServer() {
	if _, err := m.launch(context.Background()); err != nil {
		m.broadcast(errPayload(err))
		return
	}
	m.broadcastStatus()
}

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
	m.mu.Lock()
	folder, ok := m.folderByInstance[fromInst]
	m.mu.Unlock()
	if !ok {
		folder = m.defaultFolder()
	}
	m.sendTo(fromInst, map[string]any{"kind": "ready", "path": path, "folder": folder})
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

// quit tears everything down: tell workbenches to close, kill
// code-server, then exit the process. The router reaps the exit and
// destroys this window; code-server (our child) is already gone.
func (m *manager) quit(_ *sdk.Conn) {
	m.broadcast(map[string]any{"kind": "shutdown"})
	time.Sleep(150 * time.Millisecond) // let workbenches self-close
	m.stop()
	os.Exit(0)
}

// ----- fan-out (own FE + subscribed workbenches) -----

func (m *manager) sendTo(inst string, payload map[string]any) {
	if inst == "" {
		return
	}
	_ = m.bus.Conn().SendAppMsgTo(wire.Recipient{InstanceID: inst}, payload)
}

func (m *manager) broadcast(payload map[string]any) {
	_ = m.bus.Conn().SendAppMsg(payload) // own control-panel FE
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

// emitStatusToFE replies to the manager FE's status request.
func (m *manager) emitStatusToFE() { _ = m.bus.Conn().SendAppMsg(m.statusPayload()) }

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
