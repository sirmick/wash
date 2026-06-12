# wash-radio (`com.wash.radio`) — internet radio

A native wash app for browsing and playing internet radio (Icecast /
Shoutcast-style streams). It is the full version of the stream seed
already in wash-music (`$WASH_MUSIC_STREAMS`): a proper station browser
with a wash-native UI, reusing the audio control plane and the ingress
proxy. **Free/open directory only** — we list public stream URLs the
station operators publish and play them; we host no content (same model
as VLC/Kodi).

This plan is unbuilt — design only.

## 1. Goals / non-goals

**Goals**
- Browse a large, free, open station directory (search + genre/country
  filters + popularity).
- One-click play of a station; transport + now-playing in the right
  sidebar via the existing `com.wash.audio` control plane.
- Live now-playing track from the stream's ICY metadata.
- Favorites + recents, persisted per session.
- Paste-any-URL for power users (drop in any Icecast/Shoutcast URL).

**Non-goals (v1)**
- No content hosting, no recording, no re-streaming to others.
- No proprietary directories (TuneIn/iHeart) or anything needing a paid
  key or restrictive ToS.
- Not Webamp — this is a wash-native UI (Solid + `@wash/ui`). Music keeps
  the Winamp skin; Radio is a normal wash window.

## 2. Directory source — Radio Browser

**`api.radio-browser.info`** is today's de-facto free/open radio
directory: community-run, CC0 data, **no API key**, ~50k stations,
multiple mirrors (`all.api.radio-browser.info` round-robins). It's what
"free content only" points to.

- Search: `/json/stations/search?name=&tag=&country=&order=clickcount&limit=`.
- Browse: `/json/stations/topvote`, `/json/tags`, `/json/countries`.
- Health: each station carries `lastcheckok` + `lastcheckoktime`; we
  filter to currently-working streams.
- Etiquette (their docs): send a descriptive `User-Agent`, pick a mirror,
  cache, and report plays via `/json/url/<uuid>` (click) so the
  community ranking stays useful. All done **server-side** in the BE.

Optional curated extras later: SomaFM (small, high-quality, explicitly
listener-supported) as a built-in collection. Both feed the same UI.

## 3. Architecture

Two pieces, both reusing existing wash patterns verbatim.

### `com.wash.radio` — window app

`surface=window`, `InstancingSingle` (one radio window). Unlike music it
is **not** pixel-locked, so it's a normal resizable wash frame (no
chromeless flag). The FE is a wash-native Solid UI.

**BE responsibilities** (the interesting part — the ingress proxy does
double duty):

1. **Directory proxy.** Fetch Radio Browser server-side and hand the FE a
   clean station list over `wash:msg` (search/browse/filters). Avoids
   CORS, lets us set the `User-Agent`, choose a mirror, and cache.

2. **Stream proxy + mixed-content/CORS fix.** The shell is served over
   `https`; most radio streams are `http` → the browser blocks them as
   mixed content, and cross-origin `<audio>` has no CORS. So the BE dials
   the upstream stream and republishes it through `PublishIngress`
   (same-origin `/app/<token>/…`), exactly like music serves files. The
   browser's `<audio>` plays the same-origin ingress URL. (See §4.)

3. **ICY metadata extraction.** Browsers don't expose Shoutcast/Icecast
   `StreamTitle`. The BE requests upstream with `Icy-MetaData: 1`, reads
   `icy-metaint`, parses the interleaved metadata blocks server-side
   (`StreamTitle='…';`), **strips them from the bytes it forwards** so the
   `<audio>` gets a clean stream, and reports the current title to the FE
   → control plane → sidebar. This is what makes live now-playing work.

**FE responsibilities** (native UI):
- Search bar + genre/tag/country chips + popularity sort.
- Station list rows: name, bitrate/codec, country, favicon, working badge.
- Favorites + recents (persisted via `app_state`).
- Now-playing panel + transport; a single `<audio>` pointed at the
  ingress-proxied stream.
- "Paste stream URL" box.
- Registers with `com.wash.audio` (register/report/unregister) so the
  sidebar widget shows now-playing and the master mixer applies — same
  contract music uses.

### `com.wash.audio` — unchanged

Radio is just another producer: `Source.kind = "fe-decoded"`, `title =`
ICY StreamTitle (fallback: station name), `artist =` station name. No
service changes; the day-1 `kind` field already covers it.

## 4. Data path

Same Case-1 "fe-decoded" path as music — **full binary, no base64, no
message-channel involvement for the audio bytes** ([[CBOR/JSON pitfall]]):

```
upstream Icecast/Shoutcast (http, ICY)
   │  BE dials upstream with Icy-MetaData:1
BE stream pump: parse + strip ICY blocks → clean audio; capture StreamTitle
   │  PublishIngress (unix socket)
router ingress: httputil.ReverseProxy{ FlushInterval: -1 }  (streamed)
   │  same-origin GET /app/<token>/stream
browser <audio>  → decodes
```

Two channels, like music:
- **Audio bytes**: HTTP through the ingress proxy (raw, streamed).
- **Metadata** (station list, ICY StreamTitle, transport): the small
  JSON `wash:msg` control channel — strings only, never byte fields.

The BE-side ICY strip means the FE never deals with the interleaved
metadata; it just plays a clean stream and renders the title the BE
reports.

## 5. Milestones

- **M1 — play a station, end to end.** BE directory proxy (search +
  topvote) + stream proxy via ingress; FE station list + play; register
  with `com.wash.audio` (now-playing = station name). Paste-URL. Proves
  the directory → proxy → `<audio>` → sidebar loop.
- **M2 — live track metadata.** ICY parse/strip in the BE → StreamTitle
  reported; sidebar + UI show the current track, not just the station.
- **M3 — make it nice.** Favorites + recents (`app_state`), genre/tag/
  country filters, popularity sort, station favicons, click reporting to
  Radio Browser; reconnect-on-drop.
- **M4 — later.** HLS streams (`hls.js`; many stations are plain MP3/AAC
  so this isn't blocking), sleep timer, "now playing" history.

## 6. Testing (e2e)

Hermetic — no live internet in CI:
- A tiny in-test **fake station server**: serves a short looping
  MP3/PCM with `icy-metaint` + a rotating `StreamTitle`, plus a fake
  Radio-Browser `search` endpoint returning that station.
- Assertions (full-stack, the [[wash e2e pattern]]): search lists the
  fake station; clicking play issues an ingress GET that returns
  `200`/streamed; the sidebar now-playing shows the station name (M1) and
  then the rotating ICY title (M2); transport from the sidebar
  round-trips to the `<audio>`.

## 7. Risks / open questions

- **Licensing.** We're a *player* of public streams the operators
  publish (VLC/Kodi model) and host nothing. Free/open directory only.
  Worth a short in-app note that station content is the broadcasters'.
- **Dead/flaky stations.** Filter on Radio Browser's `lastcheckok`;
  reconnect-on-drop; surface clear "stream offline" state.
- **Bandwidth.** Streams flow through the wash host (BE proxy) → uses the
  host's egress. Fine for personal use; note it for multi-user later.
- **Codec/HLS coverage.** Browser `<audio>` handles MP3/AAC/Ogg; HLS
  (`.m3u8`) needs `hls.js` (M4). Filter or label HLS until then.
- **Rate-limit etiquette.** Cache directory queries, set a real
  `User-Agent`, rotate mirrors, report clicks — all BE-side.
- **Relationship to music.** Separate app (distinct UI + lifecycle), but
  it could later share a small `@wash/audio-client` helper for the
  control-plane register/report dance if a third producer appears
  ([[no premature service]] — keep it in-app until then).
```
