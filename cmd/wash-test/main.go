// wash-test — control-panel + event-log app used for manual UI
// exercising and Playwright-driven e2e tests. Hidden from the
// launcher catalog (manifest.Hidden=true); spawned either by
// --initial-app or by another app's spawn.request.
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log"
	"sync"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
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
		OnReady:            onReady,
		OnMapped:           onMapped,
		OnFocus:            onFocus,
		OnUnfocus:          onUnfocus,
		OnResize:           onResize,
		OnState:            onState,
		OnCloseRequested:   onCloseRequested,
		OnAppMsg:           onAppMsg,
		OnSpawnResult:      onSpawnResult,
		OnClipboardChanged: onClipboardChanged,
	})
}

// onReady captures the Conn so callbacks can write back. Also opts
// the test app into the FilePicker bridge — one call registers fs.*
// handlers that the picker FE addresses via sendAppMsg to this BE.
// No per-message plumbing required.
func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	st.mu.Lock()
	st.conn = c
	st.mu.Unlock()
	sdk.EnableFilePicker(c)
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

func onResize(c *sdk.Conn, win uint32, w, h uint32) {
	log.Printf("wash-test resize win=%d %dx%d", win, w, h)
	sendEvent(c, map[string]any{"kind": "event", "type": "resize", "win": win, "w": w, "h": h})
}

func onState(c *sdk.Conn, win uint32, state string) {
	log.Printf("wash-test state win=%d %s", win, state)
	sendEvent(c, map[string]any{"kind": "event", "type": "state", "win": win, "state": state})
}

func onClipboardChanged(c *sdk.Conn, mime string) {
	log.Printf("wash-test clipboard.changed mime=%s", mime)
	sendEvent(c, map[string]any{"kind": "event", "type": "clipboard_changed", "mime": mime})
}

// onCloseRequested honors the veto-next-close flag (single-shot — the
// flag is cleared after one use, so a second close click still
// dismisses the window). On consumption, the FE is told via a
// veto_changed event so its UI mirrors the BE's source of truth.
func onCloseRequested(c *sdk.Conn, win uint32) bool {
	st.mu.Lock()
	wasVeto := st.vetoNextClose
	st.vetoNextClose = false
	st.closeReqAllow = !wasVeto
	st.mu.Unlock()
	allow := !wasVeto
	log.Printf("wash-test close_requested win=%d allow=%v", win, allow)
	if wasVeto {
		sendEvent(c, map[string]any{"kind": "event", "type": "veto_changed", "on": false})
	}
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
	m := sdk.AsMap(data)
	if m == nil {
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
	case "notify":
		st.mu.Lock()
		st.pingSeq++
		seq := st.pingSeq
		st.mu.Unlock()
		title := fmt.Sprintf("wash-test #%d", seq)
		if err := c.Notify(title, "Hello from the test app", "info"); err != nil {
			log.Printf("wash-test notify: %v", err)
		}
	case "clipboard_set":
		mime, _ := m["mime"].(string)
		text, _ := m["text"].(string)
		if mime == "" {
			mime = "text/plain"
		}
		if err := c.ClipboardSet(mime, []byte(text)); err != nil {
			log.Printf("wash-test clipboard_set: %v", err)
		}
	case "clipboard_get":
		go func() {
			mime, data, err := c.ClipboardGet(context.Background())
			if err != nil {
				log.Printf("wash-test clipboard_get: %v", err)
				return
			}
			sendEvent(c, map[string]any{
				"kind": "event",
				"type": "clipboard_get_ok",
				"mime": mime,
				"text": string(data),
			})
		}()
	case "open_echo":
		// OpenChannel must NOT be called from a callback that runs on
		// the SDK's read goroutine — the response arrives on that
		// same goroutine, so blocking it deadlocks. Hand off to a
		// fresh goroutine.
		go func() {
			ch, err := c.OpenChannel(context.Background(), c.WindowID())
			if err != nil {
				log.Printf("wash-test open_echo: %v", err)
				return
			}
			log.Printf("wash-test echo channel open id=%d", ch.ID())
			sendEvent(c, map[string]any{
				"kind":       "event",
				"type":       "echo_opened",
				"channel_id": uint64(ch.ID()),
			})
			defer ch.Close()
			if _, err := io.Copy(ch, ch); err != nil && err != io.EOF {
				log.Printf("wash-test echo io.Copy: %v", err)
			}
		}()
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
		sendEvent(c, map[string]any{"kind": "event", "type": "veto_changed", "on": on})
	case "spawn":
		appID, _ := m["app_id"].(string)
		if appID != "" {
			if err := c.SpawnRequest(appID); err != nil {
				log.Printf("wash-test spawn err: %v", err)
			}
		}
	case "sudo_whoami":
		// One-liner privileged call via the SDK helper. Goroutine
		// because PrivRunInlineSync blocks and OnAppMsg runs on the
		// SDK's read goroutine — blocking it would deadlock the
		// reply path that delivers our result.
		go func() {
			r, _ := c.PrivRunInlineSync(context.Background(), []string{"whoami"}, "wash-test demo")
			errMsg := ""
			if r.Err != nil {
				errMsg = r.Err.Error()
			}
			sendEvent(c, map[string]any{
				"kind":   "sudo_whoami_result",
				"stdout": string(r.Stdout),
				"stderr": string(r.Stderr),
				"exit":   r.Exit,
				"error":  errMsg,
			})
		}()
	case "send_to":
		// Cross-app messaging exerciser for e2e tests. The harness
		// uses it to drive wash-test into sending an arbitrary
		// payload to wash-priv (or any singleton), so the receiver
		// sees a router-attested sender of "com.wash.test" — the
		// path payload-claimed identity would never produce.
		targetAppID, _ := m["target_app"].(string)
		targetInstID, _ := m["target_inst"].(string)
		payload, ok := m["payload"]
		if !ok {
			log.Printf("wash-test send_to: missing payload")
			return
		}
		recip := wire.Recipient{AppID: targetAppID, InstanceID: targetInstID}
		if err := c.SendAppMsgTo(recip, payload); err != nil {
			log.Printf("wash-test send_to err: %v", err)
		} else {
			log.Printf("wash-test send_to ok target_app=%s target_inst=%s", targetAppID, targetInstID)
		}
	case "fe_event":
		log.Printf("wash-test fe_event %v", m["type"])
	case "crash":
		// Deliberate panic exercising the router's crash-capture +
		// shell tombstone path. Goroutine so the SDK can ship the
		// app_msg reply (if any) before the process dies. Note that
		// printing here also pre-seeds the ring buffer with a
		// recognisable line — useful for e2e assertions even if a
		// future Go release changes the panic prefix.
		log.Printf("wash-test: deliberate crash incoming")
		go func() {
			panic("wash-test: deliberate crash from FE button")
		}()
	default:
		log.Printf("wash-test unhandled kind=%q msg=%+v", kind, m)
	}
}

func sendEvent(c *sdk.Conn, payload map[string]any) {
	if err := c.SendAppMsg(payload); err != nil {
		log.Printf("wash-test SendAppMsg: %v", err)
	}
}

// testIcon — Lucide sprite symbol name (lab-glassware-themed since
// this is the test harness app).
const testIcon = "flask-conical"
