// wash-packages — graphical front-end for the host's package
// manager. Search is structured (parsed Package list); install /
// remove / upgrade / upgrade-system are commands streamed into an
// embedded xterm widget via wash-priv's PTY-mode run_inline.
//
// Wire shapes (FE→BE):
//   {kind:"hello"}                              — request backend_status
//   {kind:"search", query}                      — run backend.Search
//   {kind:"run_action", action, pkg?, cols, rows} — dispatch via priv
//   {kind:"action_input", bytes}                — forward terminal input to priv stdin
//   {kind:"action_resize", cols, rows}          — forward resize to priv
//   {kind:"action_cancel"}                      — kill in-flight action
//
// Wire (BE→FE):
//   {kind:"backend_status", name?, error?}      — sent on hello/onReady
//   {kind:"search_result", query, packages}     — reply to search
//   {kind:"action_started", action, pkg?, channel_id} — request accepted
//   {kind:"action_stream", bytes}               — base64 PTY output
//   {kind:"action_done", exit, error?}          — terminal state

package packages

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/sirmick/wash/internal/version"
	"io/fs"
	"log"
	"sync"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

// Icon: lucide "package" — the universal box glyph. Matches the
// metaphor without colliding with file-manager (folder) or services
// (settings) icons.
const packagesIcon = "package"

type appState struct {
	conn    *sdk.Conn
	backend Backend // nil ⇒ no supported pkg manager on this host

	mu     sync.Mutex
	action *action // at most one in-flight per app instance
}

// action tracks the in-flight priv request for the active install /
// remove / upgrade. reqID is what we sent to priv; priv.stream
// replies are broadcast back to the own-FE via SendAppMsg (the app
// runs one window per instance, so there's no per-recipient routing
// to figure out).
type action struct {
	reqID string
	pkg   string
	kind  string
}

var st *appState

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-packages: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.packages",
			Name:            "Packages",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-packages",
			Surface:         sdk.SurfaceWindow,
			Icon:            packagesIcon,
			Accent:          "#5cb088",
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 880, DefaultHeight: 600},
		},
		Assets:       sub,
		OnReady:      onReady,
		OnAppMsgFrom: onAppMsgFrom,
	}
	registry.Register(&registry.App{
		Name:     "wash-packages",
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
	log.Printf("wash-packages ready instance=%s window=%d backend=%s", instanceID, windowID, name)
	st = &appState{conn: c, backend: backend}
	bus := sdk.NewBus(c)
	registerHandlers(bus)
	// Persist the FE's search query so the window reopens on the same
	// search after a reconnect (the canonical save_state→SaveState path).
	sdk.HandlePersist(bus)
	// Push initial backend status; the FE might miss this if it
	// connects after onReady fires, so it also re-requests via hello.
	sendBackendStatus(c, backend)
}

// ----- FE → BE bus -----

type emptyReq struct{}

type searchReq struct {
	Query string `json:"query"`
}

type runActionReq struct {
	Action string `json:"action"` // install | remove | upgrade_pkg | global
	Pkg    string `json:"pkg"`    // empty for global actions
	// PkgKind mirrors Package.Kind from the search result the FE is
	// acting on (""=regular, "task"=tasksel). The backend's argv
	// methods dispatch on it. Named pkg_kind on the wire because
	// plain "kind" is the message-type discriminator the bus
	// consumes before decoding into this struct.
	PkgKind string `json:"pkg_kind"`
	// GlobalID identifies the action when Action == "global"
	// (refresh, upgrade_system, autoremove, …). Backend
	// dispatches via GlobalActionArgv(GlobalID).
	GlobalID string `json:"global_id"`
	Cols     uint16 `json:"cols"`
	Rows     uint16 `json:"rows"`
}

type actionInputReq struct {
	Bytes any `json:"bytes"` // base64 string from xterm onData
}

type actionResizeReq struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func registerHandlers(b *sdk.Bus) {
	sdk.HandleVoid(b, "hello", func(c *sdk.Conn, _ string, _ emptyReq) error {
		sendBackendStatus(c, st.backend)
		return nil
	})
	sdk.HandleVoid(b, "search", func(c *sdk.Conn, _ string, req searchReq) error {
		go runSearch(c, req)
		return nil
	})
	sdk.HandleVoid(b, "run_action", func(c *sdk.Conn, _ string, req runActionReq) error {
		go runAction(c, req)
		return nil
	})
	sdk.HandleVoid(b, "action_input", func(_ *sdk.Conn, _ string, req actionInputReq) error {
		st.mu.Lock()
		a := st.action
		st.mu.Unlock()
		if a == nil {
			return nil
		}
		b := sdk.DecodeBase64(req.Bytes)
		if len(b) == 0 {
			return nil
		}
		_ = st.conn.SendAppMsgTo(wire.Recipient{AppID: sdk.PrivAppID}, map[string]any{
			"kind":   "stdin",
			"req_id": a.reqID,
			"bytes":  base64.StdEncoding.EncodeToString(b),
		})
		return nil
	})
	sdk.HandleVoid(b, "action_resize", func(_ *sdk.Conn, _ string, req actionResizeReq) error {
		st.mu.Lock()
		a := st.action
		st.mu.Unlock()
		if a == nil || req.Cols == 0 || req.Rows == 0 {
			return nil
		}
		_ = st.conn.SendAppMsgTo(wire.Recipient{AppID: sdk.PrivAppID}, map[string]any{
			"kind":   "resize",
			"req_id": a.reqID,
			"cols":   req.Cols,
			"rows":   req.Rows,
		})
		return nil
	})
	sdk.HandleVoid(b, "action_cancel", func(_ *sdk.Conn, _ string, _ emptyReq) error {
		st.mu.Lock()
		a := st.action
		st.mu.Unlock()
		if a == nil {
			return nil
		}
		_ = st.conn.SendAppMsgTo(wire.Recipient{AppID: sdk.PrivAppID}, map[string]any{
			"kind":   "cancel",
			"req_id": a.reqID,
		})
		return nil
	})
}

func sendBackendStatus(c *sdk.Conn, backend Backend) {
	msg := map[string]any{"kind": "backend_status"}
	if backend == nil {
		msg["error"] = "no supported package manager found (looked for apt/dnf/apk)"
	} else {
		msg["name"] = backend.Name()
		// Ship the global-actions catalog with status so the FE
		// can build the Actions dropdown without a separate round-
		// trip. Backend authors order the list; the FE preserves
		// that order.
		msg["global_actions"] = backend.GlobalActions()
	}
	_ = c.SendAppMsg(msg)
}

func runSearch(c *sdk.Conn, req searchReq) {
	if st.backend == nil {
		_ = c.SendAppMsg(map[string]any{
			"kind":  "search_result",
			"query": req.Query,
			"error": "no backend",
		})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	packages, err := st.backend.Search(ctx, req.Query)
	out := map[string]any{
		"kind":     "search_result",
		"query":    req.Query,
		"packages": packages,
	}
	if err != nil {
		out["error"] = err.Error()
	}
	_ = c.SendAppMsg(out)
}

func runAction(c *sdk.Conn, req runActionReq) {
	if st.backend == nil {
		_ = c.SendAppMsg(map[string]any{
			"kind":  "action_done",
			"exit":  -1,
			"error": "no backend",
		})
		return
	}
	// Refuse overlapping actions — one terminal pane per app, one
	// PTY-bound priv request at a time. The FE disables action
	// buttons while an action is live, but enforce server-side too.
	st.mu.Lock()
	if st.action != nil {
		st.mu.Unlock()
		_ = c.SendAppMsg(map[string]any{
			"kind":  "action_done",
			"exit":  -1,
			"error": "another action is already running",
		})
		return
	}

	var argv []string
	switch req.Action {
	case "install":
		argv = st.backend.InstallArgv(req.Pkg, req.PkgKind)
	case "remove":
		argv = st.backend.RemoveArgv(req.Pkg, req.PkgKind)
	case "upgrade_pkg":
		argv = st.backend.UpgradePkgArgv(req.Pkg, req.PkgKind)
	case "global":
		argv = st.backend.GlobalActionArgv(req.GlobalID)
	default:
		st.mu.Unlock()
		_ = c.SendAppMsg(map[string]any{
			"kind":  "action_done",
			"exit":  -1,
			"error": "unknown action: " + req.Action,
		})
		return
	}
	// Backends return nil to signal "this kind doesn't support this
	// action" (e.g. tasksel has no per-task upgrade). Surface as a
	// user-visible error rather than spawning an empty argv.
	if len(argv) == 0 {
		st.mu.Unlock()
		_ = c.SendAppMsg(map[string]any{
			"kind":  "action_done",
			"exit":  -1,
			"error": fmt.Sprintf("%s not supported for %s", req.Action, kindLabel(req.PkgKind)),
		})
		return
	}

	reqID := "pkg-" + randHex(8)
	st.action = &action{reqID: reqID, pkg: req.Pkg, kind: req.Action}
	st.mu.Unlock()

	_ = c.SendAppMsg(map[string]any{
		"kind":      "action_started",
		"action":    req.Action,
		"pkg":       req.Pkg,
		"global_id": req.GlobalID,
		"argv":      argv,
	})

	// DEBIAN_FRONTEND=noninteractive keeps debconf from blocking on
	// dialogs; -y already handles the standard prompts. Passed via
	// the Env field which priv merges into the child's environment
	// (and allowlists via sudo --preserve-env).
	_ = c.SendAppMsgTo(wire.Recipient{AppID: sdk.PrivAppID}, map[string]any{
		"kind":   "run_inline",
		"req_id": reqID,
		"argv":   argv,
		"env":    map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		"reason": "wash-packages: " + req.Action + " " + req.Pkg,
		"pty":    true,
		"cols":   req.Cols,
		"rows":   req.Rows,
	})
}

// onAppMsgFrom — accept priv streams keyed to our active action.
// Forwards bytes (already base64) to the requester FE and tears down
// the in-flight slot on priv.result.
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
	a := st.action
	st.mu.Unlock()
	if a == nil || a.reqID != reqID {
		return
	}

	switch sdk.ToString(m["kind"]) {
	case "priv.stream":
		// Pass through as base64 — the FE will atob and write into
		// xterm. Stream label is dropped: PTY mode merges
		// stdout/stderr at the kernel level anyway.
		_ = st.conn.SendAppMsg(map[string]any{
			"kind":  "action_stream",
			"bytes": m["bytes"],
		})
	case "priv.result":
		exit := int(sdk.ToInt64(m["exit_code"]))
		errMsg := sdk.ToString(m["error"])
		_ = st.conn.SendAppMsg(map[string]any{
			"kind":  "action_done",
			"exit":  exit,
			"error": errMsg,
		})
		st.mu.Lock()
		st.action = nil
		st.mu.Unlock()
	case "rejected":
		_ = st.conn.SendAppMsg(map[string]any{
			"kind":  "action_done",
			"exit":  -1,
			"error": "rejected: " + sdk.ToString(m["reason"]),
		})
		st.mu.Lock()
		st.action = nil
		st.mu.Unlock()
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// kindLabel turns a Package.Kind into a human-readable noun for
// error messages. Future kinds (group, flatpak, …) extend here.
func kindLabel(kind string) string {
	switch kind {
	case "task":
		return "tasksel tasks"
	case "":
		return "packages"
	default:
		return kind
	}
}
