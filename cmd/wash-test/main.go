// wash-test — control-panel + event-log app used for manual UI
// exercising and Playwright-driven e2e tests. Hidden from the
// launcher catalog (manifest.Hidden=true); spawned either by
// --initial-app or by another app's spawn.request.
package main

import (
	"embed"
	"io/fs"
	"log"
	"sync"

	"github.com/sirmick/wash/internal/sdk"
)

//go:embed all:assets
var assetsFS embed.FS

const version = "0.0.0"

// state is per-process, shared between callbacks. The test app is
// single-window per process (instancing=multi), so this is fine.
type state struct {
	mu             sync.Mutex
	conn           *sdk.Conn
	vetoNextClose  bool
	pingSeq        int
	closeReqAllow  bool // last decision returned, for logging
}

var st state

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-test: assets: %v", err)
	}
	sdk.Main(&sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.test",
			Name:            "wash test",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-test",
			Surface:         sdk.SurfaceWindow,
			Icon:            testIcon,
			Instancing:      sdk.InstancingMulti,
			Capabilities:    []string{sdk.CapSpawn},
			Window:          &sdk.WindowHints{DefaultWidth: 560, DefaultHeight: 480},
			Hidden:          true,
		},
		Assets:        sub,
		OnReady:       onReady,
		OnMapped:      onMapped,
		OnFocus:       onFocus,
		OnUnfocus:     onUnfocus,
		OnCloseRequested: onCloseRequested,
		OnAppMsg:      onAppMsg,
		OnSpawnResult: onSpawnResult,
	})
}

// onReady captures the Conn so callbacks can write back.
func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	st.mu.Lock()
	st.conn = c
	st.mu.Unlock()
	log.Printf("wash-test ready instance=%s window=%d", instanceID, windowID)
}

func onMapped(c *sdk.Conn, win uint32) {
	log.Printf("wash-test mapped win=%d", win)
	sendEvent(c, map[string]any{"kind": "event", "type": "mapped", "win": win})
}

func onFocus(c *sdk.Conn, win uint32) {
	log.Printf("wash-test focus win=%d", win)
	sendEvent(c, map[string]any{"kind": "event", "type": "focus", "win": win})
}

func onUnfocus(c *sdk.Conn, win uint32) {
	log.Printf("wash-test unfocus win=%d", win)
	sendEvent(c, map[string]any{"kind": "event", "type": "unfocus", "win": win})
}

// onCloseRequested honors the veto-next-close flag (single-shot — the
// flag is cleared after one use, so a second close click still
// dismisses the window).
func onCloseRequested(c *sdk.Conn, win uint32) bool {
	st.mu.Lock()
	allow := !st.vetoNextClose
	st.vetoNextClose = false
	st.closeReqAllow = allow
	st.mu.Unlock()
	log.Printf("wash-test close_requested win=%d allow=%v", win, allow)
	sendEvent(c, map[string]any{"kind": "event", "type": "close_requested", "win": win, "allow": allow})
	return allow
}

func onSpawnResult(c *sdk.Conn, appID, instanceID string, err error) {
	if err != nil {
		log.Printf("wash-test spawn_err %s: %v", appID, err)
		// err is "code: msg" from the SDK; split for the FE.
		sendEvent(c, map[string]any{
			"kind":  "event",
			"type":  "spawn_err",
			"app_id": appID,
			"code":  "forbidden_or_unknown",
			"msg":   err.Error(),
		})
		return
	}
	log.Printf("wash-test spawn_ok %s instance=%s", appID, instanceID)
	sendEvent(c, map[string]any{
		"kind":        "event",
		"type":        "spawn_ok",
		"app_id":      appID,
		"instance_id": instanceID,
	})
}

// onAppMsg handles the FE's control messages. data is the JSON-shaped
// object from the FE (CBOR-decoded by the SDK into map[any]any).
func onAppMsg(c *sdk.Conn, win uint32, data any) {
	m, ok := data.(map[any]any)
	if !ok {
		log.Printf("wash-test app_msg unexpected shape %T", data)
		return
	}
	kind, _ := m["kind"].(string)
	switch kind {
	case "ping":
		st.mu.Lock()
		st.pingSeq++
		seq := st.pingSeq
		st.mu.Unlock()
		log.Printf("wash-test ping → pong seq=%d", seq)
		sendEvent(c, map[string]any{"kind": "pong", "seq": seq})
	case "set_title":
		title, _ := m["title"].(string)
		if title != "" {
			if err := c.SetTitle(title); err != nil {
				log.Printf("wash-test set_title err: %v", err)
			}
			sendEvent(c, map[string]any{"kind": "event", "type": "title_set", "title": title})
		}
	case "set_veto_close":
		on, _ := m["on"].(bool)
		st.mu.Lock()
		st.vetoNextClose = on
		st.mu.Unlock()
		log.Printf("wash-test veto_next_close=%v", on)
	case "spawn":
		appID, _ := m["app_id"].(string)
		if appID != "" {
			if err := c.SpawnRequest(appID); err != nil {
				log.Printf("wash-test spawn err: %v", err)
			}
		}
	case "fe_event":
		log.Printf("wash-test fe_event %v", m["type"])
	default:
		log.Printf("wash-test unhandled kind=%q msg=%+v", kind, m)
	}
}

func sendEvent(c *sdk.Conn, payload map[string]any) {
	if err := c.SendAppMsg(payload); err != nil {
		log.Printf("wash-test SendAppMsg: %v", err)
	}
}

// testIcon — small "T" mark to distinguish the test app from About.
const testIcon = "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'><rect x='2' y='3' width='12' height='2' fill='%23eee'/><rect x='7' y='3' width='2' height='10' fill='%23eee'/></svg>"
