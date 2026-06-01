// wash-settings — the singleton settings UI. Reads/writes user
// preferences in ~/.config/wash/, and hosts control panels for the
// background services (VS Code, the X/Wayland compositor) over a
// generic cross-app relay.
//
// # Architecture
//
// FE picks a wallpaper image via @wash/ui's <FilePicker>; the BE
// opts that picker in by calling sdk.EnableFilePicker(c) — same
// path wash-edit uses. settings.read/settings.write are this app's
// own app_msg surface for round-tripping desktop.json.
//
// Writes are atomic (temp file in same dir + fsync + rename) so a
// crash mid-write can't leave the consumer reading a half-truncated
// JSON file. fswatch on the consumer side sees a single rename
// event and re-reads cleanly.
//
// Service panels (docs/SETTINGS.md §4). The Developer (VS Code) and
// Display (compositor) panels are host-rendered over a thin relay:
//   - svc.send{app,payload}    FE→BE→service (cross-app, From-attested)
//   - svc.restart{app}         FE→BE; BE calls RestartApp (CapRestart),
//     replies svc.restart_done{app,ok,error?}
//   - svc.recv{app,payload}    BE→FE; any cross-app reply from a service
//     wrapped with its app id so the FE can
//     route it to the right panel.
//
// The BE hardcodes no service verbs — the panels own that vocabulary.
package settings

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const (
	version = "0.8.0"

	// maxConfigBytes caps the JSON payload written through
	// settings.write. desktop.json is a few hundred bytes; this is
	// a sanity guard against the FE shipping something nuts.
	maxConfigBytes = 64 * 1024
)

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-settings: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.settings",
			Name:            "Settings",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-settings",
			Surface:         sdk.SurfaceWindow,
			Icon:            "settings",
			Accent:          "#7c8fa8",
			Instancing:      sdk.InstancingSingleton,
			Window:          &sdk.WindowHints{DefaultWidth: 760, DefaultHeight: 520},
			// restart lets the Display panel cycle the compositor
			// (and any background service) via the router's app.restart.
			Capabilities: []string{sdk.CapRestart},
		},
		Assets:  sub,
		OnReady: onReady,
		// Cross-app replies from a service (vscode/display status, log,
		// etc.) carry service-defined kinds the bus doesn't register;
		// they fall through to this catch-all, which wraps them as
		// svc.recv for the FE. Set before NewBus so the bus chains to it.
		OnAppMsgFrom: onSvcReply,
	}
	registry.Register(&registry.App{
		Name:     "wash-settings",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

var bus *sdk.Bus

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-settings ready instance=%s window=%d", instanceID, windowID)
	// FilePicker support: lets the FE browse the disk for an image
	// via @wash/ui's <FilePicker>. EnableFilePicker installs its own
	// OnAppMsg chain on c; the bus then wraps that so fs.* messages
	// still route to the picker.
	sdk.EnableFilePicker(c)
	bus = sdk.NewBus(c)
	registerHandlers(bus)
}

type readReq struct {
	Domain string `json:"domain"`
}

type writeReq struct {
	Domain string `json:"domain"`
	Value  any    `json:"value"`
}

type writeResp struct {
	Domain string `json:"domain"`
}

func registerHandlers(b *sdk.Bus) {
	// settings.read returns `settings.value` (not settings.read_ok)
	// — non-conventional, so emit explicitly via HandleVoid.
	sdk.HandleVoid(b, "settings.read", func(_ *sdk.Conn, id string, req readReq) error {
		path := domainFile(req.Domain)
		if path == "" {
			return b.Emit("settings.read_err", map[string]any{
				"id": id, "domain": req.Domain, "code": "bad_request", "msg": "unknown domain",
			})
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				data = []byte("{}")
			} else {
				return b.Emit("settings.read_err", map[string]any{
					"id": id, "domain": req.Domain, "code": "io", "msg": err.Error(),
				})
			}
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			log.Printf("wash-settings: %s not valid JSON (%v); returning {}", path, err)
			value = map[string]any{}
		}
		return b.Emit("settings.value", map[string]any{
			"id": id, "domain": req.Domain, "value": value,
		})
	})

	sdk.Handle(b, "settings.write", func(_ *sdk.Conn, _ string, req writeReq) (writeResp, error) {
		if err := doWrite(req.Domain, req.Value); err != nil {
			return writeResp{}, err
		}
		return writeResp{Domain: req.Domain}, nil
	})

	// --- service relay (Developer + Display panels) ---

	// svc.send forwards an opaque payload to a background service by
	// app id. The router stamps settings as the From, so the service's
	// subscribe records this instance and routes its replies back here
	// (→ onSvcReply → svc.recv). A send to an unregistered app id is
	// dropped router-side; the panel treats silence as "unavailable".
	sdk.HandleVoid(b, "svc.send", func(c *sdk.Conn, _ string, req svcSendReq) error {
		if req.App == "" {
			return nil
		}
		return c.SendAppMsgTo(wire.Recipient{AppID: req.App}, req.Payload)
	})

	// svc.restart cycles a background singleton via the router's
	// app.restart verb (requires CapRestart). RestartApp blocks on the
	// reply, so run it off the reader goroutine. not_found surfaces as
	// ok=false with the error — the Display panel reads that as "not
	// installed".
	sdk.HandleVoid(b, "svc.restart", func(c *sdk.Conn, _ string, req svcRestartReq) error {
		if req.App == "" {
			return nil
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			inst, err := c.RestartApp(ctx, req.App)
			out := map[string]any{"kind": "svc.restart_done", "app": req.App, "ok": err == nil}
			if err != nil {
				out["error"] = err.Error()
			} else {
				out["instance"] = inst
			}
			_ = c.SendAppMsg(out)
		}()
		return nil
	})
}

type svcSendReq struct {
	App     string         `json:"app"`
	Payload map[string]any `json:"payload"`
}

type svcRestartReq struct {
	App string `json:"app"`
}

// onSvcReply wraps any cross-app message from a background service into
// a svc.recv envelope tagged with the sender's app id, so the FE panels
// can route status / log / state pushes to the right panel. Bus-handled
// kinds never reach here; this is the fallthrough for service-defined
// reply kinds (status, log, ready, exited, error, display.state, …).
func onSvcReply(c *sdk.Conn, _ uint32, data any, from wire.Sender) {
	m, ok := data.(map[string]any)
	if !ok {
		return
	}
	_ = c.SendAppMsg(map[string]any{
		"kind":    "svc.recv",
		"app":     from.AppID,
		"payload": m,
	})
}

// configDir resolves $XDG_CONFIG_HOME/wash, falling back to
// $HOME/.config/wash. Returns "" only if both env vars are unset.
func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "wash")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "wash")
	}
	return ""
}

// domainFile maps a settings domain ("desktop") to its on-disk file.
// New domains added here gain read/write for free.
func domainFile(domain string) string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	switch domain {
	case "desktop":
		return filepath.Join(dir, "desktop.json")
	}
	return ""
}

// doWrite atomically replaces the domain file. value is the
// JSON-decoded payload (map[string]any for objects); json.Marshal
// handles it directly.
func doWrite(domain string, value any) error {
	path := domainFile(domain)
	if path == "" {
		return sdk.Errf("bad_request", "unknown domain")
	}
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return sdk.Err{Code: "bad_request", Msg: err.Error()}
	}
	if len(out) > maxConfigBytes {
		return sdk.Errf("too_large", "config exceeds cap")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	// Atomic write: temp file in same dir, fsync, rename. The
	// rename is what consumers' fswatch sees — no torn-read window.
	tmp, err := os.CreateTemp(dir, ".desktop-*.json.tmp")
	if err != nil {
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		cleanup()
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	return nil
}
