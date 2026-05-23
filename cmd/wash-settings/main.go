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
package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"

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

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-settings: assets: %v", err)
	}
	sdk.Main(&sdk.AppDef{
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
		Assets:   sub,
		OnReady:  onReady,
		OnAppMsg: onAppMsg,
	})
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-settings ready instance=%s window=%d", instanceID, windowID)
	// FilePicker support: lets the FE browse the disk for an
	// image via @wash/ui's <FilePicker>. The BE half is the same
	// dispatch helper wash-edit uses.
	sdk.EnableFilePicker(c)
}

func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m := sdk.AsMap(data)
	if m == nil {
		return
	}
	kind, _ := m["kind"].(string)
	id, _ := m["id"].(string)
	switch kind {
	case "settings.read":
		domain, _ := m["domain"].(string)
		doRead(c, id, domain)
	case "settings.write":
		domain, _ := m["domain"].(string)
		// value is a CBOR-decoded map[any]any — re-marshal as JSON.
		doWrite(c, id, domain, m["value"])
	}
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

// readReply ships the decoded JSON as a Go value (map/array/etc.) so
// CBOR encodes it as a structured object. Using json.RawMessage here
// would land as a CBOR byte string — and the router's CBOR→JSON
// normalizer base64-encodes byte strings, breaking the FE decode.
type readReply struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Domain string `json:"domain"`
	Value  any    `json:"value"`
}

type writeReply struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Domain string `json:"domain"`
}

type errReply struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Domain string `json:"domain,omitempty"`
	Code   string `json:"code"`
	Msg    string `json:"msg"`
}

// doRead loads the domain file and replies with settings.value. A
// missing file returns an empty object — the FE applies the same
// defaults the consumer BE does.
func doRead(c *sdk.Conn, id, domain string) {
	path := domainFile(domain)
	if path == "" {
		sendErr(c, "settings.read_err", id, domain, "bad_request", "unknown domain")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			data = []byte("{}")
		} else {
			sendErr(c, "settings.read_err", id, domain, "io", err.Error())
			return
		}
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		log.Printf("wash-settings: %s not valid JSON (%v); returning {}", path, err)
		value = map[string]any{}
	}
	_ = c.SendAppMsg(readReply{
		Kind: "settings.value", ID: id, Domain: domain, Value: value,
	})
}

// doWrite atomically replaces the domain file. CBOR decode produces
// map[any]any (string keys held in interface{}); json.Marshal can't
// handle that, so toJSON() recurses and rewrites maps to map[string]any
// before marshaling.
func doWrite(c *sdk.Conn, id, domain string, value any) {
	path := domainFile(domain)
	if path == "" {
		sendErr(c, "settings.write_err", id, domain, "bad_request", "unknown domain")
		return
	}
	out, err := json.MarshalIndent(wire.ToJSONValue(value), "", "  ")
	if err != nil {
		sendErr(c, "settings.write_err", id, domain, "bad_request", err.Error())
		return
	}
	if len(out) > maxConfigBytes {
		sendErr(c, "settings.write_err", id, domain, "too_large", "config exceeds cap")
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		sendErr(c, "settings.write_err", id, domain, "io", err.Error())
		return
	}
	// Atomic write: temp file in same dir, fsync, rename. The
	// rename is what consumers' fswatch sees — no torn-read window.
	tmp, err := os.CreateTemp(dir, ".desktop-*.json.tmp")
	if err != nil {
		sendErr(c, "settings.write_err", id, domain, "io", err.Error())
		return
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		cleanup()
		sendErr(c, "settings.write_err", id, domain, "io", err.Error())
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		sendErr(c, "settings.write_err", id, domain, "io", err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		sendErr(c, "settings.write_err", id, domain, "io", err.Error())
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		sendErr(c, "settings.write_err", id, domain, "io", err.Error())
		return
	}
	_ = c.SendAppMsg(writeReply{Kind: "settings.write_ok", ID: id, Domain: domain})
}

func sendErr(c *sdk.Conn, kind, id, domain, code, msg string) {
	_ = c.SendAppMsg(errReply{Kind: kind, ID: id, Domain: domain, Code: code, Msg: msg})
}

