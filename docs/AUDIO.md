# AUDIO — wash audio subsystem & music player

Status: **implemented** (0.9.0; ex-branch `wash-audio`). M1–M4 done and
e2e-green — incl. per-source volume + active-source / single-play
exclusivity. Cross-window persistence and server-live audio (Case 2) deferred.

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

### `com.wash.washamp` — Winamp window

`surface=window`, `InstancingSingle` (one Winamp). FE embeds **Webamp**
(npm `webamp`, MIT) for the pixel-perfect classic-skin UI, playlist, EQ, and
Butterchurn visualizer — Webamp is the JS implementation of the Winamp skin
engine; we consume it, we don't reimplement skinning. BE scans the music
library, serves files over ingress, and registers/reports playback to
`com.wash.audio`.

Window geometry: classic Winamp is pixel-locked (main window 275×116; EQ and
playlist are fixed sub-windows). The window is **chromeless** —
`WindowHints{Resizable:false, Chromeless:true}` sized to the main window's
275×116 footprint. The shell renders a chromeless window with no titlebar and
no border (`web/shell/src/window.tsx`), so Webamp's own Winamp titlebar is the
only titlebar (no double chrome) and the UI sits flush (no black margin).

Webamp renders its UI as a `#webamp` overlay appended to `<body>` (it does not
render into the node passed to `renderWhenReady`), so the FE reparents `#webamp`
into the window slot and pins the main window to the slot origin; the EQ and
playlist stack below and the whole `#webamp` moves as one. Because there is no
wash titlebar, the FE drives the window from Winamp's native chrome via
`window.wash`: a drag on the main-window titlebar is intercepted (capture-phase,
suppressing Webamp's own drag) and translated into `moveWindow`; the minimize
button calls `minimizeWindow`; and Webamp's `onClose` calls `closeWindow`. The
generic `WindowHints.Chromeless` flag is reusable by any future app that ships a
pixel-locked native UI.

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

### Active source + single-play exclusivity

The service is the only global observer, so it owns two cross-source
policies (the sidebar and producers stay dumb):

- **Active source.** `AudioState` carries an `activeId` — the source the
  sidebar shows + drives. It's set to whichever source last went
  `playing`, and *kept across pause* so the widget still targets "the
  thing you'd resume" when nothing is playing. (Before this, the sidebar
  just showed `sources[0]` — the newest-registered, not the playing one.)
- **Single-play exclusivity (default on).** When a producer `report`s
  `status:"playing"`, the service relays a `pause` `cmd` to every *other*
  source currently `playing`. So starting Music auto-pauses Washamp/Radio
  (and a future video) — one thing plays at a time. Producers never learn
  about each other; no feedback loop (the paused one reports `paused`,
  which doesn't re-trigger). This is the natural seam for richer
  ducking/exclusivity later (e.g. duck-don't-pause for chimes).

Together these make the sidebar a single global transport for "whatever is
playing now" across every media app.

## 4. Sidebar

`AudioWidget` in `apps/session/fe/src/sidebar/` subscribes to `AudioState`
through the session BE gateway (the `NotifyWidget` pattern exactly): now-playing
title/artist/progress, transport buttons, master volume slider, per-source
mute. It shows/drives the **active source** (`activeId`, §3); transport
buttons send cross-app messages the service relays to the owning producer.
Pure renderer; the gateway does the wiring. The transport cluster +
now-playing line are the same extracted `@wash/ui` media components the
Music/Radio (and future video) windows use, so all three places stay
consistent.

## 5. Milestones

- **M1 — Webamp plays a track over ingress.** Scaffold `apps/washamp/{be,fe}`.
  BE: fixed non-resizable window; Range-capable file server on a unix socket →
  `PublishIngress`; serve a bundled sample track. FE: embed `webamp`,
  `renderInto` the host, default skin, `setTracksToPlay` with the ingress URL.
  Wire Makefile + `cmd/wash` imports. Green build + e2e (window opens, Webamp
  main window present, playback starts). *Proves: webamp bundles under Vite,
  ingress serves Range audio with correct CORS, fixed-size window.*

- **M2 — library + playlist + skins.** DONE. BE scans a music dir
  (`$WASH_MUSIC_DIR`, default `~/Music`) recursively for audio files, serves
  them over ingress with a Range-capable file server (`os.DirFS`-confined,
  per-segment URL-escaped), and falls back to the synth sample when empty.
  Streams: `$WASH_MUSIC_STREAMS` (comma-separated Icecast/HTTP URLs) appended
  verbatim. Skins: Webamp's default renders, and dropping a `.wsz` onto the
  player re-skins it (Webamp's native drag-drop — no code). A hosted
  skin-picker / museum browser stays deferred (skins are author-owned fan
  art; no third-party art bundled).

- **M3 — control plane + sidebar.** `com.wash.audio` (`StateService`) +
  session gateway + `AudioWidget`. Washamp registers/reports; sidebar shows
  now-playing + master volume + transport and drives the player cross-app.

- **M4 — service policy + shared media kit.** `activeId` + single-play
  exclusivity in `com.wash.audio` (§3); per-source volume (model A — in-app
  volume slider, `el.volume = master × source`); extract the shared
  `@wash/ui` media components (`TransportControls`/`NowPlaying`/`MediaList`/
  `VolumeSlider`) + `@wash/audio-client` from `AudioWidget`, refactoring the
  sidebar to consume them. Foundation for the native Music/Radio apps
  (`docs/MUSIC.md`, `docs/RADIO.md`) and a future video player.

- **M5+ — deferred.** Persistence (keep playing on window close) via a
  persistent host element owned outside the closable window; **Case 2**
  server-live audio folded into the display/wayland WebRTC milestone.

## 6. Non-goals (v1)

- No server-side mixing/decoding/effects (that's the soft-PipeWire path,
  explicitly out — see Case 2 deferral).
- No modern `.wal` skins (would need the WAL/Maki engine; classic `.wsz` only,
  which *is* the aesthetic).
- No skin museum browser (the Skin Museum DB is private; manual sourcing).
- No gapless playback (nice-to-have, not v1).
