# wash-music (`com.wash.music`) — minimalist local music player

A small, **native** wash music player: one window, basic transport, and a
recursive track list from a single selectable folder. This is the
*native* Music app (distinct from **Washamp**, the bonus Webamp/Winamp
app — `docs/AUDIO.md`). It shares the audio control plane + ingress, but
the UI is plain wash (Solid + `@wash/ui`), not a Winamp skin.

**Implemented** — shipped in 0.9.0 (`apps/music/`, `com.wash.music`),
e2e-tested. Sibling of `docs/RADIO.md`; both follow the same
**list + transport + info** skeleton.

**Architecture (decided 2026-06-12):** Music and Radio (and a future
video player) are **separate thin apps over shared libraries** — *not* one
app with modes/tabs, *not* one package with multiple registrations. The
reuse lives in libraries, so apps stay cheap and focused (the fm/disks/net
idiom). Shared foundation (§7): `internal/medialib` (Go scan/serve), a
`@wash/ui` media kit, and `@wash/audio-client`.

## 1. Scope — minimalist

**In:**
- Single window. Transport: **play / pause / prev / next**. Volume (§6).
- A **recursive track list** of one folder (selectable; persists).
- A small **now-playing info** panel for the current track.

**Out (v1):** playlists/queues, tag editing, EQ/visualizers, multiple
roots/library management, search, album art galleries, gapless/crossfade.

## 2. Layout

```
┌─ Music ───────────────────────────────[_][□][x]┐
│ ♪  Artist — Title                  album · 3:42 │  ← now-playing info
│    320 kbps mp3                                 │
├─────────────────────────────────────────────────┤
│  01  Intro                                 1:12 │
│  02  Title (playing)                       3:42 │  ← recursive track list
│  03  …                                          │     (scroll; dbl-click = play)
│  …                                              │
├─────────────────────────────────────────────────┤
│  [◁◁] [▷/❚❚] [▷▷]      🔊 ▭▭▭▭▭───   📁 Folder  │  ← transport + volume + pick-folder
└─────────────────────────────────────────────────┘
```

Normal resizable wash window (not chromeless, not pixel-locked). lucide
icons (SkipBack / Play / Pause / SkipForward / Volume2), matching the
sidebar `AudioWidget`.

## 3. Backend

The BE is essentially Washamp's minus Webamp — scan a folder, serve audio
over ingress:

- **Scan** the selected root recursively for audio files (same ext set as
  Washamp: mp3/flac/wav/ogg/oga/opus/m4a/aac/webm), sorted, capped. Read
  **tags** server-side for sensible info (title/artist/album/duration) via
  a small Go tag lib (e.g. `dhowden/tag`); fall back to the filename stem.
- **Serve** each file as a Range-capable ingress URL (`os.DirFS`-confined
  file server on a unix socket → `PublishIngress`), exactly like Washamp.
- **Folder selection**: the `@wash/ui` `FilePicker` in `directory` mode
  (it already browses wash-side fs via `sdk.EnableFilePicker`). The chosen
  root persists via `app_state`; default `$WASH_MUSIC_DIR`, else `~/Music`.
  Re-scan + re-publish on change.

Washamp and native Music now share folder-scan + ingress-serve logic →
extract a small Go helper (`internal/medialib`: scan→entries, serve→base)
and have both call it ([[no premature service]]: two consumers now, so a
plain library, not a service).

## 4. Playback + control plane

- One `<audio>` element; `next/prev` step the list, `ended` auto-advances.
- Register with **`com.wash.audio`** (`kind:"fe-decoded"`), report
  status/pos/title/artist — same producer contract Washamp uses. Now-playing
  flows to the sidebar `AudioWidget` for free; transport/volume commands
  from the sidebar relay back and drive the same `<audio>`.
- Title/artist come from the BE tag read (anchored, like Washamp's
  now-playing-label fix — never let an empty tag show "Unknown").

## 5. Now-playing info

Sensible, compact: **title · artist · album** (tags, else filename), plus
**duration** and **codec/bitrate**. Duration/bitrate from the BE tag read
(or the `<audio>`/`loadedmetadata`); art deferred.

## 6. Volume — DECIDED (model A)

An **in-window volume slider = this source's volume**, applied client-side
as `el.volume = masterVolume × sourceVolume` (`docs/AUDIO.md §3` — per-source
`volume`; the sidebar slider stays the **master**). The app reports its
`sourceVolume` so state stays truthful. (Rejected: sidebar-master-only; or
the in-window slider driving master directly.)

## 7. Shared foundation (architecture A)

Per the decision above, the reuse lives in libraries that Music, Radio,
the future video player, and the sidebar all consume:

- **`internal/medialib`** (Go) — recursive folder scan → entries (+ tag
  read), and Range ingress serve. Shared by Music + Washamp (+ video).
- **`@wash/ui` media kit** (Solid components, extracted from the sidebar
  `AudioWidget` — its 3rd+ consumers): `TransportControls` (prev/play-pause/
  next), `VolumeSlider`, `NowPlaying` (info header), `MediaList` (selectable
  list w/ playing-indicator + render-prop rows), optional `SeekBar`
  (`seekable` flag: Music seeks, Radio is live).
- **`@wash/audio-client`** (FE logic) — `createAudioSource({title, onCmd})
  → { report, set, dispose }` for the `com.wash.audio` register/report/
  transport dance; the service's active-source + single-play exclusivity
  (`docs/AUDIO.md §3`) then makes the sidebar one global transport.

Build order: the shared kit + service policy first (AUDIO.md M4),
refactoring the sidebar onto it; then Music, then Radio, then (later) video
— each a thin app on this foundation.

## 8. Testing (e2e, hermetic)

Seed a temp folder with a couple of tag-bearing + tag-less files; assert
the recursive list shows them, play streams over ingress (200/206), the
info panel shows tag title (and filename for the tag-less one, not
"Unknown"), next/prev move the selection, and now-playing reaches the
sidebar. Folder-pick: drive the `FilePicker` to a second seeded dir and
assert the list re-scans.
