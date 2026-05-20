# wash v0.0 — build prompt

A self-contained prompt for implementing the v0.0 milestone in one pass. The
design is already locked; this prompt should not introduce new design
decisions. Where this prompt and the design docs disagree, the design docs win
— file a question, do not improvise.

## Mission

Implement v0.0 of wash, as defined in [PLAN.md](PLAN.md) §"v0.0 — Walking
skeleton", strictly conforming to [ARCHITECTURE.md](ARCHITECTURE.md) and the
v0.0 scope of [WIRE.md](WIRE.md).

## End-state deliverable

A successful build produces **exactly three files** and nothing else:

```
out/wash-router         # the router (also serves the browser shell runtime)
out/wash-session        # the session app  (declares surface=desktop)
out/wash-about          # the About app    (declares surface=window)
```

Each is a **fully static Linux ELF** (verifiable: `file out/* | grep -i statically`,
`ldd out/* → "not a dynamic executable"`), built with `CGO_ENABLED=0`. Every
asset — the browser shell runtime, each app's web-component bundle, every
icon, every byte the system needs at runtime — is embedded *inside* one of
those three binaries via `//go:embed`. Nothing else is needed at runtime.

Cross-compile of all three to `linux/arm64` MUST succeed and produce
likewise-static binaries.

## Source tree (the only tree)

```
wash/
├── README.md
├── LICENSE                            # AGPL-3.0
├── go.mod                             # module github.com/sirmick/wash
├── go.sum
├── Makefile                           # builds the three binaries; also dev
├── .gitignore                         # ignores out/, **/dist/, **/node_modules/, **/assets/
├── docs/
│   ├── ARCHITECTURE.md
│   ├── PLAN.md
│   ├── WIRE.md
│   └── V0_PROMPT.md
├── cmd/
│   ├── wash-router/
│   │   ├── main.go                    # router entrypoint; //go:embed assets
│   │   └── assets/                    # populated by `make web-shell`; gitignored
│   ├── wash-session/
│   │   ├── main.go                    # session BE entrypoint; //go:embed assets
│   │   └── assets/                    # populated by `make web-session`; gitignored
│   └── wash-about/
│       ├── main.go                    # about BE entrypoint; //go:embed assets
│       └── assets/                    # populated by `make web-about`; gitignored
├── internal/
│   ├── wire/                          # frame codec, CBOR/JSON discipline, message types
│   ├── router/                        # router internals (spawn, mux, registry, capabilities)
│   └── sdk/                           # Go SDK binding apps link
└── web/
    ├── package.json                   # workspace root
    ├── pnpm-workspace.yaml            # (or npm workspaces; pick one and document)
    ├── shell/                         # the browser shell runtime (Solid + Vite)
    │   ├── package.json
    │   ├── vite.config.ts
    │   ├── index.html
    │   └── src/
    │       ├── main.tsx
    │       ├── ws.ts                  # WS client + frame mux (mirrors internal/wire)
    │       ├── wm/                    # window manager (drag, focus-raise, close)
    │       ├── desktop.tsx            # mounts session app element as root
    │       └── window.tsx             # floating window component
    ├── apps/
    │   ├── session/
    │   │   ├── package.json
    │   │   ├── vite.config.ts         # builds <wash-app-session> as a library
    │   │   └── src/main.ts            # defines and registers the custom element
    │   └── about/
    │       ├── package.json
    │       ├── vite.config.ts         # builds <wash-app-about> as a library
    │       └── src/main.ts
    └── tsconfig.base.json
```

There is no other top-level directory. No `pkg/`, no `examples/`, no `test/`
beyond `_test.go` files next to the code they cover. The three `cmd/*/assets/`
trees are build output, gitignored.

## Build pipeline

Two stages, wired by `Makefile`:

1. **Web stage** (per binary): `vite build` produces a `dist/` for each web
   package; `brotli -k -q 11 dist/**/*.{js,css,html,svg,json}` precompresses
   in place; the dist directory is then copied into the matching
   `cmd/<binary>/assets/` for `//go:embed` to pick up. The session and about
   apps each build their web component as a **single self-contained JS file**
   (library build, IIFE/UMD or ES module with no external imports of the host
   shell — they only consume the host-provided API).
2. **Go stage** (per binary): `CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build
   -trimpath -ldflags="-s -w" -o out/<binary> ./cmd/<binary>`.

Required `make` targets:

- `make` (default = `make all`): produces `out/wash-router`,
  `out/wash-session`, `out/wash-about` for `$(GOARCH)` (default `amd64`).
- `make linux-arm64`: cross-compiles the three for `arm64`.
- `make dev`: runs the router and the Vite dev server with HMR; the router,
  when built with `-tags dev`, serves the shell runtime from a Vite proxy
  instead of embedded assets, and asset-pull for apps reads from the apps'
  `dist/` on disk. This is the only place the build tag `dev` is honored.
- `make clean`: removes `out/`, all `dist/`, all `cmd/*/assets/`.
- `make verify`: runs `go vet ./...`, `go test ./...`, and runs the static-ELF
  check (`file out/* | grep statically`).

The Makefile MUST fail if the web stage is skipped (i.e., if an embed source
is empty), so a stale or unbuilt frontend cannot silently ship.

## The three binaries

### `wash-router`

Behavior:

- Loads its config (env + flags): `WASH_LISTEN` (default `127.0.0.1:7681`),
  `WASH_APPS_DIR` (default
  `~/.local/share/wash/apps:/usr/share/wash/apps`, colon-separated; user dir
  wins on id collision), `WASH_SESSION_APP_ID` (default
  `com.wash.session`).
- At startup: scans `WASH_APPS_DIR` for executable regular files; for each,
  invokes `<binary> --wash-manifest` with a stripped env + `WASH_PROTO=1`,
  2-second timeout, 64 KiB stdout cap; parses and validates the manifest per
  [WIRE.md §5.1](WIRE.md). Builds the in-memory registry. Listed-disabled
  entries record the reason but are not silently dropped.
- Listens for the browser shell on `ws://<WASH_LISTEN>/ws`. Serves the
  embedded shell runtime on `GET /` and its assets on `GET /assets/...`.
- After the first browser WS connection, spawns the configured session app
  (the one registered app with `surface:"desktop"`, ID matches
  `WASH_SESSION_APP_ID`) by `fork+exec` with `socketpair()` and the child
  end as fd 3, plus the env from [WIRE.md §1](WIRE.md). The session app is
  considered the **first parent in the supervision tree**.
- Implements the wire protocol per WIRE.md v0.0 scope: frame codec, channel
  0 control (JSON) on both transports, channel 1 event channel (CBOR) on
  the app socket, handshake, asset pull (`asset.read`/`asset.read.ok`/
  `asset.data` base64 chunks), capability gating (the only gated op is
  `spawn.request`), close handshake with 5 s grace then `SIGTERM` then
  `SIGKILL`.
- The router-to-shell control vocabulary in [WIRE.md §8](WIRE.md) is the
  full set the router must speak; it relays `app_msg` both ways between the
  app event channel and the shell.
- Supervision: when an app exits, the router emits `window.destroy` for any
  of its windows and removes it from the active set. When the router
  receives `SIGTERM`/`SIGINT`, it asks the session app to close (which
  cascades through the supervision tree), waits the grace period, then
  exits.
- Never parses event-channel CBOR payloads beyond relaying them — the
  router only inspects message *type* when it must (capability gating,
  spawn, close). This enforces "transport, not interpreter."
- Embeds the **browser shell runtime** bundle (HTML + Solid-compiled JS +
  CSS) under `cmd/wash-router/assets/` via `//go:embed`. Brotli-precompressed
  files are preferred when the client accepts `br`.

### `wash-session`

Behavior:

- `--wash-manifest` (intercepted by the SDK before app code): prints the
  manifest below and exits 0.
- Otherwise: adopts fd 3 via the SDK, performs handshake per WIRE.md §6,
  enters the SDK's Tier-2 event loop.
- Receives `app_msg{data: {action:"launch", app_id:"com.wash.about"}}` from
  its FE half (the desktop's launcher click). Replies by emitting
  `spawn.request{app_id:"com.wash.about"}` on its event channel.
- Handles `window.close_requested` for any windows it owns by confirming
  close, and propagates close to children before its own teardown.

Manifest:

```json
{
  "id": "com.wash.session",
  "name": "wash session",
  "version": "0.0.0",
  "protocol_version": 1,
  "element": "wash-app-session",
  "surface": "desktop",
  "icon": "data:image/svg+xml,...",
  "instancing": "single",
  "capabilities": ["spawn"],
  "window": {}
}
```

Embedded FE bundle: `wash-app-session.js` (a single file registering the
`<wash-app-session>` custom element). The element renders the desktop:
a full-viewport background plus a single launcher entry "About". Click
sends the `launch` `app_msg` to the BE half. The bundle MUST work as a
plain ES module or library bundle with no external module imports —
everything it needs is in that one file (its framework, if any, bundled
in).

### `wash-about`

Behavior:

- `--wash-manifest`: prints the manifest below and exits 0.
- Otherwise: adopts fd 3, handshake, idles in the SDK event loop.
- Sets a window title once mapped (`"About wash"`).
- Confirms close immediately on `window.close_requested` (`allow:true`).

Manifest:

```json
{
  "id": "com.wash.about",
  "name": "About wash",
  "version": "0.0.0",
  "protocol_version": 1,
  "element": "wash-app-about",
  "surface": "window",
  "icon": "data:image/svg+xml,...",
  "instancing": "multi",
  "capabilities": [],
  "window": { "default_width": 480, "default_height": 320 }
}
```

Embedded FE bundle: `wash-app-about.js`. The element renders static
content: `wash — Web Application SHell · v0.0 · AGPL-3.0`, plus version
strings.

## Shared internals

`internal/wire/`:

- `Frame` type + encode/decode for the [WIRE.md §2](WIRE.md) format,
  enforcing the 16 MiB cap and `END=1` requirement.
- A `Mux` that demultiplexes a stream into per-channel sub-streams.
- JSON types for [WIRE.md §6–§9](WIRE.md) control/shell vocabularies.
- CBOR types for [WIRE.md §9](WIRE.md) event-channel messages. Use
  `github.com/fxamacker/cbor/v2` (pure Go, mature, zero cgo).
- Tests: round-trip for every defined message, plus an oversize-frame
  rejection test, plus a fuzz test on the decoder.

`internal/router/`:

- `Registry`, `Probe(--wash-manifest)`, `Spawn`, `Mux` per connection,
  `Capabilities`, `Supervisor`, shell-side handlers per WIRE.md §8,
  app-side handlers per §9.
- WebSocket server using `github.com/coder/websocket` (pure Go, no cgo).
- HTTP server using `net/http`; serves the embedded shell runtime with
  brotli content-encoding when the browser accepts it.

`internal/sdk/`:

- The Go SDK binding: `Connect()`, `Run()` (Tier 2), `LoopAddFD()` (for
  later), `OpenRaw()` (stub for v0.0 — not used), `SendAppMsg`, `SetTitle`,
  `ConfirmClose`, `SpawnRequest`. Intercepts `--wash-manifest` in
  `Main(def)`.
- The SDK is responsible for serving the embedded FE bundle to the router
  on `asset.read` requests, from a `fs.FS` the caller passes in.

## Acceptance criteria

A pull request closing v0.0 is accepted iff **all** of the following hold:

1. `make clean && make verify && make` produces exactly three files in
   `out/` and nothing else. `file out/* | grep -i statically` reports all
   three. `ldd out/*` reports each is not a dynamic executable.
2. `make linux-arm64` produces the same three, cross-compiled, statically
   linked.
3. `./out/wash-router --apps-dir <tmpdir>` with the other two binaries in
   `<tmpdir>`, with a browser pointed at the listen URL:
   - The desktop appears (the session app's element is mounted as the root
     surface).
   - The desktop's launcher shows one entry, "About", with the inline icon
     from the manifest.
   - Clicking "About" opens a floating window titled "About wash" of the
     declared default size. The window can be dragged. Clicking the window
     focuses/raises it.
   - Opening "About" again creates a second window; clicking each focuses
     correctly; the chosen one is on top.
   - Clicking a window's titlebar close dismisses the window and tears
     down the corresponding About process.
   - Killing the router (Ctrl-C) cleanly terminates session and any
     surviving About processes (no zombies, no warnings).
4. `./out/wash-session --wash-manifest` and
   `./out/wash-about --wash-manifest` each print the manifest from their
   section above and exit 0.
5. `go test ./...` passes; in particular `internal/wire` round-trip,
   oversize-frame, and fuzz tests.
6. An in-process loopback transport test exercises handshake → asset-pull
   → window mapped → close handshake using the real SDK against a real
   router, without any sockets/WS (pipe-based). The development sandbox
   SIGKILLs long-lived listeners; this test is how the spine is validated
   in CI.

## Strict non-goals (do not implement in v0.0)

Anything not on this list is in scope only if it is a direct prerequisite.

- No raw channels, no splice, no credit/backpressure (channel ids ≥ 2 are
  reserved but unused).
- No pty, no terminal app.
- No fs service, no file manager, no dialogs, no capability handles.
- No window resize, min/max/restore, taskbar, snapping, animations.
- No persistence/reattach.
- No native system settings, no auth beyond localhost binding, no
  `WASH_TOKEN`.
- No live registration rescan (router restart only).
- No LSP, no multi-user, no mobile/touch, no a11y beyond reasonable HTML
  defaults.
- No third-party UI kit larger than what Solid + tiny custom CSS provides.
  Specifically: no Tailwind, no MUI, no Radix, no shadcn.

## Hardening posture

- `CGO_ENABLED=0` is enforced in the Makefile (`-tags netgo,osusergo` if
  needed). No cgo dependencies may be added.
- All Go dependencies must be pure-Go. Reject any module that pulls in a
  C toolchain.
- `-ldflags="-s -w"` strips symbol tables.
- The router binds **127.0.0.1 only** by default; binding to any other
  address requires `--listen` and prints a warning.
- The probe exec for `--wash-manifest` runs with a stripped env (only
  `WASH_PROTO=1`), a 2-second timeout, and stdout capped at 64 KiB.
- Frame decoders reject any frame with `length > 16 MiB`. The CBOR decoder
  has a max-depth and max-elements cap.

## Decisions still open inside v0.0 (resolve in PR description, not by
  asking)

These are details that the design docs do not pin. Pick a defensible answer
and write it into the PR description:

- WebSocket library: `coder/websocket` recommended (modern, pure-Go,
  context-aware) — but `nhooyr/websocket` and `gorilla/websocket` are
  acceptable if pure-Go.
- The TS↔Go message types stay in lockstep how? For v0.0 the simplest is
  hand-written types on both sides + a contract test that round-trips a
  representative sample of each message. A codegen step is fine but only
  if it adds no runtime deps in the binaries.
- Icon SVGs: any small, lint-clean SVG is fine; the wash logo can be a
  simple "W" mark for now.
- The session app's launcher UI is whatever Solid component lets the user
  click "About". A bare button is acceptable for v0.0.

## What to commit

Commits should be small and explain the *why*. Suggested order:

1. Repo scaffold + `Makefile` skeleton that builds three empty binaries.
2. `internal/wire` with tests (round-trip, oversize, fuzz).
3. Router minimum: WS listen, spawn, mux, handshake echo, asset pull.
4. SDK minimum: fd-3 adoption, `--wash-manifest` intercept, event-loop
   stub.
5. Shell runtime minimum: WS connect, demux, mount root element.
6. wash-session BE + FE; desktop renders; launcher click sends app_msg.
7. wash-about BE + FE; spawn flow end-to-end works; close handshake.
8. arm64 cross-compile target verified.

Each commit must leave the tree buildable.

## When you are stuck

If something here contradicts the design docs, follow the docs. If the docs
are silent on a point that genuinely blocks progress, leave a `TODO(v0.0):`
comment with the chosen interim behavior, log the question to a `QUESTIONS`
section at the bottom of this file, and continue.
