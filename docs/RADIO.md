# wash-radio (`com.wash.radio`) — minimalist internet radio

A small, **native** wash internet-radio player: one window, basic
transport, a simple station list, and a now-playing info panel. Free/open
stations only — we list public stream URLs operators publish and play
them; we host no content (VLC/Kodi model).

**Implemented** — shipped in 0.9.0 (`apps/radio/`, `com.wash.radio`),
e2e-tested, with a broad-genre tree, live ICY now-playing titles,
stream details, and favorites. Sibling of `docs/MUSIC.md`; same **list +
transport + info** skeleton. Where Music's "list" is files in a folder,
Radio's "list" is stations.

**Architecture (decided 2026-06-12):** a **separate thin app over shared
libraries** (architecture A — see `docs/MUSIC.md`), reusing
`internal/medialib`, the `@wash/ui` media kit, and `@wash/audio-client`.
Not a mode of a combined app.

## 1. Scope — minimalist

**In:**
- Single window. Transport: **play / pause / prev / next** (prev/next step
  the station list). Volume (§6, same as Music).
- A **nested station tree**: a seeded user-configurable set grouped by broad genres
  (Electronic, Ambient, Rock, Pop, Hip-Hop/R&B, Country/Americana, Latin/World,
  Jazz, Classical, Reggae/Ska, Metal, Folk, Oldies/Soul, Lounge, Eclectic)
  and subtypes where useful. Electronic includes downtempo/chill, deep house,
  progressive/trance, IDM, drum & bass, dubstep/bass, electropop/synthpop,
  vaporwave, ambient/space, hacker/cyberpunk, industrial/dark ambient, and
  techno; Rock includes alternative/modern, indie rock, punk rock, hard rock,
  and nu-metal. Edit `~/.config/wash/radio-stations.json` for the fixed list,
  or add/remove your own by pasting a stream URL; pasted stations persist under
  Custom. On launch the tree starts fully collapsed, except for the saved
  last-played station's genre/subtype path.
- A small **now-playing info** panel: station + live track (ICY) plus
  stream headers when available (`Content-Type`, `icy-br`, `icy-name`,
  `icy-genre`, `icy-url`, `icy-description`, `icy-metaint`).

**Out (v1):** tag/country search & filters, favorites folders, ratings,
station logos galleries, HLS. (Filters/search → "later".)

## 2. Station list — sources (DISCUSS)

The whole free/open landscape, best-first, then a recommended mix:

- **Radio Browser** (`api.radio-browser.info`) — the de-facto open
  directory: free, **no API key**, ~50k stations, CC0, clean JSON,
  mirrored. `/json/stations/topclick/N` (or `/topvote/N`) = **popular**;
  also search/tags/countries (deferred). Best for a future dynamic
  directory/search path.
- **SomaFM** (`somafm.com/channels.json`) — not a directory but a curated
  set of ~40 excellent commercial-free, listener-supported channels with a
  clean JSON + direct streams. Reliable, high-quality, free → ideal as the
  built-in **curated** collection.
- **Radio Paradise** (`api.radioparadise.com`) — a few hand-curated
  channels (incl. lossless), clean API, free → nice curated additions.
- **Xiph / Icecast public directory** (`dir.xiph.org`) — open YP listing of
  Icecast streams; smaller/older, scrappier data. Optional fallback.
- **Shoutcast directory** (`directory.shoutcast.com`) — huge, but needs a
  (free) Dev API key + ToS strings. Skip for v1 (key friction).
- **internet-radio.com / TuneIn / iHeart** — **no clean API**.
  internet-radio.com only exposes `.pls`/`.m3u` links on HTML pages (scrape
  + playlist-parse, fragile); TuneIn/iHeart are proprietary. We don't pull
  from these. Direct stream URLs from any source can still be pasted.

**Recommended mix (minimalist):**
- **Default list = user-configurable JSON**, seeded on first run from
  `apps/radio/be/default-stations.json` into
  `~/.config/wash/radio-stations.json`. The seed is curated from **SomaFM**,
  **Radio Paradise**, Nightride FM, and a handful of hand-picked public
  stations. Works offline-ish, reliable, no key. Current emphasis: breadth
  across popular genres, with electronic and rock exposed as nested subtype
  groups. Edit the JSON file to add/remove/reorder fixed stations.
- **"+URL"** — paste a direct Icecast/Shoutcast/native audio stream URL;
  persists via `app_state`. Playlist URLs (`.pls`, `.m3u`, HLS `.m3u8`) are
  later work.

## 3. Layout

```
┌─ Radio ───────────────────────────────[_][□][x]┐
│ 📻 SomaFM — Groove Salad             128k aac   │  ← station + live track
│    ♪  Bonobo — Kerala                           │     (ICY StreamTitle)
├─────────────────────────────────────────────────┤
│  ● SomaFM Groove Salad (playing)                │  ← station list
│  ○ FIP                                           │     (dbl-click = tune in)
│  ○ Radio Paradise                               │
│  …                                              │
├─────────────────────────────────────────────────┤
│ [◁◁] [▷/❚❚] [▷▷]   🔊 ▭▭▭▭▭───       [+URL] │  ← transport + volume + list ops
└─────────────────────────────────────────────────┘
```

Normal resizable wash window; lucide icons, matching Music + the sidebar.

## 4. Backend — the part that makes browser radio actually work

The BE does two essential jobs (not optional polish — browsers can't play
most radio without them), both via the ingress proxy:

1. **Stream proxy + mixed-content/CORS fix.** The shell is `https`; most
   streams are `http` → the browser blocks them as mixed content, and
   cross-origin `<audio>` has no CORS. The BE dials the upstream stream and
   republishes it through `PublishIngress` (same-origin `/app/<token>/…`),
   so `<audio>` plays a same-origin URL.
2. **ICY metadata.** Browsers don't expose Shoutcast/Icecast `StreamTitle`.
   The BE requests upstream with `Icy-MetaData: 1`, reads `icy-metaint`,
   parses the interleaved blocks server-side, **strips them from the bytes
   it forwards** (clean audio to `<audio>`), and reports the current track
   title → control plane → sidebar. This is how live now-playing works.
3. **Stream details.** When an upstream connects, the BE forwards safe
   response/header facts to the FE: content type, bitrate, station/genre
   headers, public station URL/description, and metadata interval. This is
   best-effort; many stations omit some or all of these fields.

The default station template (A) is shipped data and copied to user config on
first run. A server-side Radio Browser fetch/cache path remains a later
directory/search feature.

## 5. Playback + control plane

- One `<audio>` pointed at the ingress-proxied stream; `prev/next` retune to
  the adjacent station. No seek (live).
- Register with **`com.wash.audio`** (`kind:"fe-decoded"`): `title =` ICY
  StreamTitle (fallback station name), `artist =` station name. Same
  producer contract as Music/Washamp → sidebar now-playing + transport for
  free.
- Reconnect-on-drop; clear "stream offline" state for dead stations.

## 6. Volume — DECIDED (model A)

Same as `docs/MUSIC.md §6`: in-window per-source volume + sidebar master,
`el.volume = masterVolume × sourceVolume`.

## 7. Shared foundation

Same as `docs/MUSIC.md §7`: `internal/medialib` (here the "serve" leg is
the §4 stream-proxy rather than a file server), the `@wash/ui` media kit
(`TransportControls`/`NowPlaying`/`VolumeSlider`; `SeekBar` runs in
non-seekable "live" mode), and `@wash/audio-client`.

## 8. Data path

Case-1 "fe-decoded" — **full binary over the ingress proxy, no base64**
([[CBOR/JSON pitfall]]); metadata (station list, ICY title, transport) on
the small JSON `wash:msg` channel:

```
upstream Icecast/Shoutcast (http, ICY) ─ BE dials w/ Icy-MetaData:1
  → BE pump: strip ICY blocks → clean audio; capture StreamTitle
  → PublishIngress (unix socket)
  → router ingress ReverseProxy{FlushInterval:-1} (streamed)
  → same-origin GET /app/<token>/stream → browser <audio>
```

## 9. Testing (e2e, hermetic — no live internet)

In-test **fake station server**: serves a short looping MP3/PCM with
`icy-metaint` + a rotating `StreamTitle`, plus a fake Radio-Browser
`topclick` endpoint returning it. Assert: the seeded station list renders;
tuning a station issues an ingress GET (200/streamed); the info panel shows
the station then the rotating ICY title; prev/next retunes in tree order; a
pasted URL plays; now-playing reaches the sidebar.

## 10. Last.fm

Possible, but separate from basic playback:

- `track.getInfo` can enrich parsed ICY titles with Last.fm tags/wiki/URLs
  using a Last.fm API key and no user auth.
- `track.updateNowPlaying` and `track.scrobble` require an API key, API
  signature, and authenticated Last.fm session key, and must be POSTed.
- Good integration point: parse `Artist - Track` from ICY, debounce title
  changes, then optionally call Last.fm only when a key/session is
  configured. Keep it opt-in because many radio StreamTitle values are
  messy and not every transition represents a user-listened scrobble.

## 11. Later (not v1)

Tag/country search & filters (Radio Browser), favorites folders, station
logos, HLS (`hls.js`), sleep timer, click reporting to Radio Browser, and
optional Last.fm enrichment/scrobbling.
