# Images: thumbnails, folder preview, viewer, and open-routing

Status: planning → in progress (branch `wash-images`).

This covers four related pieces that together make wash handle images and
"open this file" like a real desktop:

1. **`internal/thumbs`** — a zero-dependency image decode → downscale → cache
   library, plus the BE/FE helpers that move image bytes over the existing
   wire (no HTTP, no ingress, no new service).
2. **fm folder preview** — selecting a folder renders a tile grid in the
   preview pane: a lucide icon per file type, and a real thumbnail for images.
3. **`com.wash.imageview`** — a small image viewer app: a thumbnail list on the
   left, a zoom/pan view on the right.
4. **Open-routing in the router** — double-click a file in fm (or any future
   caller) and the router launches the right app, passing the path as argv.

## Why no service and no HTTP (the transport decision)

Two transports were considered for getting image bytes from a BE to a browser
`<img>`:

- **HTTP ingress** (the washamp/`medialib` pattern): efficient and browser-
  native, but ingress does **not** exist on the in-browser VM transport
  (virtio), and it forces each app to run an HTTP server.
- **base64 over app_msg**: works everywhere but bloats binary by 33% and shares
  the one control WS (head-of-line blocking on large images).

We use neither. The wire already carries **raw binary channels** (the same
mechanism behind terminal PTYs and the display app's video, `ChannelKindVideo`
/ `ChannelKindGeneric`). An app opens a `channel.open`, the router binds it to
that app's window, and the FE reads `Uint8Array`s via `subscribeRaw`. This:

- rides the **existing transport**, so it works on the in-browser VM too;
- carries raw bytes — **no base64**, no JSON envelope;
- needs **no HTTP server and no ingress**.

The one constraint that shaped the packaging: **raw channels are owner-scoped**
— a channel binds to the *opening app's own window*. A separate background
"assets service" could not stream a channel into fm's FE (it has no window), so
it would be forced back into a cross-app base64 hop. Therefore the image
pipeline is a **shared library each app embeds**, not a service: each app's own
BE streams to its own FE. The on-disk cache is the shared authority.

### Text preview is unchanged

fm's text preview stays on the existing `read` → `read_ok` app_msg path. Text
content is a UTF-8 *string*, which marshals to a plain JSON string with **no
base64 tax** — the raw channel only exists to dodge base64 for *binary*. So the
channel helper is purely an image-bytes concern; we do not re-plumb text.

## `internal/thumbs` (library)

Pure-Go, **no new dependencies** (stdlib `image/jpeg`, `image/png`, `image/gif`
decoders; `image/jpeg` encoder). Formats beyond jpeg/png/gif (e.g. WebP) are
out of scope for v1 and degrade to the file-type icon; adding
`golang.org/x/image` later (still pure Go) is the upgrade path if WebP matters.

- **Scaler**: area-averaging box downscale to a max dimension, aspect-preserving.
  Good enough at thumbnail size; no `x/image` needed.
- **Cache**: `~/.cache/wash/thumbs/` (`os.UserCacheDir` + `wash/thumbs`, fallback
  `~/.cache`). Key = `sha256(abspath | mtime | size | dim)`. Atomic temp+rename
  (mirrors `internal/fs/mutate.go`). Cache invalidates implicitly on mtime/size
  change — no watching.
- **API** (sketch):
  - `Get(absPath string, mtime, size int64, maxDim int) (cachePath string, err error)`
    — generate-if-absent, return the cached JPEG path.
- **Confinement**: callers pass already-confined absolute paths (each app
  confines with its own `fs.FS` root, exactly as fm's `read` does today).

### BE helper (in `internal/thumbs`)

`ServeOverChannel(conn, req)` — given `{path, dim?}`, resolve bytes (full file
for `dim==0`, else `Get(...)`), open a generic raw channel bound to the app's
window, stream the bytes, close it. Replies an app_msg `{kind:'file_ready',
req_id, channel_id, mime, size}` so the FE can correlate.

### FE helper (in `@wash/ui`)

`washFileUrl(instance, path, opts?) → Promise<string>` — send the request,
`subscribeRaw` the announced channel, accumulate until close, `new Blob` →
`URL.createObjectURL`, return the URL for `<img src>`. Maintains a small
in-memory blob cache keyed by `path|dim` (revoke on eviction). Loading is driven
by an **IntersectionObserver** (only fetch tiles in view) with a **small
concurrency cap**, since everything shares one WS and large transfers can
head-of-line-block (cf. the fm upload-cancel stall).

## fm folder preview (piece 2)

Today the preview pane shows text/binary for a selected *file* and nothing for a
folder. Add a **grid mode**: when the selected row is a directory, render its
entries as tiles.

- Non-image entry → a **lucide icon by extension** (new `iconForEntry(name,type)`
  map: FileText/FileImage/FileCode/FileArchive/Music/Video/…), plus the name.
- Image entry → `<img loading=lazy src={washFileUrl(instance, abspath, {dim:160})}>`.
- Undecodable image format (e.g. WebP) → falls back to the FileImage icon.
- Works on the in-browser VM (raw channels are transport-agnostic).
- Tiles are clickable (navigate / select). Depends on the fm entries already
  listed for that folder.

Builds on the just-landed fm preview-dock work (fixed-width dock, toggle,
responsive columns).

## `com.wash.imageview` (piece 3)

New app, `Surface: window`, `Instancing: multi`, lucide icon `image`.

- **BE**: import `internal/thumbs`; scan the opened image's sibling folder for
  image files (cap + sort); serve full bytes + thumbnails over the channel
  helper. Parse the `--open <path>` launch arg (see open-routing) to know which
  image to show first and which folder to list.
- **FE**: left thumbnail list (Splitter) + main view. Main view is a single
  `<img>` at full bytes with `transform: scale()+translate()`: wheel = zoom,
  drag = pan, a fit/reset control, arrow keys = prev/next. Scope is deliberately
  small — zoom/pan/prev-next, no edit/rotate in v1.

Full new-app wiring checklist: `apps/imageview/{be,fe}`, `be/cmd/main.go`,
`cmd/wash/imports_imageview.go`, Makefile (`BINS`, `*_ASSETS`/`*_STAMP`,
`web-imageview`, embed rule, build target, `MULTICALL_STAMPS`),
`packaging/wash.binaries`, icon in `web/shell/build-icons.mjs`, e2e fixture +
spec.

## Open-routing in the router (piece 4)

No mime/open-with system exists today; double-clicking a file in fm is a no-op.
Build routing into the **router**, which already owns both the manifests and the
spawn machinery.

- **Manifest**: add `Opens []string` (extensions) to `sdk.Manifest`. edit →
  text/code exts; imageview → image exts. The router builds an `ext → appID`
  index at registration (wash's `mimeapps.list` equivalent, but declarative).
- **`open {path}` event**: a new router-handled event (sibling to
  `EvtSpawnRequest`). The router resolves ext → app and **spawns it with argv**
  `["--open", path]` — generalizing the existing per-spawn args mechanism
  (`RootVariant.Args`, used by `wash-term --login`).
- **SDK** parses `--open <path>` centrally and surfaces it to the app; the BE
  then sends its FE `{kind:'open_file', path}` on ready — a normal message.
- **fm**: double-click a file → emit `open {path}`.
- **edit + imageview**: handle the `open_file` launch arg.

Property: argv is per-process, so each open = a fresh window with its own path
(right for the multi-instance viewers). Retargeting an already-running singleton
window would need the wire-message path — explicitly out of scope.

## Build order (each an independently committable, green checkpoint)

1. `internal/thumbs` (scaler + cache) + BE channel-server + `@wash/ui` FE blob
   helper. Unit tests for the scaler/cache.
2. fm folder-grid preview (consumes the helper) + ext→icon map. *(depends on the
   fm preview-dock work being in this branch's base.)*
3. `com.wash.imageview` app (full new-app wiring).
4. Router open-routing: `Opens` + ext index + `open` event + `--open` argv +
   fm double-click + edit/imageview handlers.
5. Finalize: packaging/registration drift check, full test, merge to main.

## Folder-grid interactions (selection / menu / DnD / sort)

The folder grid is a second view over the fm **tree's** existing machinery —
adding interaction is wiring tiles to the same handlers the rows use, not new
behaviour:

- **Windowing**: `@wash/ui` `VirtualGrid` renders only the tiles near the
  viewport (fixed tile height → computed row pitch; raw item refs so `<For>`
  reuses tiles across scroll). A 2000-image folder mounts ~dozens of nodes.
- **Selection**: `onRowClick`'s Shift-range domain is parameterized; `gridClick`
  runs the same `nextSelection` kernel (`selection.ts`) over the grid's order.
  One shared `selection()` set; tiles render `data-selected`.
- **Right-click**: tiles call the existing `openContextMenu` / `ContextMenu`.
- **Drag copy/move**: tiles reuse `onDragStart` / `onRowDragOver` / `onRowDrop`
  (drop handlers on folder tiles only) — left-drag move, right-drag copy menu.
- **Sort**: grid renders `sortedFiltered(listing, {sort, showHidden})`
  (`@wash/fs-client`) — the same comparator the tree uses — so it follows the
  toolbar sort / column headers.
- **Double-click**: folder drills in, file opens via the router's open routing.

Key structural point: the grid's visibility is driven by a dedicated `gridDir`
signal, **independent of `selectedEntry`**. Clicking/right-clicking a tile
updates `selectedEntry` + `selection` (so the info panel and highlight follow)
WITHOUT flipping the dock to a file preview and hiding the grid. `gridDir` is set
only when a folder becomes the preview target (tree folder click / navigate-in).

## Decisions locked

- **Transport**: WS raw channels (no HTTP, no ingress, no service). Works on the
  browser VM.
- **Scaler**: zero-dependency, jpeg/png/gif only; WebP/`x/image` deferred.
- **Text preview**: unchanged (`read_ok` app_msg).
- **Big folders**: windowed (`VirtualGrid`), not capped — fm grid unbounded,
  imageview list capped at 5000 (bounds the one `scan_ok` message, not the DOM).
- **Grid interactions**: reuse the tree's handlers; `gridDir` decouples grid
  visibility from `selectedEntry`.
