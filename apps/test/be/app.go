// wash-test — control-panel + event-log app used for manual UI
// exercising and Playwright-driven e2e tests. Hidden from the
// launcher catalog (manifest.Hidden=true); spawned either by
// --initial-app or by another app's spawn.request.
package test

import (
	"bytes"
	"context"
	"embed"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"log"
	"sync"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// fakeFrame is a stand-in video frame for fake-display mode (the
// multi-window contract reference / e2e). It matches the on-wire frame
// format the wash-display compositor emits and that the shell's
// <wash-app-display> decoder expects: a 45-byte little-endian header
// followed by an image payload (the browser's createImageBitmap
// auto-detects PNG/WebP from the magic bytes). Here the payload is a
// minimal 1×1 opaque PNG, so the decoder renders one fully-opaque pixel
// — enough for the e2e canvas pixel-readback assertion.
var fakeFrame = makeFakeFrame(onePixelPNG())

// onePixelPNG returns a 1×1 fully-opaque red PNG encoded by the stdlib,
// so its chunk CRCs are valid and the browser's createImageBitmap accepts
// it. (An earlier hand-pasted base64 frame had a corrupt IDAT CRC that
// strict decoders like Chromium reject even though lenient ones read it.)
// The e2e reads this pixel back from the <wash-app-display> canvas.
func onePixelPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic("wash-test: png encode: " + err.Error())
	}
	return buf.Bytes()
}

// makeFakeFrame prepends the 45-byte little-endian display header to a
// raw image payload. The image is a single 1×1 full-surface frame with
// a dirty rect covering the whole surface; timestamps and cursor fields
// are left zero (the decoder ignores them). See docs/DISPLAY.md and the
// shell's wash-app-display.ts parseHeader for the field layout.
func makeFakeFrame(payload []byte) []byte {
	const headerBytes = 45
	const w, h = uint32(1), uint32(1)
	buf := make([]byte, headerBytes+len(payload))
	// off 0 u64 t1_frame_ready left 0.
	// dirty rect covers the whole 1x1 surface.
	binary.LittleEndian.PutUint32(buf[8:], 0)  // dirty_x
	binary.LittleEndian.PutUint32(buf[12:], 0) // dirty_y
	binary.LittleEndian.PutUint32(buf[16:], w) // dirty_w
	binary.LittleEndian.PutUint32(buf[20:], h) // dirty_h
	binary.LittleEndian.PutUint32(buf[24:], w) // frame_w
	binary.LittleEndian.PutUint32(buf[28:], h) // frame_h
	// off 32 u64 t4, off 40 cursor_x/y, off 44 cursor_visible all 0.
	copy(buf[headerBytes:], payload)
	return buf
}

//go:embed all:assets
var assetsFS embed.FS

const version = "0.9.0"

// state is per-process, shared between callbacks. The test app is
// single-window per process (instancing=multi), so this is fine.
type state struct {
	mu             sync.Mutex
	conn           *sdk.Conn
	vetoNextClose  bool
	pingSeq        int
	closeReqAllow  bool // last decision returned, for logging
	// displayChans holds the per-window video channels opened by the
	// fake-display mode, keyed by window id, so display_close can shut
	// one down. Reference impl for wash-display's per-surface streams.
	displayChans map[uint32]*sdk.RawChannel
}

var st state

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-test: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.test",
			Name:            "wash test",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-test",
			Surface:         sdk.SurfaceWindow,
			Icon:            testIcon,
			Instancing:      sdk.InstancingMulti,
			// CapRestart lets the e2e harness exercise the full-stack
			// app.restart contract (router restartBackgroundApp) through
			// a real wire round-trip, not just the router unit tests.
			Capabilities: []string{sdk.CapSpawn, sdk.CapWindows, sdk.CapRestart},
			Window:          &sdk.WindowHints{DefaultWidth: 560, DefaultHeight: 480},
			Hidden:          true,
		},
		Assets:             sub,
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
	}
	registry.Register(&registry.App{
		Name:     "wash-test",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

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

// onAppMsg handles the FE's control messages. data is the
// JSON-decoded object from the FE (map[string]any).
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
		// Echo the request id so a control-socket await_id can correlate the
		// reply (same pattern as display_open). ClipboardGet blocks on the
		// router reply, so it MUST run off the read goroutine.
		id, _ := m["id"].(string)
		go func() {
			mime, data, err := c.ClipboardGet(context.Background())
			if err != nil {
				log.Printf("wash-test clipboard_get: %v", err)
				return
			}
			log.Printf("wash-test clipboard_get_ok mime=%s bytes=%d", mime, len(data))
			// kind:"event"+type for the FE's nested dispatch; top-level "id"
			// lets a control-socket await_id correlate the reply.
			sendEvent(c, map[string]any{
				"kind": "event",
				"type": "clipboard_get_ok",
				"id":   id,
				"mime": mime,
				"text": string(data),
			})
		}()
	case "input":
		// wash-display input contract (docs/DISPLAY.md §6): the FE
		// <wash-app-display> element batches pointer/keyboard/scroll events
		// into one app_msg per rAF. The real BE (wash-display) injects these
		// into the focused wlroots surface; here we just decode + log each
		// event so the e2e can assert the FE→BE round-trip (capture,
		// coalescing, coordinate mapping) without a compositor. Note the
		// target window is the payload's "win", NOT the app_msg win arg —
		// the router delivers cross-instance app_msgs on the primary window.
		dwin := uint32(sdk.ToUint64(m["win"]))
		pchan := uint32(sdk.ToUint64(m["popup_chan"]))
		evs, _ := m["events"].([]any)
		log.Printf("wash-test input win=%d popup_chan=%d events=%d", dwin, pchan, len(evs))
		for _, e := range evs {
			em := sdk.AsMap(e)
			if em == nil {
				continue
			}
			switch sdk.ToString(em["ev"]) {
			case "motion":
				log.Printf("wash-test input win=%d ev=motion x=%d y=%d",
					dwin, sdk.ToInt64(em["x"]), sdk.ToInt64(em["y"]))
			case "motion_rel":
				log.Printf("wash-test input win=%d ev=motion_rel dx=%d dy=%d",
					dwin, sdk.ToInt64(em["dx"]), sdk.ToInt64(em["dy"]))
			case "button":
				log.Printf("wash-test input win=%d ev=button btn=%s state=%s",
					dwin, sdk.ToString(em["btn"]), sdk.ToString(em["state"]))
			case "axis":
				log.Printf("wash-test input win=%d ev=axis axis=%s delta=%d",
					dwin, sdk.ToString(em["axis"]), sdk.ToInt64(em["delta"]))
			case "key":
				log.Printf("wash-test input win=%d ev=key code=%s state=%s",
					dwin, sdk.ToString(em["code"]), sdk.ToString(em["state"]))
			default:
				log.Printf("wash-test input win=%d ev=%s (unknown)",
					dwin, sdk.ToString(em["ev"]))
			}
		}
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
	case "display_open":
		// Fake-display mode: a reference for wash-display. Create n
		// windows, open a per-window video channel on each, push one
		// stand-in frame, and reply with the (win, channel) pairs.
		// CreateWindow/OpenChannel block on the read goroutine's reply,
		// so this MUST run off it.
		id, _ := m["id"].(string)
		n := 1
		if v, ok := m["n"].(float64); ok && int(v) > 0 {
			n = int(v)
		}
		go func() {
			ctx := context.Background()
			windows := make([]map[string]any, 0, n)
			for i := 0; i < n; i++ {
				// Element override: these windows render video, so the shell
				// must mount the built-in <wash-app-display> decoder rather
				// than the test app's own manifest element (wash-app-test).
				// The real wash-display app gets this for free (its manifest
				// element IS wash-app-display); the override lets a non-display
				// app present a display window. See docs/DISPLAY.md §4.
				win, err := c.CreateWindow(ctx, sdk.WindowOpts{Title: fmt.Sprintf("Display %d", i+1), W: 320, H: 240, Element: "wash-app-display"})
				if err != nil {
					log.Printf("wash-test display_open create: %v", err)
					windows = append(windows, map[string]any{"win": 0, "channel": 0, "error": err.Error()})
					continue
				}
				entry := map[string]any{"win": uint64(win), "channel": uint64(0)}
				// The per-window video channel needs a bound shell; in a
				// headless (BE-only) run there's none, so a channel-open
				// failure is expected and non-fatal — the window itself is
				// the multi-window contract. With a shell attached the
				// channel opens and carries the frame.
				if ch, err := c.OpenChannelKind(ctx, win, wire.ChannelKindVideo); err != nil {
					log.Printf("wash-test display_open channel win=%d: %v (no shell?)", win, err)
				} else {
					if _, err := ch.Write(fakeFrame); err != nil {
						log.Printf("wash-test display_open frame win=%d: %v", win, err)
					}
					st.mu.Lock()
					if st.displayChans == nil {
						st.displayChans = map[uint32]*sdk.RawChannel{}
					}
					st.displayChans[win] = ch
					st.mu.Unlock()
					entry["channel"] = uint64(ch.ID())
					log.Printf("wash-test display win=%d channel=%d frame=%dB", win, ch.ID(), len(fakeFrame))
				}
				windows = append(windows, entry)
			}
			sendEvent(c, map[string]any{"kind": "display_opened", "id": id, "windows": windows})
		}()
	case "popup_open":
		// Open a child-surface overlay channel (kind "video-popup") on an
		// existing display window and push a geometry control frame + one
		// canned pixel frame — the fake-display analogue of an X/Wayland
		// menu mapping (docs/DISPLAY.md §12 M3). The shell routes it to the
		// parent window's <wash-app-display> as a positioned overlay canvas.
		id, _ := m["id"].(string)
		parent := uint32(sdk.ToUint64(m["win"]))
		px := int(sdk.ToInt64(m["x"]))
		py := int(sdk.ToInt64(m["y"]))
		go func() {
			ch, err := c.OpenChannelKind(context.Background(), parent, wire.ChannelKindVideoPopup)
			if err != nil {
				log.Printf("wash-test popup_open channel parent=%d: %v", parent, err)
				sendEvent(c, map[string]any{"kind": "popup_opened", "id": id, "channel": uint64(0)})
				return
			}
			// Geometry control frame (< 45 bytes → the FE reads it as JSON,
			// not a pixel frame).
			ctrl := []byte(fmt.Sprintf(`{"x":%d,"y":%d}`, px, py))
			if _, err := ch.Write(ctrl); err != nil {
				log.Printf("wash-test popup_open ctrl: %v", err)
			}
			if _, err := ch.Write(fakeFrame); err != nil {
				log.Printf("wash-test popup_open frame: %v", err)
			}
			st.mu.Lock()
			if st.displayChans == nil {
				st.displayChans = map[uint32]*sdk.RawChannel{}
			}
			st.displayChans[parent*100000+ch.ID()] = ch // unique key; cleaned on teardown
			st.mu.Unlock()
			log.Printf("wash-test popup win=%d channel=%d off=%d,%d", parent, ch.ID(), px, py)
			sendEvent(c, map[string]any{"kind": "popup_opened", "id": id, "channel": uint64(ch.ID())})
		}()
	case "display_close":
		id, _ := m["id"].(string)
		var win uint32
		if v, ok := m["win"].(float64); ok {
			win = uint32(v)
		}
		st.mu.Lock()
		ch := st.displayChans[win]
		delete(st.displayChans, win)
		st.mu.Unlock()
		if ch != nil {
			ch.Close()
		}
		if win != 0 {
			if err := c.DestroyWindow(win); err != nil {
				log.Printf("wash-test display_close: %v", err)
			}
		}
		log.Printf("wash-test display_close win=%d", win)
		sendEvent(c, map[string]any{"kind": "display_closed", "id": id, "win": uint64(win)})
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
	case "restart":
		// Full-stack app.restart exerciser for e2e. Drives the router's
		// restartBackgroundApp through a real wire round-trip: RestartApp
		// blocks on the router's app.restart.ok/err reply, so it MUST run
		// off the read goroutine. Echoes the request id so the control
		// socket's await_id can correlate the reply (router matches on
		// data["id"]). On success the new singleton instance id comes
		// back; on failure the SDK error is "<code>: <msg>".
		id, _ := m["id"].(string)
		appID, _ := m["app_id"].(string)
		go func() {
			newInst, err := c.RestartApp(context.Background(), appID)
			if err != nil {
				log.Printf("wash-test restart %s err: %v", appID, err)
				sendEvent(c, map[string]any{
					"kind":   "restart_err",
					"id":     id,
					"app_id": appID,
					"error":  err.Error(),
				})
				return
			}
			log.Printf("wash-test restart %s ok new_instance=%s", appID, newInst)
			sendEvent(c, map[string]any{
				"kind":        "restart_ok",
				"id":          id,
				"app_id":      appID,
				"instance_id": newInst,
			})
		}()
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
