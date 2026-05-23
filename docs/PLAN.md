# wash — v1 Implementation Plan

v1 scope: **router, session app, frontend shell + basic WM, terminal app,
file manager.** See [ARCHITECTURE.md](ARCHITECTURE.md) for the locked design
this plan builds against.

Locked decisions feeding this plan: event encoding = **CBOR**; shell framework
= **Solid**.

## Sequencing principle

Front-load the two genuine risks — wire/splice/backpressure correctness, and
terminal performance over the mux. The WM and file manager are mostly known UI
work and come after the hard part is proven. Each phase has an explicit exit
criterion: the thing that proves it done.

## Project setup

Greenfield at `~/wash`, AGPL-3.0. Two-stage build: frontend → brotli →
`go build` with `//go:embed`; dev mode serves the frontend from Vite, prod
serves it from the binary. Cross-compile targets include arm64.

## v0.0 — Walking skeleton (first milestone)

Goal: a desktop with a single floating "About" window — implemented as a
**separate app**. The smallness of the feature surface is the point; the
separateness of About from the session app is what makes this a real
milestone.

What v0.0 must prove, end to end, in one running binary set:

- Router spawns the session app over an inherited-fd Unix socket.
- The session app's web component is mounted by the shell runtime **as the
  root desktop surface** (not a floating window) — the first concrete
  realization of "the DE chrome is the session app's web component."
- Discovery of installed apps via the **real `--wash-manifest` probe** at
  router load (not a hardcoded list).
- The session app uses the **spawn capability** to launch the About app on
  user click in a minimal launcher.
- The About app handshakes, serves its embedded web-component bundle via the
  **asset-pull** path, and renders in a **floating window** with a titlebar,
  drag, focus-on-click, and a close button. Opening About twice yields two
  windows that focus/raise correctly.
- The X-style **close handshake** completes (router CLOSE_REQUESTED →
  `wash_confirm_close` → teardown).

Out of scope in v0.0 (deferred to later phases): raw channels, splice,
credit/backpressure, pty, fs, dialogs, window resize/min/max, taskbar,
persistence/reattach.

The wire spec drafted for v0.0 covers framing, channel 0 (handshake /
identity / asset pull) and the event channel (a handful of window/lifecycle
messages + the spawn request) — about a third of the full wire — and is
designed so the deferred parts slot in without rework. See
[WIRE.md](WIRE.md).

**Exit:** click the launcher → an About window appears, drags, focuses, closes.
Opening About again opens a second window. Killing the router cleanly tears
down the children.

---

## Toward v1

## Phase 0 — Spec & scaffold (the gate)

Write the wire spec — the contract both halves and the SDK compile against:

- Frame header (chan-id + flags + len).
- Channel lifecycle: OPEN / OPENED / CLOSE / CLOSED.
- The three payload disciplines (JSON control, CBOR events, raw bytes).
- Credit / backpressure encoding.
- Control message set: handshake/identity, manifest schema, lazy asset pull,
  **splice attach/detach**, **spawn/supervision**, **dialog request/response +
  handle**, window/WM lifecycle events, `APP_MSG`.

Plus: repo scaffold, Go module, frontend toolchain (Solid + Vite), the
embed+brotli pipeline, cross-compile setup.

**Exit:** the spec document exists and a frame codec passes round-trip tests in
both Go and C.

v0.0 ships a scoped slice of this spec (see above); Phase 0 completes the
rest — raw-channel discipline, credit/backpressure encoding, splice
attach/detach, dialog request/response, fs messages.

## Phase 1 — Walking skeleton

The highest-risk integration, made deliberately thin.

- Router: WS listen on 127.0.0.1; spawn one configured app over an
  inherited-fd Unix socket; mux/relay frames.
- Minimal **Go SDK binding** first (built-ins are Go; fastest iteration);
  C ABI immediately after.
- A trivial "hello" app: handshake, declares a manifest + a tiny web component.
- Shell runtime: connect WS, demux, mount the app's web component in a bare div
  (no WM yet).

**Exit:** spawn → handshake → asset pull → web component rendered in the
browser → a round-trip `APP_MSG`. The entire spine proven end to end.

## Phase 2 — pty service + terminal + splice

De-risk the core and the performance story.

- Router native **pty service** (`creack/pty`, pure-Go).
- Terminal app (Go/SDK): requests a pty session; the router **splices**
  pty ↔ the window's raw channel.
- Frontend: xterm web component (webgl + fit + serialize addons) wired to that
  raw channel; resize via `APP_MSG` (FE computes cols/rows).

**Exit:** a working terminal in a borderless window that survives a
`yes` / `cat bigfile` flood with correct backpressure.

## Phase 3 — Session app + window manager

Now it becomes a desktop.

- Session app becomes the one thing the router spawns; its FE web component is
  the DE chrome (desktop surface, taskbar, app menu). It holds the
  spawn-provider role and launches the terminal via the spawn capability.
- Frontend WM: floating windows, titlebar, drag, 8-way resize, z-order,
  **focus model (intrinsic, not deferred)**, min/max/restore, taskbar, and the
  **modal-for** relationship (needed in Phase 4).
- Registration: startup scan of the apps dir, `--wash-manifest` probe,
  catalog → menu.

**Exit:** menu → launch terminal → a focused, draggable, resizable floating
window; close/min/restore work.

Widens v0.0's minimal WM (drag / focus-raise / close) to add resize, min/max/
restore, z-order rules, and the taskbar.

## Phase 4 — fs service + file manager + dialogs

- Router native **fs service**: list/stat/ranged-read/write
  (atomic: temp+fsync+rename)/mkdir/delete/move/copy. Watch is provided
  by `internal/fswatch` as a shared library — see ARCHITECTURE.md
  §Filesystem.
- File manager app: FE web component (tree + listing +
  move/copy/rename/delete) over the raw fs API.
- Session app implements the **dialog provider** (open/save/folder UI) using
  the fs service; the router does handle mint/redeem with the lifecycle-bound
  semantics from ARCHITECTURE.md. Exercise it via a minimal save path
  (file manager "Save As", or a stub editor).

**Exit:** the file manager performs real fs operations; an open/save dialog
returns a working capability handle redeemed into a raw channel.

## Phase 5 — v1 hardening

- PTY survives reload (reattach); layout persistence (localStorage).
- Cross-compile fat single binaries with the brotli-embedded frontend for
  **arm64** — concrete proof of the embedded constraint.
- Security posture: bind 127.0.0.1; document SSH-tunnel / Tailscale exposure;
  optional `WASH_TOKEN`.
- Supervision/teardown policy, crash-restart, basic theming.

**Exit:** one static arm64 binary runs the whole desktop; reload preserves the
session; flood and teardown are clean.

## Phase 6 — Router QoS, priority classes, and online demo

Two workstreams. QoS first; demo depends on it.

### 6a. Router scheduling: priority classes + per-class flow control

Phase 0/2's credit/backpressure encoding gives correctness under flood; it does
not give *fairness*. With one shell-side stream fanned out by the router, a
chatty app (e.g. `cat bigfile` in wash-term, a 50k-entry listing in wash-fm)
can starve interactive frames from other apps. Real desktops layer scheduling
on top of flow control; wash needs the same.

- **Two priority classes to start**: Interactive (keystrokes, mouse, focus,
  app-lifecycle, anything the user is staring at right now) and Bulk
  (pty_output, list_reply, file content, watch events, telemetry). Verb-keyed
  classification table inside the router; apps stay dumb.
- **Bounded per-class outgoing queues** between the per-app Unix sockets and
  the FE WebSocket. When a class's queue is full, the router stops reading the
  Unix sockets of apps producing into it; kernel-level socket backpressure does
  the rest. No wire-format change for this piece.
- **Per-channel FE→router credit windows** (one new frame type, `CREDIT chan n`)
  so the FE can pace per-app — "wash-fm is slow rendering, pause that channel"
  without touching wash-term.
- Optional channel-class hint in the OPEN handshake later; not needed for v1
  of this work.
- **Not** doing per-app fairness within a class yet. Defer until it bites.

**Exit:** under a sustained `cat 100MB` from wash-term, keystrokes to wash-term
itself stay <50ms p99 and *other* apps' interactive frames stay <50ms p99.

### 6b. Online demo via v86

Static-hosted, zero-infra demo at `demo.wash.example` so people can try wash
in their browser without booting a VM locally. Architecture: v86 boots a real
Linux kernel + minimal userspace running native `wash-router` and friends;
the outer page hosts the wash FE directly; transport between them is
virtio-console framed as length-prefixed CBOR.

Stack locked:

- Custom Linux LTS kernel, i686, **no TCP/IP**, with virtio-PCI + virtio-console
  + ptmx + inotify + devtmpfs + futex + epoll. One 8250 kept for `earlycon`
  only (handoff to `hvc0` once virtio inits, so xterm.js never shows blank
  during early boot). Target ~5MB kernel blob.
- **Alpine minirootfs** userspace (musl + OpenRC + busybox); wash's
  service/journal UIs taught to speak OpenRC + on-disk log files directly
  rather than systemd/journald.
- `wash-router --transport=virtio-console:/dev/vport0p2` (new) — no HTTP, no
  ws listener.
- Three virtio-console ports (confirmed implemented in v86 `src/virtio_console.js`,
  buffer-event API, no UART fiction): `hvc0` (kernel/boot log → xterm.js boot
  terminal in outer page), `vport0p1` (control: `WASH_READY` token + future
  signals), `vport0p2` (router CBOR bus). Outer-side API:
  `bus.register("virtio-console{N}-output-bytes", …)` and
  `bus.send("virtio-console{N}-input-bytes", buf)`.
- Outer page: Vite-built static site on Cloudflare Pages; hosts v86,
  xterm.js, wash FE bundles, kernel.bin, rootfs.img. The same Vite project
  that builds production wash; transport abstraction in the FE selects
  WebSocket vs. serial-shim at runtime.
- Target total download: ~15MB raw, ~5–7MB brotli.

Open items: flow-control behavior under v86's virtio-console drain rate
(buffer-event API should be MB/s-class but unmeasured).

**Depends on 6a** — v86's serial bandwidth is well below loopback ws, so
without priority classes a single bulk transfer wedges the demo visibly.

**Exit:** open `demo.wash.example` in a fresh browser → see Linux boot in an
xterm.js panel → wash desktop fades in within ~10s → fm/term/session all
usable; a `cat 1MB` in wash-term does not stall wash-fm or keystrokes.

## Validation strategy

The development sandbox SIGKILLs long-lived listening servers, so v1 is
validated **piecewise**: frame-codec round-trips, `creack/pty` spawn, fs ops,
manifest probe, Vite build, cross-compile — plus an **in-memory loopback
transport** that exercises router↔app↔SDK with no real sockets/WS (the
transport-agnostic contract makes this free). Live end-to-end runs are on the
user's own machine.

## Explicit v1 non-goals

No LSP; no multi-user; no auth beyond localhost + optional token; no
Landlock/sandbox; no iframe app isolation; no live registration rescan
(router-restart only); no persistent file handles; no native system settings;
no mobile/touch; accessibility deferred.

## Critical path

Phase 0 gates everything. Phase 1's spine gates all features. pty/terminal
(Phase 2) deliberately precedes the WM (Phase 3) so the hard core is proven
before chrome is built. Dialogs (Phase 4) require the WM's modal-for and the
session app from Phase 3.
