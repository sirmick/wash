# wash settings — service-control panels (VS Code & compositor)

Status: **design** (2026-05-31). Plan of record for turning the VS Code
manager and the X/Wayland compositor into **background services** that
the settings app controls via host-rendered panels — the same idiom the
session sidebar already uses for notify / bulk / priv.

Related: [ARCHITECTURE.md](ARCHITECTURE.md) (§ "Services vs. apps",
the subscribe-with-snapshot pattern), [DISPLAY.md](DISPLAY.md)
(wash-display, already `surface=background`), [WIRE.md](WIRE.md).

---

## 1. Goal & non-goals

**Goal.** Two subsystems that today are (or were) windowed manager apps
become headless background services with their control UI hosted in the
settings app:

- **VS Code** — code-server ownership + install/upgrade move into a
  background service. Its control FE (status / start / stop / install /
  update / **restart**) becomes a settings panel. The actual editor is
  the `vscode.workbench` window, which becomes the launchable entry and
  prompts for a folder on cold launch.
- **Compositor (wash-display)** — already `surface=background` with an
  empty FE bundle. Gains a settings panel: status (running / which
  `wayland-N` / native-window count) + a **restart** button, and
  degrades gracefully when the `wash-display` package isn't installed.

**Non-goals (v1).** No new "embed app B's surface inside app A"
primitive — panels are host-rendered over a `*.state` snapshot, exactly
like the sidebar widgets. No settings-side folder picker (the folder
prompt lives on the workbench, see §4). No per-service config files
beyond what each service already keeps.

---

## 2. Why host-rendered, not surface-embedding

wash has no "mount app B's element inside app A" mechanism, and we're
deliberately not adding one. The established pattern is the inverse:
the **service owns the data, the host owns the UI**. The session
sidebar already does this — `NotifyWidget` / `BulkWidget` / `PrivWidget`
live in `apps/session/fe`, while notify / bulk / priv are bundle-less
background services that push `*.state` snapshots on subscribe and on
every change.

Settings panels follow the same shape, with one routing difference from
the sidebar: settings is a real **window** app with its own BE, so its
FE routes cross-app sends **through its own BE** (which calls
`SendAppMsgTo({AppID: …})` with a proper router-attested `From`). The
sidebar has to bounce through the session-BE gateway because
shell-originated sends carry no attested sender; a normal app BE doesn't.

Consequence: the C++ compositor never needs an FE bundle. Its panel is
TypeScript in the settings app; the C++ side only emits a state message
and accepts a restart.

---

## 3. The two services

### 3a. VS Code → background service + workbench window

Today `com.wash.vscode` is a singleton **window** that both owns
code-server and is the control panel, and `com.wash.vscode.workbench`
is a hidden multi-window editor it spawns per folder. After this change:

- **`com.wash.vscode`** flips `surface=window` → `surface=background`.
  It keeps everything in `server.go` / `install.go` (code-server
  process + ingress + detect/install/upgrade), keeps `CapSpawn` (it
  still spawns workbench windows), and keeps its cross-app message
  surface (`subscribe` / `ensure` / status / log / ready / exited /
  error). What it loses: its own window, its control-panel FE bundle,
  the `auto-open workspace on ready` behaviour, and the
  `onCloseRequested` confirm-and-teardown (a background service has no
  window to close). code-server lifecycle detaches from "is the manager
  window open" — strictly better for a service. It autoboots like any
  other background app and lives for the router's lifetime.

- **`com.wash.vscode.workbench`** flips `Hidden=true` → visible, gaining
  a launcher/catalog entry (it *is* "VS Code" to the user now). On
  launch it has no folder, so its FE **prompts for a folder first**
  (reusing the FilePicker — picker enabled on the workbench BE), then
  runs its existing `ensure` flow against the service and opens the
  editor at that folder. Re-opening from a persisted state restores the
  last folder without prompting.

The control panel FE (status/start/stop/install/update/restart, plus
the install-log stream) is deleted from `apps/vscode/fe` and
reincarnated as the settings **Developer** panel (§4), host-rendered
over the service's existing `status` / `log` messages.

### 3b. Compositor (wash-display)

Already `surface=background`, singleton, empty bundle (DISPLAY.md §2).
Two additions:

- **State message.** It already sends `app_msg{kind:"display_ready",
  wayland_display:"wayland-N"}` on startup (DISPLAY.md §9a). Generalise
  to a `display.state` snapshot — `{ running, wayland_display,
  window_count }` — emitted on subscribe and on window create/destroy,
  so the panel can show live status. (Small C++ addition; mirrors the
  notify/bulk StateService contract in JSON.)
- **Restart.** Handled by the new router verb (§5), not by the service
  itself.

**Not-installed is free.** wash-display ships as a separate package
(`wash-display` deb/rpm/apk, DISPLAY.md §8). When absent it's simply not
in the router registry. The panel's `display.subscribe` cross-app send
resolves to `not_found`, and the panel renders "Compositor not
installed — install the `wash-display` package." Registered-but-dead →
the singleton resolve path spawns it on demand. No new "is it installed"
wire surface; **absence is the signal.**

---

## 4. The settings host

Today `com.wash.settings` (appearance/desktop prefs) and
`com.wash.services` (systemd/openrc init control) are separate window
apps. The two new panels are service-control, closer to `services` in
spirit but to `settings` in name. **v1 plan: add both panels to
`settings`** as new sections (Developer = VS Code, Display =
compositor), leaving the init-system `services` app untouched.
Consolidating `services` + `settings` into one "System Settings" hub is
a later, separate question (§8).

**Settings BE** gains a thin cross-app relay (no folder picker here).
For each panel it:
- forwards FE subscribe/unsubscribe to the service via
  `SendAppMsgTo({AppID: serviceID}, …)`,
- forwards control verbs (start/stop/install/update/restart) the same
  way,
- relays the service's `status` / `log` / `display.state` pushes back
  down to its own FE.

**Settings FE** gets a `VSCodePanel` and a `DisplayPanel` (new files
under `apps/settings/fe/src/`), modelled on the sidebar widgets:
subscribe on mount, render the snapshot, fire control verbs on click,
unsubscribe on cleanup. Restart and install/update route through the
service; privileged steps (compositor restart may need it; code-server
install does not) go through `wash-priv` exactly as the `services` app
already does for init actions.

**No folder picker in settings.** The folder prompt belongs to whatever
opens an editor window — i.e. the **workbench** (§3a), which asks for a
folder on cold launch. Settings only controls the *service*.

---

## 5. New wire/router surface: restart a background singleton

The one genuinely new mechanism. Background services don't auto-respawn:
`Router.EnsureBackgroundAppsRunning` (`internal/router/autoboot.go`)
spawns each `surface=background` app once per shell connect and only
clears the per-app `backgroundStarted` flag on spawn *failure*. So
"restart the X/Wayland server" needs an explicit verb.

**Approach: a router-level restart verb.** General — any background
singleton can be restarted, not just display.

```jsonc
// app → router  (channel-1 control, req_id correlated like spawn.request)
{ "t":"app.restart", "req_id":9, "app_id":"com.wash.display" }
// router → app
{ "t":"app.restart.ok",  "req_id":9, "instance_id":"…" }   // new instance
{ "t":"app.restart.err", "req_id":9, "code":"not_found", "msg":"…" }
```

Router behaviour:
1. Resolve `app_id` in the registry. Missing/disabled → `not_found`.
2. Must be `surface=background` (restarting a windowed/desktop app is
   out of scope; return `forbidden` otherwise).
3. Gated by a new **`restart`** capability on the *caller* (settings
   declares it), mirroring the `spawn` trust model — an arbitrary app
   can't cycle system services.
4. Terminate the existing singleton instance (SIGTERM → grace → SIGKILL),
   GC its windows/channels (display owns N — reuse the instance-death
   teardown DISPLAY.md §11 calls out), clear `backgroundStarted[appID]`,
   then `spawnAndRun` a fresh instance on the router-lifetime context.
   The kill+clear+respawn shares `backgroundMu` with
   `EnsureBackgroundAppsRunning` so a restart can't race autoboot.
5. Reply `app.restart.ok` with the new `instance_id`.

The SDK gets `Conn.RestartApp(appID) (instanceID, error)` and the
capability constant `CapRestart`. The shell `window.wash` bridge does
**not** need this — settings drives it BE-side.

vscode reuses the same verb if/when a hard "bounce the whole service"
is wanted; for routine use its existing `stop`/`start` on code-server is
enough, so vscode-restart is opportunistic, not required.

---

## 6. Commit ladder

Each row is one reviewable commit. 1–3 are pure Go/docs/tests and land
before any FE or C++.

| # | Commit | Touches | CI-testable now? |
|---|---|---|---|
| 1 | `docs: settings service-control panels (SETTINGS.md)` | docs | ✅ (this) |
| 2 | `wire: app.restart/ok/err + restart capability` | `internal/wire`, `internal/sdk` (cap + `RestartApp`) | ✅ `go test ./internal/wire/...` |
| 3 | `router: handle app.restart (background singleton kill+respawn, cap gate)` | `internal/router` | ✅ router unit tests |
| 4 | `vscode: surface=window→background; drop control FE + window lifecycle` | `apps/vscode` | ✅ build + be_test |
| 5 | `vscode-workbench: unhide; folder prompt on cold launch` | `apps/vscode-workbench` | ✅ build; e2e in 8 |
| 6 | `settings: BE relay for vscode + display; FE Developer/Display panels` | `apps/settings` | ✅ build |
| 7 | `display: emit display.state snapshot; accept app.restart` | `wash-display` (C++) | gated `WASH_DISPLAY=1` |
| 8 | `e2e: settings panels drive both services; restart round-trip; not-installed fallback` | `apps/test`, `e2e` | ✅ Playwright + router-log |

Commit 3 makes the restart contract real and regression-tested before
either consumer exists. Commit 6 is the first user-visible payoff
(panels work against the live services). Commit 7's C++ change plugs
into an already-green contract.

---

## 7. E2E test plan

Per the wash e2e pattern (test app + Playwright FE + router-log BE
assertions):

- **Restart verb (no compositor needed).** Drive `app.restart` against a
  fake `surface=background` test fixture: assert router log shows
  terminate + `backgroundStarted` clear + respawn, and `app.restart.ok`
  carries a *new* instance id. Capability-denial: a caller without
  `restart` cap gets `app.restart.err code=forbidden` (assert log).
- **VS Code panel.** Open settings → Developer panel; assert it
  subscribes (router log: `app_msg … to com.wash.vscode kind=subscribe`),
  renders status, and start/stop/restart clicks reach the service.
- **Workbench folder prompt.** Launch `com.wash.vscode.workbench` cold;
  Playwright asserts the folder picker appears before any editor frame,
  picks a folder, asserts `ensure` fires with that folder.
- **Compositor not-installed fallback.** With `wash-display`
  unregistered, the Display panel's subscribe resolves `not_found`;
  Playwright asserts the "not installed" state renders (no crash).
- **Orphan hygiene** ([memory: e2e orphan accumulation]) — teardown
  asserts child count returns to baseline.

---

## 8. Open questions / risks

- **Hub consolidation.** Whether `services` (init) and `settings`
  eventually merge into one "System Settings" hub. Deferred; v1 just
  adds two sections to `settings`.
- **Restart races.** `app.restart` while autoboot's first spawn for the
  same id is still in flight — the kill+clear+respawn must be
  serialised against `EnsureBackgroundAppsRunning` (share the
  `backgroundMu` critical section).
- **Display restart teardown.** Restarting wash-display must GC all N
  windows + video channels it owns (DISPLAY.md §11 instance-death audit)
  — verify a restart doesn't leak windows in the shell's `SessionWindow`
  list.
- **code-server detach.** Moving vscode to background means code-server
  no longer dies with a window. Confirm the service's own teardown
  (router-lifetime ctx cancel) still kills the child code-server
  process on router shutdown.
- **Workbench as launcher entry.** It was `Hidden=true`; unhiding it
  means it needs a sensible name/icon/accent in the catalog and must not
  double up with any leftover vscode-manager entry.
