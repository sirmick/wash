# wash — Architecture

This document records the locked design decisions and the reasoning behind them.
Decisions here are settled; do not relitigate without cause.

## Name

**wash** = *Web Application SHell*. It sits in the Unix shell-naming lineage
(bash/zsh/wash) and is literally true: a shell that hosts applications, the way
"GNOME Shell" is a shell. ABI prefix `wash_`, describe trigger
`--wash-manifest`, environment variable `WASH_PROTO`. (A dormant,
different-domain Puppet project shares the name; accepted.)

## Guiding constraints

- **Major constraint: must run on embedded Linux.** This pins everything:
  the router is a single static Go binary, `CGO_ENABLED=0`, **no cgo ever**,
  pure-Go dependencies only (e.g. `creack/pty`), cross-compiled to
  arm/arm64/mips/riscv64.
- **Single-user, localhost trust model (v1).** Bind 127.0.0.1; expose only via
  SSH tunnel / Tailscale; optional `WASH_TOKEN`. The real security boundary is
  server-side. The browser side is the RCE surface but has *zero ambient
  authority* (see invariant below).
- **Build fresh, do not co-opt a desktop environment.** Every surface is real
  HTML wash owns. The shell *is* the product.
- **X11 as the mental model** — connection by inherited fd, a typed event
  stream, an event-loop SDK, focus/lifecycle as events, `WM_DELETE`-style
  close. *Not* the X drawing model: the frontend web component owns rendering.

## Topology

- **Router = "init".** One static Go binary. Pure **transport, not
  interpreter**: it muxes/demuxes channels, enforces flow control, relays
  verbatim, and never parses event payloads. All semantics live in the SDK and
  the browser shell.
- The router spawns exactly **one** thing: the configured **session app**
  ("session leader" — the systemd→gnome-session relationship).
- **Flat router + supervision tree**, *never* a recursive router. The session
  app spawns children via a privileged spawn/supervision capability; children
  connect **directly** to the router (inherited fd). The router tracks
  parent→children only for lifecycle (teardown order, crash policy,
  kill-session-kills-subtree). Transport stays flat at any depth so the splice
  optimization always holds. Any spawn-capable app can parent children; the
  session app is just the first holder.
- The router has **zero desktop knowledge**. The DE chrome
  (taskbar/menu/desktop) *is the session app's frontend web component*.
  Swapping the session app swaps the entire desktop.

## App model

An app is **one self-contained executable** = a backend (BE) event-loop
program plus an embedded frontend (FE) web-component bundle.

- **FE** = a web component in a shell window. Owns the DOM. **Zero OS
  authority.**
- **BE** = a process. The only place with OS authority.
- **Invariant:** the only syscall-capable parties are **{router services, app
  backends}**. All browser-side code — every FE, the shell runtime — has zero
  ambient authority and can only send messages a BE or router service will
  police. An FE-only app (no BE) is therefore an inherently-sandboxed,
  first-class tier.
- The **FE↔BE app-private link** rides the event channel and is the *only*
  place domain semantics live (e.g. a terminal's cols/rows is computed FE-side
  from font metrics and sent to the BE as `APP_MSG`; the protocol carries only
  pixels and lifecycle).

## Frontend

- **Traditional floating desktop**: taskbar, menu, overlapping windows, a real
  WM (titlebars, 8-way resize, z-order, focus, min/max/restore, modal-for).
  Chosen over tiling.
- The shell runtime is a **compositor that hosts web components in windows plus
  the WS mux** — nothing more.
- **Right sidebar** lives in the session app's chrome (not the shell).
  Houses ambient/peripheral status: virtual-desktop pager, host
  CPU/mem load, notification history, bulk-ops queue, priv approval
  queue. Widgets subscribe to background services via the session
  BE gateway (which provides router-attested sender attribution that
  shell-originated cross-app sends lack). Modal overlays (bulk
  conflicts, priv password handshake) are anchored at the screen
  level, not nested in the sidebar — too dense for ~300px.
- App contract = a **web component** (in-process, Shadow DOM CSS isolation,
  framework-agnostic internally), defined transport-agnostically so a future
  untrusted app could be moved to an iframe binding of the same contract. Not a
  crash/security sandbox — accepted, because apps are first-party and the real
  boundary is server-side. Custom-element tags namespaced `wash-app-<id>`.
- The **focus model is intrinsic to the WM, not deferrable**; FocusIn/Out are
  delivered down the event channel. Keymap polish is deferrable.
- **Framework: Solid** (tiny runtime, fine-grained reactivity suited to a
  compositor's constant drag/resize/z-order churn; reversible because it is
  behind the web-component boundary).
- Persistence: the router owns PTY lifetime; the FE reattaches to live
  sessions; layout persists (localStorage in v1).

## Transport / wire

- **One multiplexed WebSocket** browser↔router. Apps never open their own
  sockets.
- App↔router IPC = an **inherited-fd Unix socket** (router spawns the app and
  passes the socket + `WASH_PROTO`; the X11 `$DISPLAY` model — trust by
  inheritance, no path discovery, no socket auth).
- Frame = `[chan-id + flags][len][payload]`, with three payload disciplines:
  1. **Channel 0 (control/asset):** identity, protocol version, manifest, lazy
     asset pull. Encoding: **JSON**.
  2. **Event channel:** typed structured messages both ways (WM/lifecycle in,
     requests out, and the FE↔BE app-private messages). Encoding: **CBOR**
     (mature Go and C libraries, self-describing/debuggable; a bespoke TLV is a
     later optimization only if the event channel ever becomes hot — it should
     not, raw channels carry the volume).
  3. **Raw channel(s):** opaque duplex bytes, header+len only, credit-based
     backpressure. The zero-serialization stream path.
- Backpressure propagates end to end: browser ⇄ WS ⇄ router ⇄ socket ⇄ app
  loop. The router relays raw frames straight into WS binary frames.

## Services vs. apps

Three tiers, distinguished by `manifest.surface`:

- **`window` apps** — windowed processes the user launches from the
  start menu / palette. fm, term, edit, about, top, journal, syslogs,
  services, packages.
- **`desktop` apps** — exactly one (the session leader). Owns the
  desktop chrome: banner, taskbar, start menu, palette, right
  sidebar. Filtered from the launcher (autoboots; the user doesn't
  pick it).
- **`background` services** — singleton processes with no window, no
  FE bundle, no launcher entry. Autoboot on first shell connect.
  Other apps consume them via cross-app `app_msg`; their UI (if any)
  lives in the session app's sidebar via the
  subscribe-with-snapshot pattern (`sdk.StateService`). The v1
  background services are wash-notify (notification authority,
  persistence + transient toast emit), wash-bulk (queued file ops),
  wash-priv (privilege gateway). Future audio mixer, clipboard
  daemon, network/battery agent take the same slot.
- The **router service** option (in-router goroutine bound by the
  same wire contract) is still on the table for things that
  fundamentally need router-process visibility — but the background
  apps tier replaces most "service" use cases without coupling the
  service's lifecycle to the router's.
- An external app can shadow a builtin by id.
- **pty** — originally specified as a native router service with
  router-owned lifetime, motivated by reattach/survive-disconnect. The
  **v0.1 implementation** keeps pty *inside the terminal app process*
  (`apps/term/be` imports `creack/pty` directly) because v0.1 does
  not ship reattach (Phase 5) and the splice argument is thinner across
  the WS framing layer than a literal `splice(2)` call would be.
  Bytes still flow pty ↔ wash-term ↔ router ↔ WS verbatim; raw
  channels mean the router is bytes-in-bytes-out, no decode. Reattach
  in a later phase will revisit this: either move pty to a router
  service then, or add a reattach-to-existing-instance protocol that
  serves AI chat sessions and any long-lived stateful app at the same
  time (probably the latter).
- **fs** = native router service (see below).
- The **Terminal** is a normal app *process* that consumes the pty service;
  the router **splices** the pty stream directly to the window's WS channel, so
  the app is in the *control* path, not the byte path — native-speed data with
  an out-of-process app, no special-casing. The flagship app dogfoods the SDK.

## Filesystem

The fs service exists because **the FE physically cannot syscall** — the file
manager is a web component and must have an fs wire API. It is **not a security
boundary.** Two layers:

1. **Dialog primitive (portal model).** `openFile`/`saveFile`/`pickFolder`,
   rendered by the **session app / DE chrome** (the only component trusted to
   enumerate the filesystem), *not* by the requesting app. Returns an opaque
   **capability handle** to the user-chosen path plus a display basename only —
   never the full path. The user's pick *is* the authorization. ~90% of apps
   use only this and declare no fs capability. The handle is **redeemable only
   syscall-side** (router fs service or a BE); the FE holds an opaque token it
   never dereferences. Redemption yields a **raw channel** of file bytes (or
   the router **splices** file bytes straight to a window for FE-only apps —
   e.g. an image viewer with no BE). Save writes are **atomic**
   (temp + fsync + rename). Handle lifetime: short unredeemed timeout
   (~60s); once redeemed the channel is the grant; both are **bound to the
   requesting app's window/app lifecycle** (window/app close ⇒ revoked).
   Single-redemption; multi-select returns an array of tokens; folder pick is
   the one deliberately broader grant (a raw-fs sub-API rooted at the chosen
   directory). The requesting app's `startDir`/filters are *hints* the trusted
   session app may ignore (prevents social-engineering navigation). Only the
   owning app's connection may redeem its token.
2. **Raw fs API** (list/stat/ranged-read/write/mkdir/delete/move/copy).
   For the DE (to implement dialogs), the file manager (which can't dialog
   its way to being a file browser), and declared power apps. Deliberately
   **not POSIX-complete** — no mmap/locking/fcntl; the escape hatch for
   full power is "be a BE and syscall directly."

**Watch is a shared library, not a service.** `internal/fswatch` wraps
`fsnotify` with refcounted per-path subscriptions and clean lifecycle.
Each consumer (file manager, session-app dialog provider, etc.) imports
it into its own BE and owns its own `Manager`; the router is not
involved. The decision rule: **a router-side service exists only when
consumers need cross-process coordination** (shared state, dedup,
single-system-resource pooling, capability gating that survives in-app
trust changes). Watch needs none of these — each app watches what it
cares about with independent lifetimes — so it's a library. pty is the
inverse: lifetime survival across shell reconnects genuinely demands
shared state in the router, so it stays a service. Prefer libraries;
reach for a service only when coordination is the actual need.

Backends are not required to use the fs service; they run as the user and may
syscall directly (an optional path-resolution helper library is *ergonomics,
not enforcement* — an in-process library cannot constrain its own process).
**Full isolation is dropped from v1 entirely:** chroot needs privilege
(router is non-root); namespaces/bubblewrap have kernel-config variance that
collides with the embedded constraint; cgroups are resource limits, not
path-scoping. Landlock is a far-future optional footnote, never required.

Two newly-required small pieces: a **dialog-provider role-capability**
(mechanically identical to the spawn-provider role; makes the file chooser
swappable like xdg-desktop-portal backends) and a **modal-for** window
relationship in the WM.

## Backend SDK

- X11-shaped event loop, but the SDK **must not own the loop** (the Xlib
  `ConnectionNumber`/`XPending` model).
- **Structured channel → parsed events; raw channel → readiness byte stream**
  (read/write + READABLE/WRITABLE), *not* data-in-events.
- **Ship three loop tiers:** (1) you own the loop (`wash_fd` + poll +
  `wash_dispatch` + `wash_next_event`); (2) the SDK loop, extensible with your
  own fds via `wash_loop_add_fd` — the documented default, used by the
  terminal; (3) pure callback for I/O-less apps.
- Backpressure is **surfaced, not hidden** (short write / `WRITABLE` event);
  the Go binding hides it behind a blocking `io.ReadWriteCloser`, so the
  terminal backend is ≈ two `io.Copy`s.
- **`APP_MSG` is the FE↔BE app-private pipe and the only place domain
  semantics live.**
- The SDK owns `main()` bootstrap and intercepts `--wash-manifest` before app
  code runs. **C is the canonical ABI**; Go built-ins use a cgo-free native
  binding to the same wire.
- Close handshake = X `WM_DELETE_WINDOW`: router `CLOSE_REQUESTED` → BE →
  (optionally forwarded to the FE for an unsaved-changes dialog) →
  `wash_confirm_close(allow)`; the router enforces a grace timeout then
  force-kills.
- Single-threaded like Xlib-without-`XInitThreads`; `wash_wakeup` for
  cross-thread.

## Registration

- **Single-file:** drop an executable into a watched apps dir. Discovery is the
  SDK-owned `--wash-manifest` self-describe exec → a JSON manifest on stdout,
  exit 0. No sidecar file. Content-hash cache.
- **Scan at known boundaries; v1 = router load only** (this dissolves the
  partial-write race that live-watching would introduce). `scanApps()` stays
  callable so a future rescan boundary (SIGHUP / menu action) is one line.
  Accepted cost: install/remove requires a router restart in v1.
- Two phases: **Catalog** (cheap discovery, feeds the menu) vs. **Bind**
  (the handshake at spawn — the real registration).
- Manifest fields: reverse-DNS `id`, `name`, `version`, `protocol_version`
  (ABI gate; incompatible → listed-but-disabled-with-reason), namespaced
  `element` tag, **inline** icon (the catalog has no live connection),
  window hints, `instancing` (`single`|`multi` — this absorbs the
  process-per-window question), a reserved `capabilities` slot.
- Uniform registry (`source = builtin | filesystem`); the shell/menu never see
  the difference. Trust = like `$PATH` (v1 single-user-localhost). The probe
  exec is defensive: timeout, bounded output, stripped env.

## Open items (tracked, not blocking v1)

- The full wire spec is the first v1 deliverable (Phase 0 of the plan).
- Reverse-DNS id format: working assumption, to be confirmed when the manifest
  schema is written.
- Persistent file handles (recent-files keeps-access) — explicitly post-v1.
