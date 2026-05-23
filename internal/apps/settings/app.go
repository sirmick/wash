// wash-settings — the singleton settings UI. Reads/writes user
// preferences in ~/.config/wash/. Each consumer app fswatches its
// own file (no central settings daemon, no router-side service);
// today the only consumer is wash-session, which watches
// desktop.json.
//
// Architecture
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

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

const (
	version = "0.0.0"

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
			Instancing:      sdk.InstancingSingleton,
			Window:          &sdk.WindowHints{DefaultWidth: 760, DefaultHeight: 520},
		},
		Assets:  sub,
		OnReady: onReady,
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
	Domain string `cbor:"domain"`
}

type writeReq struct {
	Domain string `cbor:"domain"`
	Value  any    `cbor:"value"`
}

type writeResp struct {
	Domain string `cbor:"domain"`
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

// doWrite atomically replaces the domain file. value is a
// CBOR-decoded map[any]any; wire.ToJSONValue normalises it for the
// JSON marshaller.
func doWrite(domain string, value any) error {
	path := domainFile(domain)
	if path == "" {
		return sdk.Errf("bad_request", "unknown domain")
	}
	out, err := json.MarshalIndent(wire.ToJSONValue(value), "", "  ")
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

