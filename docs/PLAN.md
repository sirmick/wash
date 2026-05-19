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

## Phase 4 — fs service + file manager + dialogs

- Router native **fs service**: list/stat/ranged-read/write
  (atomic: temp+fsync+rename)/mkdir/delete/move/copy. `watch` may defer if it
  threatens the phase.
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
