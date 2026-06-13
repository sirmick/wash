# wash — internals

A code map of what's in the tree right now. The companion docs cover
intent ([ARCHITECTURE.md](ARCHITECTURE.md)) and wire format
([WIRE.md](WIRE.md)). This one just tells you where the code is and how
the pieces fit together.

## Layout

```
cmd/                 binaries (one per app, plus the router and CLIs)
internal/
  wire/              frame format, message types, transports, Mux
  router/            router + HTTP + control socket + WM state
  sdk/               app SDK (handshake, dispatch, channels, helpers)
  fs/                read-side filesystem accessor with sandbox
  fswatch/           refcounted fsnotify wrapper
  bulkops/           queue + worker library behind wash-bulk
  loopback/          in-memory transport for spine tests
  wiretest/          pipe-pair helper used by wire/sdk tests
web/
  shell/             browser shell runtime (WM, WS client)
  lib/               @wash/ui — shared UI primitives + FilePicker
  apps/<name>/       per-app web component bundle
e2e/                 Playwright end-to-end tests
docs/                this directory
out/                 build output (binaries)
Makefile             two-stage build (web → embed → go build)
```

Every app lives in `apps/<name>/`, with the Go backend under `be/`
(plus a `cmd/` shim for the standalone binary) and the Vite bundle
under `fe/`. The Makefile's `embed_dist` rule copies the Vite output
into `apps/<name>/be/assets/`, which the binary then `//go:embed`s.
Five non-app entries stay under `cmd/`: the multicall dispatcher
(`cmd/wash`), the router/launch services, the `wash-sudo` CLI, and
the `wash-priv-fakesudo` e2e stub.

## Process model

```
                                browser
                          ┌────────────────────┐
                          │   shell (web)      │
                          │   one WebSocket    │
                          └─────────┬──────────┘
                                    │  /ws  (mux of channels)
                                    │
                          ┌─────────▼──────────┐
                          │   wash-router      │   one process
                          │   :11000           │   spawns the rest
                          └─┬──────┬──────┬────┘
                            │      │      │  Unix socket per app
                            │      │      │  (control socket; apps
                            │      │      │   dial WASH_DISPLAY)
                       ┌────▼─┐ ┌──▼──┐ ┌─▼─────┐
                       │ses-  │ │term │ │ fm    │   each app =
                       │sion  │ │     │ │       │   one process
                       └──────┘ └─────┘ └───────┘
```

- **One router** binds `:11000` by default. Serves the shell at `/`,
  the WebSocket at `/ws`, optional screenshot upload at `/screenshot`.
- **One session app** (`com.wash.session`) is the desktop chrome.
  Spawned on first shell connect unless `--no-session`.
- **N app processes**, one per running window (for `instancing:multi`)
  or one globally (for `instancing:singleton`). Each dials the router's
  control socket and speaks the wash wire protocol over it.

Multiple browser tabs may attach to the same router; they all see the
same WM state because the router is authoritative.

## The wire (see WIRE.md for the spec)

Two transports carry the same frame format `[flags|chan-id(24)|len|payload]`:

1. **Browser ↔ router** — one WebSocket, binary frames, each frame one
   wash frame. Channel 0 is shell↔router control (JSON). Higher
   channels are raw byte streams (PTY, bundle uploads).
2. **App ↔ router** — a Unix socket (currently the same control socket
   apps dial via `WASH_DISPLAY`). Channel 0 is JSON control. Channel 1
   is the CBOR event channel. Higher channels are raw.

Encoding disciplines:

| Channel        | Direction       | Encoding | Use                          |
|----------------|-----------------|----------|------------------------------|
| 0 (app↔router) | both            | JSON     | Identity, ChannelOpen/Close, Error |
| 1 (app↔router) | both            | CBOR     | Window events, app_msg, spawn, clipboard |
| 0 (shell↔router)| both           | JSON     | Catalog, snapshot/patch, window controls, app_msg.deliver |
| ≥2 (app), ≥1 (shell) | both      | raw      | PTY bytes, bundle uploads    |

All message types live in `internal/wire/`:

- `frame.go` — frame codec.
- `transport.go` — `FrameTransport` interface + `StreamTransport`
  (wraps any `io.ReadWriteCloser`, serialises concurrent writes).
- `msgs_ctrl.go` — channel-0 JSON message types (Identity, ChannelOpen…).
- `msgs_event.go` — channel-1 CBOR event types (window.*, app_msg, spawn.*, clipboard.*, app_state.set).
- `msgs_shell.go` — shell-channel JSON message types (catalog,
  session.snapshot, session.patch, app_msg.deliver, notify, channel.bind…).
- `mux.go` — generic per-channel dispatch helper. **Currently unused by
  router/SDK** (both hand-roll their dispatch); exists with tests.
- `wstransport.go` — WebSocket adapter.

## Router (`internal/router/`)

The router is structured around two session types and one canonical
WM state. File-by-file:

| File              | Owns                                                       |
|-------------------|------------------------------------------------------------|
| `router.go`       | `Router` struct, channel/instance/window id allocators, channel binding registry, app_msg watchers, bundle replay |
| `app_session.go`  | `AppInstance` (one per app), handshake, event loop, dispatch of channel-0 / channel-1 / raw frames, close handshake |
| `shell_session.go`| `ShellSession` (one per browser), event loop, control message dispatch, broadcasts |
| `attach.go`       | Control-socket attach (fresh, spawn-completion, token-redeem branches) + SO_PEERCRED |
| `session.go`      | `windowSession` — authoritative WM state (geometry, z-order, focus, app_state); patches |
| `control.go`      | Control-socket protocol: `launch`, `msg`, `priv.run` |
| `session_app.go`  | Autoboot session app + `--initial-app` |
| `spawn.go`        | `exec.Command` wrapper that sets WASH_DISPLAY/PROTO/APP_ID/INSTANCE_ID |
| `proc.go`         | `/proc/<pid>` introspection for priv.run audit fields |
| `registry.go`     | App catalog: scan apps dirs, probe `--wash-manifest`, reserved-id trust |
| `manifest.go`     | Manifest schema + validation |
| `probe.go`        | `--wash-manifest` subprocess invocation (stripped env, timeout) |
| `clipboard.go`    | Router-held clipboard state |
| `http.go`         | `/`, `/ws`, `/screenshot` HTTP server |
| `transport.go`    | App-socket framing (currently the WS adapter) |
| `devreload.go`    | `--dev` mode: fsnotify on binaries → kill instances, broadcast `shell.reload`, self-re-exec |
| `cli_session.go`  | Control-socket connection promoted to a streaming back-channel (wash-sudo) |
| `signal.go`       | Cross-platform `stopSignal()` |
| `ringbuf.go`      | Per-channel scrollback buffer for shell reattach |
| `session_app.go`  | Autoboot orchestration |

Notable internals:

- **WM state lives router-side.** `windowSession` owns geometry,
  z-order, focus, min/max/restore, and per-instance app_state blobs.
  Shells observe it via `session.snapshot` on connect and `session.patch`
  on changes. Locally-originated changes (drag, focus, resize, state)
  go shell → router → patch → all shells.
- **Three handshake/teardown paths.** `spawnAndRun` (router started it),
  the token-attach branch in `handleAttach` (wash-priv started it under
  sudo), and the fresh-attach branch (terminal-launched). All three end
  in `inst.loop()` and share the same teardown sequence (unregister →
  close channels → drop watchers → drop app_state → destroy window
  patch). Some of that sequence is duplicated across the three paths.
- **Channel binding registry.** `channelBinding{app, shell, windowID,
  kind, ringbuf}`. Bytes flow app → ringbuf → shell verbatim. On shell
  disconnect, the shell pointer is cleared but the binding survives —
  the next shell that attaches calls `reattachChannelsToShell` and
  gets the buffered scrollback replayed.
- **Bundle delivery.** SDK opens a `kind:"bundle"` channel during
  handshake and uploads its embedded JS. The router caches the bytes
  on the `AppInstance` and replays to every (re)attaching shell over a
  fresh per-shell bundle channel. The shell blob-URL-imports the
  bundle and instantiates the custom element.
- **Cross-app messaging.** `EvtAppMsgSendTo` (and the shell-side
  `ShellAppMsgSendTo`) resolves a `Recipient{InstanceID|AppID}` and
  relays the data as a normal `EvtAppMsg` with a router-attested
  `from` field. Singleton apps are addressable by app_id; the router
  spawns them on demand.
- **Privilege chain.** Reserved app id `com.wash.priv` can only be
  served from a uid-0-owned binary (production) or a binary under a
  declared trusted dir (`--dev` or `WASH_TRUSTED_APPS_DIRS`). The WM
  paints a red ROOT stripe on any window whose backing process runs as
  uid 0 (SO_PEERCRED-attested) or whose app id is reserved.

## SDK (`internal/sdk/`)

The Go SDK that every app binary uses. `sdk.Main(&sdk.AppDef{...})` is
the canonical entrypoint; it intercepts `--wash-manifest`, dials
`WASH_DISPLAY`, handshakes, and runs the event loop.

| File              | Owns                                                       |
|-------------------|------------------------------------------------------------|
| `sdk.go`          | `Conn`, `Main`, `Connect`, handshake                        |
| `dispatch.go`     | Event loop, channel-0/1 dispatch into `AppDef` callbacks    |
| `outbound.go`     | App→router writes: `SetTitle`, `SendAppMsg`, `SendAppMsgTo`, `SpawnRequest`, `PrepareSpawn`, `Notify`, `SaveState`, `ClipboardSet/Get`, `PrivSpawn`, `PrivRun`, `PrivRunInlineSync` |
| `channel.go`      | `OpenChannel` + `RawChannel` (io.ReadWriteCloser over a raw channel) |
| `asset.go`        | Bundle upload at handshake (embedded `assets/` → bundle channel) |
| `filepicker.go`   | `EnableFilePicker(c)` — installs `fs.*` message handlers so any FE in the app can drive `@wash/ui`'s `<FilePicker>` |
| `manifest.go`     | Manifest schema (duplicated from `internal/router/manifest.go`) |

`AppDef` callbacks:

```
OnReady(c, instanceID, windowID)
OnMapped / OnFocus / OnUnfocus / OnResize / OnState(c, win, ...)
OnCloseRequested(c, win) bool        // false = veto
OnAppMsg(c, win, data)               // FE → BE
OnAppMsgFrom(c, win, data, sender)   // another app → us (router-attested)
OnSpawnResult / OnPrepareSpawnResult
OnClipboardChanged(c, mime)
OnShutdown(c)
```

## Browser shell (`web/shell/src/`)

The shell is a Solid app that connects to `/ws`, demuxes, and hosts
app web components in floating windows. State is server-authoritative
— the shell renders from `session.snapshot` + `session.patch`.

| File         | Owns                                                            |
|--------------|-----------------------------------------------------------------|
| `main.tsx`   | WS dispatch, app/window mounting, `window.wash` API, log mirror |
| `ws.ts`      | `Conn` — wraps WebSocket, reconnect, control vs raw frame split |
| `wire.ts`    | Frame codec mirroring `internal/wire/frame.go`                  |
| `wm.ts`      | `windows` Solid store, viewport projection, snapshot/patch apply |
| `window.tsx` | `FloatingWindow` — titlebar, drag, 8-way resize, focus model    |
| `desktop.tsx`| Root surface that hosts the session app's element               |
| `api.ts`     | Cross-element subscription primitives + BE→FE msg routing       |
| `assets.ts`  | Bundle channel reassembly + blob-URL dynamic import             |
| `notify.ts`  | Toast rendering for ShellNotify                                 |

`window.wash` is the API the desktop chrome and apps use to drive the
router from JS:

```ts
window.wash.sendAppMsg(instanceID, data)
window.wash.sendAppMsgTo({app_id|instance_id}, data)
window.wash.catalog()                  + onCatalog(cb)
window.wash.windows()                  + onWindowsChanged(cb)
window.wash.focusWindow / closeWindow / moveWindow / resizeWindow
window.wash.minimize / maximize / restoreWindow
window.wash.getViewport / setViewport  + onViewport(cb)
window.wash.openRawChannel / writeRaw
window.wash.log(level, source, msg, stack?)
```

The shell mirrors `console.*` and `window.onerror` / `unhandledrejection`
to the router via `t:"log"` so the BE log shows what the browser sees.

**Viewports.** The shell projects a 3×3 grid of viewports — the router
isn't aware. The cam div translates the windows layer by
`(-vx*W, -vy*H)`. Ctrl+Alt+arrows pans. Persists in localStorage.

## Web apps (`apps/<name>/fe/`)

Each app is a Vite library bundle that exports a custom element. The
shell loads the bundle on demand (bundle channel) and mounts the
element under the floating window or root surface.

Shared dependency is `@wash/ui` (`web/lib/`):

| File                  | What it provides                                |
|-----------------------|-------------------------------------------------|
| `button.tsx`          | `<Button>`                                      |
| `menu.tsx`            | `<Menu>`, `<MenuItem>`, `<MenuSeparator>`       |
| `overlay.tsx`         | `<ConfirmDialog>` and modal scaffolding         |
| `splitter.tsx`        | `<Splitter>` for two-pane layouts (fm, edit)    |
| `status-bar.tsx`      | `<StatusBar>`                                   |
| `file-picker.tsx`     | `<FilePicker>` — speaks `fs.*` to the host BE   |
| `tokens.ts`           | Shared design tokens (colors, spacing)          |
| `index.ts`            | Re-exports                                      |

A `<FilePicker>` placed in any app FE works as long as the BE calls
`sdk.EnableFilePicker(c)` — the picker addresses the host's own BE
with `fs.list` / `fs.stat` / `fs.complete` / `fs.watch` messages,
which the SDK handler chain answers from `internal/fs` + `internal/fswatch`.

## Filesystem (`internal/fs/` + `internal/fswatch/`)

Read accessor (`internal/fs/fs.go`) — `List`, `Stat`, `Complete`,
`Confine`, `DefaultStart`. Sandboxes by a `root` set from the
router's handshake `Session.Root`. Mutations (`internal/fs/mutate.go`)
— `Rename`, `Delete`, `CreateFile`, `CreateDir`, `Write` (atomic
temp + fsync + rename). Used by wash-fm and wash-edit directly.

Watcher (`internal/fswatch/fswatch.go`) — refcounted `fsnotify`
wrapper with `Manager.Watch(path) -> *Sub`, `Sub.Events()`, `Sub.Close()`.
Each consumer owns its own Manager; no router service.

## Bulk ops (`internal/bulkops/`)

Queue + worker library for recursive delete/move/copy with conflict
prompts and progress reporting. `wash-bulk` is a thin wire shim
around it. Counts entries (not bytes) for progress. One worker
goroutine; jobs run sequentially.

## Control socket

A separate Unix socket (`/tmp/wash-<uid>.sock` by default) the router
opens alongside `/ws`. Its purpose is to give local CLI tools and
externally-spawned apps a way in without speaking the full wash
protocol. First-byte demux:

- Starts with `{` → JSON request: `launch`, `msg`, or `priv.run`.
- Otherwise → a wash frame; treated as an app attach (the normal path
  for both router-spawned and terminal-launched apps).

JSON requests:

```
{"t":"launch","app_id":"com.wash.fm"}
{"t":"msg","instance_id":"i-5","data":{...},"await_id":"r1","timeout_ms":3000}
{"t":"priv.run","req_id":"...","argv":[...],"window":false,...}
```

`wash-launch` and `wash-sudo` are the user-facing CLIs that drive this.

## CLIs

- `wash-launch` — `launch <app-id>` or `msg <instance-id> <json>`.
- `wash-sudo` — drives `wash-priv` from the terminal. Password entry
  happens in wash-priv's FE; stdio streams through the control socket.
- `wash-priv-fakesudo` — a stub sudo for e2e tests (not in default build).

## Build

Two stages, glued by per-binary embed stamps:

1. **web** — `pnpm --filter @wash/<x> run build` (Vite library mode).
   Per-app output → `cmd/wash-<x>/assets/` via the Makefile's
   `embed_dist` macro. Brotli precompresses if installed.
2. **go** — `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"` per
   binary. `//go:embed all:assets` picks up the precompressed bundle.

The web stage is mandatory: skipping it leaves `assets/` empty and
`//go:embed` errors, so a stale frontend can't silently ship.

Dev mode (`make dev` or `wash-router --dev`) runs Vite at `:5173`
(proxies `/ws` to the router at `:11000`) and watches binaries for
changes — touched apps are killed, the router self-re-execs on its
own binary change, and shells get a `shell.reload`.

## Test surfaces

- `internal/loopback/spine_test.go` — in-memory router↔SDK round-trip
  exercising the whole spine without sockets.
- `internal/wire/*_test.go` — frame codec, message round-trips, fuzz.
- `internal/router/spine_test.go` + `manifest_test.go` — registry,
  spawn flow, handshake.
- `internal/sdk/sdk_test.go` — handshake + dispatch in-process.
- `internal/bulkops/bulkops_test.go` — queue semantics, conflicts.
- `internal/fswatch/fswatch_test.go` — refcount, lifecycle.
- `e2e/tests/*.spec.ts` — Playwright end-to-end against a real router
  + test app. `make e2e` builds the test app and runs the suite.

## Configuration

Environment / flag matrix (the flag overrides the env):

| Flag                  | Env                       | Default                          |
|-----------------------|---------------------------|----------------------------------|
| `--listen`            | `WASH_LISTEN`             | `0.0.0.0:11000`                  |
| `--apps-dir`          | `WASH_APPS_DIR`           | dir of the wash-router binary    |
| `--session-app-id`    | `WASH_SESSION_APP_ID`     | `com.wash.session`               |
| `--no-session`        | —                         | false                            |
| `--initial-app`       | —                         | (none)                           |
| `--show-hidden`       | —                         | false                            |
| `--fs-root`           | `WASH_FS_ROOT` / `WASH_FM_ROOT` | unconfined                 |
| `--control-socket`    | —                         | `/tmp/wash-<uid>.sock`           |
| `--screenshot-dir`    | `WASH_SCREENSHOT_DIR`     | `/tmp/wash-screenshots`          |
| `--dev`               | `WASH_DEV`                | false                            |
| —                     | `WASH_TRUSTED_APPS_DIRS`  | (none; for reserved-id binaries) |

Apps inherit these via env from the router:

- `WASH_DISPLAY` — control-socket path; what the SDK dials.
- `WASH_PROTO=1` — wire protocol version.
- `WASH_APP_ID` / `WASH_INSTANCE_ID` — identity for the handshake.
- `WASH_ATTACH_TOKEN` — set by wash-priv on its sudo'd children.
- `WASH_CONTROL_SOCKET` — same as DISPLAY for now; what wash-launch/sudo dial.
- `WASH_BIN_DIR` — dir of the wash-router binary; wash-term prepends it to PATH.
