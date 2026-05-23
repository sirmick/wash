# wash — Web Application SHell

A browser-delivered Linux desktop. A traditional floating-window
environment — taskbar, menu, real windows — served over a single
WebSocket from a static, dependency-free Go binary that runs anywhere,
including embedded Linux. wash is not pixel-streamed remote desktop:
every surface is HTML it owns, and apps are independent one-file
programs that the router supervises.

The router is a transport — it muxes channels and never interprets
payloads. The desktop chrome is itself an app (`wash-session`); the
window manager runs in the browser; backend processes run as the user
and may syscall directly. Each app ships a Go event-loop binary
paired with an embedded web component bundle.

License: AGPL-3.0.

## Get started

Prerequisites: `go` (≥ 1.22), `pnpm`, `make`, optionally `brotli` for
smaller embedded bundles. Linux is the target; macOS works for
development but not all apps (wash-priv, the privilege primitive,
expects Linux semantics).

```bash
git clone https://github.com/sirmick/wash.git
cd wash
make            # builds every binary into ./out/
./out/wash-router
```

Then open `http://localhost:11000/` in a browser. The session app
boots automatically; click the launcher to open apps.

### Development loop

```bash
make dev        # vite at :5173 (HMR), router at :11000, /ws proxied
```

Open `http://localhost:5173/`. Edits under `web/shell/src/` hot-reload.
Edits to Go sources or app FE bundles require restarting `make dev`,
or using `--dev` mode:

```bash
./out/wash-router --dev
```

`--dev` watches the binaries in the apps dir. When you rebuild an app
(`make wash-fm`, say), running instances are killed and connected
shells get a `shell.reload` so the new bundle takes over. Rebuilding
the router itself triggers a self-re-exec.

### Useful flags

```
--listen HOST:PORT          # default 0.0.0.0:11000
--apps-dir DIR[:DIR…]       # default: dir of the wash-router binary
--no-session                # don't autoboot wash-session
--initial-app APP_ID        # spawn this app full-screen ("kiosk")
--fs-root PATH              # sandbox every fs-touching app under PATH
--show-hidden               # include manifest.hidden apps in the catalog
--control-socket PATH       # default /tmp/wash-<uid>.sock (or "none")
--screenshot-dir DIR        # default /tmp/wash-screenshots (or "none")
--dev                       # watch binaries; auto-reload
--version
```

### Running the tests

```bash
make verify     # go vet + go test + static-ELF check
make e2e        # builds the test app + runs Playwright (downloads Chromium on first run)
```

## Trust model (read this before binding to anything other than localhost)

wash v1 is single-user and assumes the router and apps run as one
trusted principal on a localhost-trust boundary. Bind to `127.0.0.1`
in production; expose via SSH tunnel or Tailscale. The router prints
a warning when `--listen` isn't loopback. There is **no
authentication** beyond "you can reach the socket."

The browser side has no ambient authority — every action funnels
through a router or app process. wash-priv is the single place a
sudo password may live, in BE memory only, with an idle timeout.
Windows whose backing process runs as root (or are wash-priv itself)
wear a red ROOT stripe so privilege is visible.

## Apps

Each app is one binary in `cmd/wash-<name>/` paired with one web
component bundle in `web/apps/<name>/`. The Makefile builds them all
and embeds the FE bundle into the BE binary.

| App           | ID                  | What it does |
|---------------|---------------------|--------------|
| **session**   | `com.wash.session`  | The desktop chrome — taskbar, launcher menu, wallpaper, pager. Autoboots when a browser connects. Surface = desktop. |
| **about**     | `com.wash.about`    | A tiny "about" window. The launch-flow smoke test. |
| **term**      | `com.wash.term`     | Terminal emulator. Tabbed xterm.js wrapping local PTYs (creack/pty). `--exec ARGS` runs a one-shot command. |
| **fm**        | `com.wash.fm`       | File manager. Single-pane tree + preview. List, read, rename, delete, write, chmod/chown, symlink, watch. Sandbox via `--fs-root`. |
| **edit**      | `com.wash.edit`     | Text editor. CodeMirror 6 with sidebar tree, tabs, embedded terminal pane, FilePicker. |
| **bulk**      | `com.wash.bulk`     | Singleton service: queue + worker for recursive delete/move/copy. Other apps enqueue jobs via app_msg.send.to. |
| **settings**  | `com.wash.settings` | Wallpaper + desktop config. Writes `~/.config/wash/desktop.json` atomically; wash-session fswatches and reloads. |
| **top**       | `com.wash.top`      | Process monitor. Reads `/proc` directly — no daemon. CPU, memory, network, disk, per-process detail; kill/term. |
| **priv**      | `com.wash.priv`     | Privilege primitive. Other apps ask wash-priv to run a registered binary as root; user approves in a queue UI; sudo password held in BE memory for the session. |
| **test**      | `com.wash.test`     | E2E test target. Hidden from the catalog unless `--show-hidden`. Built via `make test-app`. |

The session app autoboots; other apps are launched by the user (via
the chrome menu, by `wash-launch`) or by other apps holding the
`spawn` capability.

### CLI tools

These are CLIs, not apps — no FE, no window — but they're part of how
you drive a running wash.

| Tool            | What it does |
|-----------------|--------------|
| **wash-launch** | `wash-launch <app-id>` to spawn an app from the terminal. `wash-launch msg <instance> <json>` to relay an app_msg, optionally awaiting a reply. Discovers the router via `WASH_CONTROL_SOCKET`. |
| **wash-sudo**   | `wash-sudo cmd args…` — sudo-shaped CLI for wash-priv. Approval + password happen in the browser FE, never in the terminal. `--window` spawns wash-term running the command as root; `--app <id>` spawns a registered app as root. |

Both are in `out/` after a normal `make`.

## Capabilities

Apps declare capabilities in their manifest. The router enforces
them:

- **`spawn`** — may call `SpawnRequest(app_id)` to launch other apps.
  Held by `session`, `edit`. The router refuses requests from apps
  that don't declare it.
- **`prepare_spawn`** — may call `PrepareSpawn(app_id)` to have the
  router mint a pending-attach record (instance_id + token) for a
  child the app itself will fork+exec (e.g. wash-priv wrapping in
  sudo). Held by `priv`.

Reserved app ids (currently just `com.wash.priv`) can only be served
from a uid-0-owned binary or one under a declared trusted dir
(`--dev` opts the apps dir into trusted; `WASH_TRUSTED_APPS_DIRS`
declares specific paths).

## Filesystem

Apps that touch the filesystem (fm, edit, settings, bulk) run as the
user and syscall directly. `internal/fs` provides the read accessor
+ sandboxed `Confine`; `internal/fswatch` wraps fsnotify with
refcounted per-path Subs. There is no router-side fs service.

The router ships a sandbox root (`--fs-root`) in the handshake
`Session` bag; apps that honor it apply `Confine` to every path-taking
operation. With no root, apps are unconfined.

`@wash/ui`'s `<FilePicker>` works in any app whose BE calls
`sdk.EnableFilePicker(c)` — the picker talks to the host's own BE
with `fs.list` / `fs.stat` / `fs.complete` / `fs.watch` messages.

## Wire protocol & internals

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — intent, constraints,
  the locked design decisions.
- [docs/WIRE.md](docs/WIRE.md) — the wash wire protocol spec
  (framing, channel disciplines, message vocabulary).
- [docs/INTERNALS.md](docs/INTERNALS.md) — a code map: what lives
  where, how the pieces fit together, what the SDK gives you.
- [docs/AUDIT.md](docs/AUDIT.md) — duplication and simplifications
  identified in the current tree.
- [docs/PLAN.md](docs/PLAN.md) — the v1 plan against which this is built.

## Repository layout

```
cmd/                each app, plus wash-router, wash-launch, wash-sudo
internal/
  wire/             frame codec, transports, message types
  router/           router + WM state + control socket + HTTP
  sdk/              app SDK (handshake, dispatch, channels, helpers)
  fs/               read accessor + sandbox + mutations
  fswatch/          refcounted fsnotify wrapper
  bulkops/          queue + worker for wash-bulk
  loopback/         in-memory transport for end-to-end tests
web/
  shell/            browser shell runtime (WM, WS client)
  lib/              @wash/ui — shared UI + FilePicker
  apps/<name>/      per-app web component bundle
e2e/                Playwright suite
docs/               see links above
```
