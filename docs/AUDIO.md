# AUDIO — wash audio subsystem & music player

Status: design approved 2026-06-06 (branch `wash-audio`). M1 + M3 done and
e2e-green; M2 (library/playlist/skins) next.

This document describes wash's audio subsystem and its first consumer, a
Winamp-skinned music player. The defining constraint shapes everything:

> **wash is a remote-rendered DE — the only real audio sink is the user's
> browser tab.** The Go backend on the server has no speakers anyone hears.
> All audio is ultimately decoded and played by Web Audio / `<audio>` in the
> browser FE.

## 1. Two data planes

Audio reaches the browser by one of two paths. They are genuinely different
problems with different transports; the control plane (§3) is shared.

### Case 1 — encoded source, browser decodes (NOW)

The backend hands the browser **already-encoded bytes** (mp3/aac/ogg) as a
URL; the browser's `<audio>` element does the decode and playback.

- local files on the server disk → served as an **ingress** URL with HTTP
  Range support (`docs/` ingress facility; `Conn.PublishIngress`).
- web radio / Icecast / SHOUTcast → the remote URL, proxied through ingress
  for CORS when needed.

Transport = **plain HTTP over the ingress reverse-proxy**. No server-side
decode, no codec work, no latency budget. This is the entire music player and
it is cheap. This is what we build now.

### Case 2 — server produces live audio, transport to FE (DEFERRED)

A process on the server generates sound *right now* into the server's audio
system (PipeWire/PulseAudio) and there is no fetchable encoded file:

- any Linux app running under the wayland/display layer that makes sound
  (mpv, a game, a browser-in-wash) — see `docs/DISPLAY.md`
- server-side TTS / AI voice / system chimes

Transport = **capture (PipeWire null-sink/monitor) → encode to Opus →
low-latency stream (WebRTC preferred; MSE/WS fallback)**. The key decision:
**Case 2 is the audio track of the display pipeline, not a standalone sound
server.** When display goes WebRTC (VP9 plan, `docs/DISPLAY.md`), audio rides
the same peer connection with A/V sync for free. Until then, server-originated
audio is simply inaudible (nothing past `xclock` makes sound yet). Do **not**
build a separate soundserver for this.

`AudioState` (§3) carries a `kind: "fe-decoded" | "server-live"` per source
from day one so Case 2 slots into the same mixer/sidebar without reshaping
anything.

## 2. Apps

Two apps, both reusing existing wash patterns verbatim.

### `com.wash.audio` — control-plane mixer service (§3)

Singleton, `surface=background`, no window, no FE bundle of its own — exactly
the `com.wash.notify` shape. Holds `AudioState` via `sdk.StateService`. **Owns
no audio bytes.** It is a registry of who is making sound plus master
volume/mute. Because it is the only global observer of all sources, it is the
natural (later) home for ducking/exclusivity policy.

### `com.wash.music` — Winamp window

`surface=window`, `InstancingSingle` (one Winamp). FE embeds **Webamp**
(npm `webamp`, MIT) for the pixel-perfect classic-skin UI, playlist, EQ, and
Butterchurn visualizer — Webamp is the JS implementation of the Winamp skin
engine; we consume it, we don't reimplement skinning. BE scans the music
library, serves files over ingress, and registers/reports playback to
`com.wash.audio`.

Window geometry: classic Winamp is pixel-locked (main window 275×116; EQ and
playlist are fixed sub-windows Webamp draws inside its own container). v1 puts
Webamp inside a normal wash frame with `WindowHints{Resizable:false}` sized to
Webamp's footprint. True borderless/chromeless windows are a WM feature for
later (`WindowHints` has no borderless flag today); when it lands, the music
window goes chromeless and Webamp draws the entire Winamp titlebar itself.

## 3. Control plane — `AudioState`

`com.wash.audio` publishes one state object to subscribers (the session
sidebar gateway, and any app that wants the mixer view) via the standard
`StateService` subscribe-with-snapshot protocol.

```
AudioState {
  masterVolume: float   // 0..1
  masterMute:   bool
  sources: [ Source ]   // everything currently making (or able to make) sound
}

Source {
  id:       string      // service-assigned, stable
  appID:    string      // router-attested producer
  kind:     "fe-decoded" | "server-live"
  title:    string
  artist:   string
  status:   "playing" | "paused" | "stopped"
  posSec:   float
  durSec:   float
  volume:   float       // 0..1, per-source
  muted:    bool
}
```

Contract between a producer app and the service (small, like notify):

```
producer → service   register   {meta}            → service assigns id
producer → service   report     {id, status, pos, …}
producer → service   unregister {id}
service  → producer   (cross-app) setVolume {id, volume} / setMuted / transport
service  → subscribers state      {AudioState}      (StateService)
```

**Volume application is client-side.** With each producer owning its own
`<audio>` (Case 1) or `MediaStream` gain node (Case 2), master volume is a
multiply applied by each producer's FE: `el.volume = masterVolume * source.volume`
(and `el.muted = masterMute || source.muted`). No central audio graph, no
centralizing of bytes — the service only moves state.

## 4. Sidebar

`AudioWidget` in `apps/session/fe/src/sidebar/` subscribes to `AudioState`
through the session BE gateway (the `NotifyWidget` pattern exactly): now-playing
title/artist/progress, transport buttons, master volume slider, per-source
mute. Transport buttons send cross-app messages the service relays to the
owning producer. Pure renderer; the gateway does the wiring.

## 5. Milestones

- **M1 — Webamp plays a track over ingress.** Scaffold `apps/music/{be,fe}`.
  BE: fixed non-resizable window; Range-capable file server on a unix socket →
  `PublishIngress`; serve a bundled sample track. FE: embed `webamp`,
  `renderInto` the host, default skin, `setTracksToPlay` with the ingress URL.
  Wire Makefile + `cmd/wash` imports. Green build + e2e (window opens, Webamp
  main window present, playback starts). *Proves: webamp bundles under Vite,
  ingress serves Range audio with correct CORS, fixed-size window.*

- **M2 — library + playlist + skins.** BE scans a music dir (default
  `~/Music`) via the fs layer and exposes each track over ingress; FE builds
  the playlist via `appendTracks`. Skins: ship Webamp's default + drag-drop
  `.wsz` (`setSkinFromArrayBuffer`) + remote stream URL entry. No bundling of
  third-party skin art (licensing — skins are author-owned fan art).

- **M3 — control plane + sidebar.** `com.wash.audio` (`StateService`) +
  session gateway + `AudioWidget`. Music registers/reports; sidebar shows
  now-playing + master volume + transport and drives the player cross-app.

- **M4+ — deferred.** Persistence (keep playing on window close) via a
  persistent host element owned outside the closable window; per-source
  volume + ducking policy in the service; **Case 2** server-live audio folded
  into the display/wayland WebRTC milestone.

## 6. Non-goals (v1)

- No server-side mixing/decoding/effects (that's the soft-PipeWire path,
  explicitly out — see Case 2 deferral).
- No modern `.wal` skins (would need the WAL/Maki engine; classic `.wsz` only,
  which *is* the aesthetic).
- No skin museum browser (the Skin Museum DB is private; manual sourcing).
- No gapless playback (nice-to-have, not v1).
