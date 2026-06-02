// Package netd is wash-netd (com.wash.netd) — the privileged networking
// service (docs/NET.md §2.11, §3). It is a wash *background singleton service*
// (no window, no FE bundle, reserved id), modeled on apps/priv/be: it links the
// pure internal/washnet library + an Applier backend, receives requests by
// cross-app app_msg from the windowed com.wash.net app (router-attested sender),
// and publishes its status to the sidebar via sdk.StateService.
//
// Privilege boundary (§3): only this service is privileged. It authorizes
// mutating requests by From.AppID == com.wash.net; the reserved-id registry gate
// (internal/router/registry.go) refuses any untrusted binary claiming this id.
//
// Cross-app wire (from com.wash.net; each carries req_id for reply correlation):
//
//	→ {kind:"validate", config:{…}}         ← {kind:"validate_ok", diagnostics:[…]}
//	→ {kind:"diff",     config:{…}}          ← {kind:"diff_ok", entries:[…], summary:[…]}
//	→ {kind:"apply",    config:{…}}          ← {kind:"apply_ok", state, events:[…], entries:[…]}
//	→ {kind:"confirm"}                       ← {kind:"confirm_ok", state}
//	→ {kind:"revert"}                        ← {kind:"revert_ok", state}
//
// `config` is the FE interchange JSON (codec.DecodeJSON, §2.11). State pushed to
// subscribers via sdk.StateService: {kind:"state", state:{status, phase, summary,
// diagnostics}}.
//
// B1a wires this against a fake/echo Applier (fake_applier.go); the real NM
// backend is B4. The autonomous commit-confirm timer (§7) is armed in B2 — here
// confirm/revert are explicit, so the message-injection tests stay deterministic.
package netd

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/apps/netd/be/backendsel"
	"github.com/sirmick/wash/apps/netd/be/wifi"
	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/change"
	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/txn"
	"github.com/sirmick/wash/internal/washnet/ucibuf"
	"github.com/sirmick/wash/internal/washnet/validate"
	"github.com/sirmick/wash/internal/wire"
)

const version = "0.8.0"

// AppID is the reserved id this service claims. The registry refuses any
// non-trusted binary from serving it (internal/router/registry.go reservedIDs),
// so a malicious local app cannot shadow netd to inherit its privilege.
const AppID = "com.wash.netd"

// NetAppID is the windowed app allowed to drive mutating requests. The router
// attests the sender, so this is a real authorization boundary, not a hint.
const NetAppID = "com.wash.net"

// NetState is the status snapshot published to subscribers (the sidebar via the
// session-BE gateway, B1d; and the com.wash.net window, which subscribes so it
// sees autonomous transitions like the auto-revert). It carries the apply job's
// phase-event stream (§7.1) so the FE apply terminal (B3) can render the
// lifecycle, plus the commit-confirm window so the FE can run the countdown.
type NetState struct {
	Status      string                `json:"status"` // idle|await-confirm|committed|reverted|failed
	Phase       string                `json:"phase,omitempty"`
	Summary     []string              `json:"summary,omitempty"`
	Diagnostics []validate.Diagnostic `json:"diagnostics,omitempty"`
	Events      []eventDTO            `json:"events,omitempty"`
	// ConfirmWindowMs is the auto-revert window, sent while await-confirm so the
	// FE can count down to the autonomous revert (§7.2). Zero otherwise.
	ConfirmWindowMs int64 `json:"confirm_window_ms,omitempty"`
	// Backend is the active renderer ("nm"/"networkd"/"fake") and Available the
	// backends usable on this box — for the Settings Network pane's dropdown
	// (which backend is running, which to grey). Static after startup; publish()
	// stamps them onto every state so they survive state transitions.
	Backend   string   `json:"backend,omitempty"`
	Available []string `json:"available,omitempty"`
	// WifiRadio reports a wifi radio is present; WifiLive that NetworkManager is
	// the live manager (nmcli reachable). The net panel gates the +Wifi button on
	// WifiRadio + the renderer's wifi capability, and the scan/connect picker on
	// WifiLive. Static after startup; publish() stamps them like Backend.
	WifiRadio bool `json:"wifi_radio,omitempty"`
	WifiLive  bool `json:"wifi_live,omitempty"`
	// Refresh asks the FE to re-fetch `current` — pushed after netd reads the
	// box's config out-of-band via a privileged escalation (see liveConfig).
	Refresh bool `json:"refresh,omitempty"`
}

// assetsFS embeds the settings "Network" panel bundle (panel.js), staged
// by the Makefile from apps/netd/fe/dist. netd has no window; this is only
// the panel the settings app hosts.
//
//go:embed all:assets
var assetsFS embed.FS

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Printf("wash-netd: assets sub: %v", err)
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Network",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
			// Network panel for the settings app (docs/NET.md §2.7).
			SettingsPanel: &sdk.SettingsPanel{
				Section: "Network",
				Element: "wash-settings-panel-network",
			},
		},
		Assets:  sub,
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-netd",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

// Service singletons. OnReady fires exactly once (singleton instancing); the
// handler closures capture these. A package-level mutex guards the pending job.
var (
	svc     *sdk.StateService[NetState]
	applier netApplier
	mu      sync.Mutex
	pending *txn.Job    // set while a change awaits confirm; nil otherwise
	timer   *time.Timer // autonomous auto-revert timer for `pending`
)

// netApplier is the backend seam plus Live() (the confirmed base netd diffs an
// edit against). Both the fake and the real NM backend satisfy it.
type netApplier interface {
	backend.Applier
	Live() model.Config
	Devices() []string // managed link names (eth0, …) for the FE's Add wizards
}

// backendStatus is the selected + available backends netd publishes in NetState
// so the Settings Network pane can show what's running and grey what this box
// can't do.
type backendStatus struct {
	active    string   // the backend in use ("nm"/"networkd"/"fake")
	available []string // backends Detect() found usable here
}

// info is the live backendStatus (set once at startup, published in every state).
var info backendStatus

// selectApplier resolves and builds the backend, plus the status for the FE
// (docs/NET.md §2.7). Precedence: WASH_NETD_BACKEND env (tests / forcing) > the
// persisted `network` setting (the Settings dropdown's choice) > "auto" (the
// default — autodetect, no env var or config needed). Autodetecting is safe: it
// only reads/shows status; nothing touches the kernel until an explicit Apply.
// Unit tests stay hermetic because connectNetd forces WASH_NETD_BACKEND=fake, so
// the fake path neither probes nor reconfigures. chooseBackend (select.go) is the
// unit-tested policy that "auto" runs the read-only Detect probes through.
func selectApplier() (netApplier, backendStatus) {
	mode := os.Getenv("WASH_NETD_BACKEND")
	if mode == "" {
		mode = persistedBackend() // the Settings dropdown's choice, "" if unset
	}
	if mode == "" {
		mode = BackendAuto // default: autodetect
	}
	switch mode {
	case BackendFake:
		return newFakeApplier(), backendStatus{active: BackendFake, available: []string{BackendFake}}
	case BackendAuto:
		d := backendsel.Probe()
		choice, reason := chooseBackend(BackendAuto, d)
		log.Printf("wash-netd: backend = %s (%s)", choice, reason)
		return buildApplier(choice), backendStatus{active: choice, available: backendsel.Available(d)}
	default: // forced nm / networkd / netplan / ifupdown — no probe needed
		choice, reason := chooseBackend(mode, detections{})
		log.Printf("wash-netd: backend = %s (%s)", choice, reason)
		return buildApplier(choice), backendStatus{active: choice, available: []string{choice}}
	}
}

// buildApplier maps a backend name to its live applier (backendsel.New), falling
// back to the local fake for "fake"/unknown.
func buildApplier(choice string) netApplier {
	if a := backendsel.New(choice); a != nil {
		return a
	}
	return newFakeApplier()
}

// persistedBackend reads the Settings Network pane's choice from the shared wash
// config (~/.config/wash/network.json, written by com.wash.settings). Empty when
// unset — netd then falls back to the fake. Resolves the same dir settings uses,
// so the two agree as long as they share $HOME/$XDG_CONFIG_HOME (the image
// launcher propagates them from the wash user to the root-spawned netd).
func persistedBackend() string {
	dir := netConfigDir()
	if dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "network.json"))
	if err != nil {
		return ""
	}
	var v struct {
		Backend string `json:"backend"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	return v.Backend
}

func netConfigDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "wash")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "wash")
	}
	return ""
}

// ConfirmTimeout is the commit-confirm window (docs/NET.md §7, §2.9): if an
// applied change isn't confirmed within it, netd auto-reverts on its OWN —
// without the FE — which is the lock-out protection (an admin who applied a
// WAN-breaking change and got cut off is restored automatically). Tests set a
// short value; production uses this default.
var ConfirmTimeout = 90 * time.Second

// setPending records the awaiting-confirm job and arms the autonomous revert.
func setPending(job *txn.Job) {
	mu.Lock()
	defer mu.Unlock()
	pending = job
	if timer != nil {
		timer.Stop()
	}
	if ConfirmTimeout > 0 {
		timer = time.AfterFunc(ConfirmTimeout, func() { autoRevert(job) })
	}
}

// takePending clears the awaiting-confirm job (for an explicit confirm/revert)
// and disarms the timer. Returns nil if nothing was pending.
func takePending() *txn.Job {
	mu.Lock()
	defer mu.Unlock()
	job := pending
	pending = nil
	if timer != nil {
		timer.Stop()
		timer = nil
	}
	return job
}

// autoRevert fires when the confirm window elapses: if the job is still the
// pending one, revert it autonomously and publish the lock-out outcome.
func autoRevert(job *txn.Job) {
	mu.Lock()
	if pending != job {
		mu.Unlock()
		return // already confirmed or reverted
	}
	pending = nil
	timer = nil
	mu.Unlock()
	_ = job.Revert()
	log.Printf("wash-netd: auto-reverted (not confirmed within %s)", ConfirmTimeout)
	publish(NetState{Status: string(txn.Reverted), Summary: []string{"auto-reverted: not confirmed in time"}, Events: eventDTOs(job)})
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-netd ready instance=%s", instanceID)
	// Operators (and the in-VM harness) can tune the commit-confirm window
	// without a rebuild. Bad values are ignored (keep the safe default).
	if v := os.Getenv("WASH_NETD_CONFIRM_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			ConfirmTimeout = d
			log.Printf("wash-netd: confirm window = %s (WASH_NETD_CONFIRM_TIMEOUT)", d)
		}
	}
	bus := sdk.NewBus(c)
	conn = c
	applier, info = selectApplier()
	washnetReadBin = locateWashnetRead()
	washnetWifiBin = locateWashnetWifi()
	// Probe the wifi runtime once: nmcli presence + radio. Cheap, never errors —
	// absence just leaves both false so the FE hides the wifi UI.
	wifiRT = wifi.New()
	wifiRadio, wifiLive = wifiRT.Detect()
	if wifiRadio || wifiLive {
		log.Printf("wash-netd: wifi radio=%v nmcli-live=%v", wifiRadio, wifiLive)
	}
	svc = sdk.NewStateService(bus, NetState{Status: "idle", Backend: info.active, Available: info.available, WifiRadio: wifiRadio, WifiLive: wifiLive})
	registerHandlers(bus)
}

// --- privileged-read escalation (docs/NET-BACKENDS.md) ----------------------
// netd may run unprivileged — the dev router runs it as the user, and a
// least-privilege deploy may too. The config backends need root to read (and
// apply), so when netd isn't euid 0 it ESCALATES through wash-priv: it runs the
// washnet-read CLI as root (the FE shows wash-priv's approval/unlock), caches
// the result, and signals the net FE to reload. "Ask on load" falls out: the
// first `current` (panel open) kicks the escalation.

var (
	conn           *sdk.Conn
	privileged     = os.Geteuid() == 0
	washnetReadBin string
	washnetWifiBin string

	wifiRT    wifi.WifiRuntime
	wifiRadio bool
	wifiLive  bool

	liveMu         sync.Mutex
	liveCache      model.Config
	liveWarm       bool
	liveRefreshing bool
)

// liveConfig is the base config netd diffs against and serves as `current`.
// Privileged → read the box directly. Unprivileged → the cache, kicking a
// one-shot privileged refresh (via wash-priv) the first time so the panel asks
// on load rather than silently showing an empty box.
func liveConfig() model.Config {
	if privileged {
		return applier.Live()
	}
	liveMu.Lock()
	defer liveMu.Unlock()
	if !liveWarm && !liveRefreshing && conn != nil && washnetReadBin != "" {
		liveRefreshing = true
		go refreshLiveViaPriv()
	}
	return liveCache
}

func refreshLiveViaPriv() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // the user may take a while to unlock
	defer cancel()
	var cfg model.Config
	ok := false
	log.Printf("wash-netd: privileged read: %s -backend %s", washnetReadBin, info.active)
	r, err := conn.PrivRunInlineSync(ctx, []string{washnetReadBin, "-backend", info.active}, "read current network configuration")
	if err == nil && r.Exit == 0 {
		parsed, perr := codec.Parse(ucibuf.Unmarshal(string(r.Stdout)))
		log.Printf("wash-netd: privileged read ok: %d stdout bytes → %d interfaces, %d devices (parse err=%v); stderr=%q",
			len(r.Stdout), len(parsed.Interfaces), len(parsed.Devices), perr, strings.TrimSpace(string(r.Stderr)))
		if perr == nil {
			cfg, ok = parsed, true
		}
	} else {
		log.Printf("wash-netd: privileged read failed (err=%v exit=%d): %s", err, r.Exit, string(r.Stderr))
	}
	// Only warm the cache on a non-empty read: a spuriously-empty result (a
	// failed escalation, a read bug) must not stick — leave the cache cold so
	// the next `current` retries rather than showing "unconfigured" forever.
	nonEmpty := len(cfg.Interfaces) > 0 || len(cfg.Devices) > 0
	liveMu.Lock()
	liveRefreshing = false
	if ok && nonEmpty {
		liveCache = cfg
		liveWarm = true
	}
	liveMu.Unlock()
	if ok && !nonEmpty {
		log.Printf("wash-netd: privileged read returned an empty config — not caching (will retry)")
	}
	if ok && nonEmpty {
		// Tell the FE to re-fetch `current` now that we can read the box.
		publish(NetState{Status: "idle", Backend: info.active, Available: info.available, Refresh: true})
	}
}

// locateWashnetRead finds the washnet-read CLI: next to netd's binary (dev
// out/), else on PATH (the installed /usr/bin path).
func locateWashnetRead() string {
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "washnet-read")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	if p, err := exec.LookPath("washnet-read"); err == nil {
		return p
	}
	return ""
}

// locateWashnetWifi finds the washnet-wifi CLI (connect/forget under wash-priv):
// next to netd's binary (dev out/), else on PATH (installed /usr/bin).
func locateWashnetWifi() string {
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "washnet-wifi")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	if p, err := exec.LookPath("washnet-wifi"); err == nil {
		return p
	}
	return ""
}

// wifiConnectArgv builds the washnet-wifi connect argv and resolves the security
// default (empty → none). Split out so it's unit-testable.
func wifiConnectArgv(bin string, req wifiConnectReq) (argv []string, security string) {
	security = req.Security
	if security == "" {
		security = wifi.SecNone
	}
	argv = []string{bin, "-op", "connect", "-ssid", req.SSID, "-security", security}
	if req.PSK != "" {
		argv = append(argv, "-psk", req.PSK)
	}
	if req.Hidden {
		argv = append(argv, "-hidden")
	}
	return argv, security
}

// runWifiMutation performs a connect/forget OFF the SDK callback path: privileged
// netd calls nmcli directly; unprivileged netd escalates through wash-priv —
// which MUST run in a goroutine, since PrivRunInlineSync deadlocks if called
// inline on the callback's read goroutine. On completion it asks the FE to
// re-fetch wifi state (Refresh) and, on failure, publishes the reason as a
// diagnostic so a post-approval error (bad password, AP gone) isn't a silent
// no-op. direct is the in-process action used when netd is already root.
func runWifiMutation(reason string, argv []string, direct func(context.Context) (string, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // includes the unlock wait
		defer cancel()
		var out string
		var err error
		switch {
		case privileged:
			out, err = direct(ctx)
		case conn != nil && washnetWifiBin != "":
			r, rerr := conn.PrivRunInlineSync(ctx, argv, reason)
			if rerr != nil || r.Exit != 0 {
				msg := strings.TrimSpace(string(r.Stderr))
				if msg == "" && rerr != nil {
					msg = rerr.Error()
				}
				if msg == "" {
					msg = "wifi action failed"
				}
				err = fmt.Errorf("%s", msg)
			} else {
				out = strings.TrimSpace(string(r.Stdout))
			}
		default:
			err = fmt.Errorf("no privileged path for wifi action (washnet-wifi not found)")
		}
		if err != nil {
			log.Printf("wash-netd: wifi %q failed: %v", reason, err)
			publish(NetState{Status: "idle", Refresh: true, Diagnostics: []validate.Diagnostic{{
				Path: "wireless", Code: "wifi-action-failed", Message: err.Error(), Severity: validate.Error,
			}}})
			return
		}
		log.Printf("wash-netd: wifi %q ok: %s", reason, out)
		publish(NetState{Status: "idle", Refresh: true})
	}()
}

func registerHandlers(bus *sdk.Bus) {
	sdk.HandleFrom(bus, "validate", func(_ *sdk.Conn, _ string, req configReq, from wire.Sender) (validateResp, error) {
		if err := authz(from); err != nil {
			return validateResp{}, err
		}
		cfg, err := decodeConfig(req.Config)
		if err != nil {
			return validateResp{}, sdk.Errf(sdk.ErrBadRequest, "decode config: %v", err)
		}
		return validateResp{Diagnostics: nonNilDiags(validate.Validate(cfg, applier.Capabilities()))}, nil
	})

	sdk.HandleFrom(bus, "current", func(_ *sdk.Conn, _ string, _ struct{}, from wire.Sender) (currentResp, error) {
		if err := authz(from); err != nil {
			return currentResp{}, err
		}
		data, err := codec.EncodeJSON(liveConfig())
		if err != nil {
			return currentResp{}, sdk.Errf(sdk.ErrInternal, "encode current: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return currentResp{}, sdk.Errf(sdk.ErrInternal, "decode current: %v", err)
		}
		return currentResp{Config: m, Devices: applier.Devices(), Caps: capsToDTO(applier.Capabilities())}, nil
	})

	sdk.HandleFrom(bus, "diff", func(_ *sdk.Conn, _ string, req configReq, from wire.Sender) (diffResp, error) {
		if err := authz(from); err != nil {
			return diffResp{}, err
		}
		cfg, err := decodeConfig(req.Config)
		if err != nil {
			return diffResp{}, sdk.Errf(sdk.ErrBadRequest, "decode config: %v", err)
		}
		d := change.Compute(liveConfig(), cfg)
		return diffResp{Entries: entryDTOs(d), Summary: summarize(d)}, nil
	})

	sdk.HandleFrom(bus, "apply", func(_ *sdk.Conn, _ string, req configReq, from wire.Sender) (applyResp, error) {
		if err := authz(from); err != nil {
			return applyResp{}, err
		}
		cfg, err := decodeConfig(req.Config)
		if err != nil {
			return applyResp{}, sdk.Errf(sdk.ErrBadRequest, "decode config: %v", err)
		}
		// Surface diagnostics to the FE on a refused apply (the txn engine
		// re-validates internally but doesn't return the findings).
		if ds := validate.Validate(cfg, applier.Capabilities()); errorCount(ds) > 0 {
			publish(NetState{Status: "failed", Diagnostics: nonNilDiags(ds)})
			return applyResp{State: string(txn.Failed), Diagnostics: nonNilDiags(ds)}, nil
		}
		mu.Lock()
		if pending != nil {
			mu.Unlock()
			return applyResp{}, sdk.Errf(sdk.ErrBadRequest, "a change is already awaiting confirmation")
		}
		mu.Unlock()

		job, err := txn.Apply(applier.Live(), cfg, applier)
		if err != nil {
			return applyResp{State: string(job.State()), Events: eventDTOs(job)}, sdk.Errf(sdk.ErrInternal, "apply: %v", err)
		}
		resp := applyResp{State: string(job.State()), Events: eventDTOs(job), Entries: entryDTOs(job.Diff())}
		switch job.State() {
		case txn.AwaitConfirm:
			setPending(job) // arms the autonomous auto-revert (§7)
			resp.ConfirmWindowMs = ConfirmTimeout.Milliseconds()
			publish(NetState{
				Status:          string(txn.AwaitConfirm),
				Phase:           lastPhase(job),
				Summary:         summarize(job.Diff()),
				Events:          eventDTOs(job),
				ConfirmWindowMs: ConfirmTimeout.Milliseconds(),
			})
		case txn.Reverted:
			publish(NetState{Status: string(txn.Reverted), Phase: lastPhase(job), Events: eventDTOs(job)})
		}
		return resp, nil
	})

	sdk.HandleFrom(bus, "confirm", func(_ *sdk.Conn, _ string, _ struct{}, from wire.Sender) (statusResp, error) {
		if err := authz(from); err != nil {
			return statusResp{}, err
		}
		job := takePending()
		if job == nil {
			return statusResp{}, sdk.Errf(sdk.ErrBadRequest, "no change awaiting confirmation")
		}
		if err := job.Confirm(); err != nil {
			setPending(job) // confirm failed; the job is still awaiting, re-arm
			return statusResp{}, sdk.Errf(sdk.ErrInternal, "confirm: %v", err)
		}
		publish(NetState{Status: string(txn.Committed), Events: eventDTOs(job)})
		return statusResp{State: string(job.State())}, nil
	})

	sdk.HandleFrom(bus, "revert", func(_ *sdk.Conn, _ string, _ struct{}, from wire.Sender) (statusResp, error) {
		if err := authz(from); err != nil {
			return statusResp{}, err
		}
		job := takePending()
		if job == nil {
			return statusResp{}, sdk.Errf(sdk.ErrBadRequest, "no change awaiting confirmation")
		}
		if err := job.Revert(); err != nil {
			return statusResp{}, sdk.Errf(sdk.ErrInternal, "revert: %v", err)
		}
		publish(NetState{Status: string(txn.Reverted), Events: eventDTOs(job)})
		return statusResp{State: string(job.State())}, nil
	})

	// wifi_scan / wifi_status are UNPRIVILEGED reads: an active session can list
	// APs and active connections via nmcli without root, so they run inline (no
	// wash-priv escalation — unlike connect/forget). When wifi isn't live they
	// return empty so the FE just shows nothing.
	sdk.HandleFrom(bus, "wifi_scan", func(_ *sdk.Conn, _ string, _ struct{}, from wire.Sender) (wifiScanResp, error) {
		if err := authz(from); err != nil {
			return wifiScanResp{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := wifiScan(ctx, wifiRT, wifiLive)
		if err != nil {
			return wifiScanResp{}, sdk.Errf(sdk.ErrInternal, "wifi scan: %v", err)
		}
		return resp, nil
	})

	sdk.HandleFrom(bus, "wifi_status", func(_ *sdk.Conn, _ string, _ struct{}, from wire.Sender) (wifiStatusResp, error) {
		if err := authz(from); err != nil {
			return wifiStatusResp{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := wifiStatus(ctx, wifiRT, wifiLive)
		if err != nil {
			return wifiStatusResp{}, sdk.Errf(sdk.ErrInternal, "wifi status: %v", err)
		}
		return resp, nil
	})

	// wifi_connect / wifi_forget MUTATE NetworkManager and need polkit. They run
	// off the callback path (runWifiMutation goroutines the escalation) and ack
	// immediately with Pending; the outcome lands as a state Refresh. This is the
	// imperative nmcli path (NM owns the profile), so it requires wifi_live. A
	// no-NM box does declarative wifi the other way: the FE folds the SSID into
	// its config draft and uses the normal `apply` flow (netplanprofile renders
	// the wifis: block) — netd never mints a second connect mechanism.
	sdk.HandleFrom(bus, "wifi_connect", func(_ *sdk.Conn, _ string, req wifiConnectReq, from wire.Sender) (wifiActionResp, error) {
		if err := authz(from); err != nil {
			return wifiActionResp{}, err
		}
		if req.SSID == "" {
			return wifiActionResp{}, sdk.Errf(sdk.ErrBadRequest, "wifi_connect: missing ssid")
		}
		if !wifiLive {
			// No NM ⇒ no imperative path; the FE connects declaratively via apply.
			return wifiActionResp{}, sdk.Errf(sdk.ErrBadRequest, "wifi_connect requires NetworkManager; use apply for declarative wifi")
		}
		argv, security := wifiConnectArgv(washnetWifiBin, req)
		rt := wifiRT
		runWifiMutation("connect to Wi-Fi network "+req.SSID, argv, func(ctx context.Context) (string, error) {
			return rt.Connect(ctx, req.SSID, security, req.PSK, req.Hidden)
		})
		return wifiActionResp{Pending: true}, nil
	})

	sdk.HandleFrom(bus, "wifi_forget", func(_ *sdk.Conn, _ string, req wifiForgetReq, from wire.Sender) (wifiActionResp, error) {
		if err := authz(from); err != nil {
			return wifiActionResp{}, err
		}
		if req.SSID == "" {
			return wifiActionResp{}, sdk.Errf(sdk.ErrBadRequest, "wifi_forget: missing ssid")
		}
		if !wifiLive {
			return wifiActionResp{}, sdk.Errf(sdk.ErrBadRequest, "wifi_forget: requires NetworkManager")
		}
		rt := wifiRT
		argv := []string{washnetWifiBin, "-op", "forget", "-ssid", req.SSID}
		runWifiMutation("forget Wi-Fi network "+req.SSID, argv, func(ctx context.Context) (string, error) {
			return rt.Forget(ctx, req.SSID)
		})
		return wifiActionResp{Pending: true}, nil
	})
}

// wifiScan/wifiStatus are the handler cores, split out so they're unit-testable
// with a fake runtime. Both return a non-nil slice (never nil) and, when wifi is
// unavailable, an empty result with no error — the FE renders nothing rather
// than surfacing a failure.
func wifiScan(ctx context.Context, rt wifi.WifiRuntime, live bool) (wifiScanResp, error) {
	if rt == nil || !live {
		return wifiScanResp{APs: []wifi.AP{}}, nil
	}
	aps, err := rt.Scan(ctx)
	if err != nil {
		return wifiScanResp{}, err
	}
	if aps == nil {
		aps = []wifi.AP{}
	}
	return wifiScanResp{APs: aps}, nil
}

func wifiStatus(ctx context.Context, rt wifi.WifiRuntime, live bool) (wifiStatusResp, error) {
	if rt == nil || !live {
		return wifiStatusResp{Conns: []wifi.WifiConn{}}, nil
	}
	conns, err := rt.Status(ctx)
	if err != nil {
		return wifiStatusResp{}, err
	}
	if conns == nil {
		conns = []wifi.WifiConn{}
	}
	return wifiStatusResp{Conns: conns}, nil
}

// authz enforces the privilege boundary: only the windowed com.wash.net app may
// drive mutating requests. Subscribe/unsubscribe (StateService) are open.
func authz(from wire.Sender) error {
	if from.AppID != NetAppID {
		return sdk.Errf(sdk.ErrForbidden, "only %s may drive netd (got %q)", NetAppID, from.AppID)
	}
	return nil
}

func publish(s NetState) {
	if svc != nil {
		// Carry the (static) backend identity through every transition so a
		// subscriber that joins mid-apply still learns which backend is running.
		s.Backend, s.Available = info.active, info.available
		s.WifiRadio, s.WifiLive = wifiRadio, wifiLive
		svc.Mutate(func(cur *NetState) { *cur = s })
	}
}

// --- wire request/response types -------------------------------------------

// configReq carries the FE interchange JSON as a decoded map (NOT json.RawMessage:
// the router base64-encodes byte strings, cf. the CBOR/JSON pitfall). decodeConfig
// re-marshals it for codec.DecodeJSON.
type configReq struct {
	Config map[string]any `json:"config"`
}

type validateResp struct {
	Diagnostics []validate.Diagnostic `json:"diagnostics"`
}

type diffResp struct {
	Entries []entryDTO `json:"entries"`
	Summary []string   `json:"summary"`
}

type applyResp struct {
	State           string                `json:"state"`
	Events          []eventDTO            `json:"events"`
	Entries         []entryDTO            `json:"entries,omitempty"`
	Diagnostics     []validate.Diagnostic `json:"diagnostics,omitempty"`
	ConfirmWindowMs int64                 `json:"confirm_window_ms,omitempty"`
}

type statusResp struct {
	State string `json:"state"`
}

// wifiScanResp / wifiStatusResp carry the unprivileged wifi reads to the FE.
// wifi.AP / wifi.WifiConn already carry their own json tags.
type wifiScanResp struct {
	APs []wifi.AP `json:"aps"`
}

type wifiStatusResp struct {
	Conns []wifi.WifiConn `json:"conns"`
}

// wifiConnectReq / wifiForgetReq drive the privileged mutations. wifiActionResp
// only acks acceptance — the result arrives asynchronously as a state Refresh
// (the action runs behind wash-priv approval), per the connect-is-async design.
type wifiConnectReq struct {
	SSID     string `json:"ssid"`
	Security string `json:"security"` // none|psk2|sae (empty → none)
	PSK      string `json:"psk"`
	Hidden   bool   `json:"hidden"`
}

type wifiForgetReq struct {
	SSID string `json:"ssid"`
}

type wifiActionResp struct {
	Pending bool `json:"pending"`
}

// currentResp carries the box's live config (the FE-JSON interchange, as a
// structured map — not raw bytes, which the router would base64-encode) plus the
// backend's capabilities so the FE greys what this backend can't do.
type currentResp struct {
	Config  map[string]any `json:"config"`
	Caps    capsDTO        `json:"caps"`
	Devices []string       `json:"devices"` // managed links for the Add wizards
}

// capsDTO carries the backend's capabilities generically (docs/NET.md §2.7): the
// supported feature keys + object-kind keys ("pkg/section"), so the FE greys
// what this backend can't do by set membership. A new backend (networkd, uci)
// advertises a different subset and the FE re-gates off these sets — no lockstep
// DTO/FE edits per feature. Both lists are sorted for deterministic output.
type capsDTO struct {
	Features []string `json:"features"`
	Kinds    []string `json:"kinds"`
}

func capsToDTO(c caps.Capabilities) capsDTO {
	feats := make([]string, 0, len(c.Features))
	for f, ok := range c.Features {
		if ok {
			feats = append(feats, string(f))
		}
	}
	sort.Strings(feats)
	kinds := make([]string, 0, len(c.Kinds))
	for k, ok := range c.Kinds {
		if ok {
			kinds = append(kinds, k)
		}
	}
	sort.Strings(kinds)
	return capsDTO{Features: feats, Kinds: kinds}
}

// entryDTO / eventDTO are snake_case wire shapes for the pure domain types
// (change.Entry / txn.Event have no json tags — kept pure).
type entryDTO struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Op   string `json:"op"`
}

type eventDTO struct {
	Seq   int    `json:"seq"`
	Phase string `json:"phase"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// --- helpers ----------------------------------------------------------------

func decodeConfig(m map[string]any) (model.Config, error) {
	if m == nil {
		return model.Config{}, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return model.Config{}, err
	}
	return codec.DecodeJSON(b)
}

func entryDTOs(d change.Diff) []entryDTO {
	out := make([]entryDTO, 0, len(d.Entries))
	for _, e := range d.Entries {
		out = append(out, entryDTO{Kind: e.Kind, Name: e.Name, Op: string(e.Op)})
	}
	return out
}

func eventDTOs(j *txn.Job) []eventDTO {
	evs := j.Events()
	out := make([]eventDTO, 0, len(evs))
	for _, e := range evs {
		out = append(out, eventDTO{Seq: e.Seq, Phase: e.Phase, Level: e.Level, Msg: e.Msg})
	}
	return out
}

func summarize(d change.Diff) []string {
	out := make([]string, 0, len(d.Entries))
	for _, e := range d.Entries {
		out = append(out, fmt.Sprintf("%s %s %s", e.Op, e.Kind, e.Name))
	}
	return out
}

func lastPhase(j *txn.Job) string {
	evs := j.Events()
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].Phase
}

func errorCount(ds []validate.Diagnostic) int {
	n := 0
	for _, d := range ds {
		if d.Severity == validate.Error {
			n++
		}
	}
	return n
}

// nonNilDiags ensures the JSON is [] not null, so the FE always gets an array.
func nonNilDiags(ds []validate.Diagnostic) []validate.Diagnostic {
	if ds == nil {
		return []validate.Diagnostic{}
	}
	return ds
}
