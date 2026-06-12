# wash-music (`com.wash.music`) — minimalist local music player

A small, **native** wash music player: one window, basic transport, and a
recursive track list from a single selectable folder. This is the
*native* Music app (distinct from **Washamp**, the bonus Webamp/Winamp
app — `docs/AUDIO.md`). It shares the audio control plane + ingress, but
the UI is plain wash (Solid + `@wash/ui`), not a Winamp skin.

Unbuilt — design only. Sibling of `docs/RADIO.md`; both follow the same
**list + transport + info** skeleton and should share a tiny audio-source
client (§7).

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

## 6. Volume — OPEN (see chat discussion)

Recommended: an **in-window volume slider = this source's volume**, applied
client-side as `el.volume = masterVolume × sourceVolume` (the model in
`docs/AUDIO.md §3` — per-source `volume` already exists; the sidebar slider
stays the **master**). The app reports its `sourceVolume` so state stays
truthful. Alternatives discussed in chat: (B) no in-window volume, rely on
the sidebar master only; (C) the in-window slider drives master directly.
Pick one before build.

## 7. Shared audio-source client

Washamp + Music + Radio all do the same register / report / unregister +
receive-transport dance. Extract a tiny FE helper
`@wash/audio-client`: `createAudioSource({title, onCmd}) → { report, set,
dispose }`. Build it here, reuse in Radio, optionally retrofit Washamp.

## 8. Testing (e2e, hermetic)

Seed a temp folder with a couple of tag-bearing + tag-less files; assert
the recursive list shows them, play streams over ingress (200/206), the
info panel shows tag title (and filename for the tag-less one, not
"Unknown"), next/prev move the selection, and now-playing reaches the
sidebar. Folder-pick: drive the `FilePicker` to a second seeded dir and
assert the list re-scans.
