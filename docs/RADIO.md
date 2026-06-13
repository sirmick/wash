# wash-radio (`com.wash.radio`) — minimalist internet radio

A small, **native** wash internet-radio player: one window, basic
transport, a simple station list, and a now-playing info panel. Free/open
stations only — we list public stream URLs operators publish and play
them; we host no content (VLC/Kodi model).

**Implemented** — shipped in 0.9.0 (`apps/radio/`, `com.wash.radio`),
e2e-tested, with live ICY now-playing titles + favorites. Sibling of
`docs/MUSIC.md`; same **list + transport + info** skeleton. Where Music's
"list" is files in a folder, Radio's "list" is stations.

**Architecture (decided 2026-06-12):** a **separate thin app over shared
libraries** (architecture A — see `docs/MUSIC.md`), reusing
`internal/medialib`, the `@wash/ui` media kit, and `@wash/audio-client`.
Not a mode of a combined app.

## 1. Scope — minimalist

**In:**
- Single window. Transport: **play / pause / prev / next** (prev/next step
  the station list). Volume (§6, same as Music).
- A **station list**: a curated built-in set + an optional "Popular" fetch
  (§2). Add/remove your own by pasting a stream URL; the set persists.
- A small **now-playing info** panel: station + live track (ICY).

**Out (v1):** genre/tag/country search & filters, favorites folders,
ratings, station logos galleries, HLS. (Filters/search → "later".)

## 2. Station list — sources (DISCUSS)

The whole free/open landscape, best-first, then a recommended mix:

- **Radio Browser** (`api.radio-browser.info`) — the de-facto open
  directory: free, **no API key**, ~50k stations, CC0, clean JSON,
  mirrored. `/json/stations/topclick/N` (or `/topvote/N`) = **popular**;
  also search/tags/countries (deferred). Server-side fetch (CORS +
  `User-Agent` + cache). Best for the dynamic "Popular" list.
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
  from these — but we *can* play any `.pls`/`.m3u` a user pastes (the BE
  resolves the playlist to its stream URL).

**Recommended mix (minimalist):**
- **Default list = curated**, seeded from **SomaFM** (`channels.json`) +
  **Radio Paradise** + a handful of hand-picked public stations (FIP, KEXP,
  etc.). Works offline-ish, reliable, no key.
- **"Popular" button = Radio Browser** `topclick` (dynamic, optional).
- **"+URL"** — paste any Icecast/Shoutcast/`.pls`/`.m3u`; persists via
  `app_state`.

Open question for you: is curated-SomaFM-first the right default, or do you
want the list to open straight onto Radio Browser "popular"? (See chat.)

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
│ [◁◁] [▷/❚❚] [▷▷]   🔊 ▭▭▭▭▭───  [Popular][+URL] │  ← transport + volume + list ops
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
   so `<audio>` plays a same-origin URL. Also resolves a pasted `.pls`/`.m3u`
   to its underlying stream.
2. **ICY metadata.** Browsers don't expose Shoutcast/Icecast `StreamTitle`.
   The BE requests upstream with `Icy-MetaData: 1`, reads `icy-metaint`,
   parses the interleaved blocks server-side, **strips them from the bytes
   it forwards** (clean audio to `<audio>`), and reports the current track
   title → control plane → sidebar. This is how live now-playing works.

Plus the small **directory proxy** for source B (server-side Radio Browser
fetch + cache). The curated list (A) is just shipped data.

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
(`TransportControls`/`NowPlaying`/`MediaList`/`VolumeSlider`; `SeekBar` runs
in non-seekable "live" mode), and `@wash/audio-client`.

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
`topclick` endpoint returning it. Assert: the curated list renders; tuning
a station issues an ingress GET (200/streamed); the info panel shows the
station then the rotating ICY title; prev/next retunes; "Popular" lists the
fake station; a pasted URL plays; now-playing reaches the sidebar.

## 10. Later (not v1)

Genre/tag/country search & filters (Radio Browser), favorites, station
logos, HLS (`hls.js`), sleep timer, click reporting to Radio Browser.
