# wash-vm/web — browser host for the RISC-V demo

The page that loads in a tab and ends up running a real wash desktop
inside a real Linux VM. Vite for the dev loop, a thin Node server on
top for the `/ws` log/control bus, vanilla static HTML when deployed.

## How it boots, step by step

1. `index.html` loads `/tinyemu/term.js` + `/tinyemu/jslinux.js`
   (vendored from bellard.org — `jslinux.js` is the wasm loader, not
   our code) and the bridge module `src/tinyemu-bridge.ts`.
2. The bridge waits for `window.term` to exist, mounts an xterm.js
   terminal into `#term_container` for the kernel view, and overrides
   Bellard's canvas `Term.write` to forward bytes into xterm + the WS
   log bus.
3. jslinux fetches `wash-riscv64.cfg`, `opensbi-fw_jump.bin`,
   `wash-kernel.bin`, and `wash-rootfs/blk.txt` (the chunk index).
   TinyEMU starts emulating; the kernel boots; busybox-init runs
   `wash-supervisor` → `wash-router`.
4. Multiport virtio-console carries the wash protocol between the
   browser and the in-VM wash-router:
   * `vport0p0` → router data plane (wash protocol frames)
   * `vport0p1` → router stdout + supervisor log (rendered in the
     **wash log** tab)
   * `vport0p2` → periodic system diagnostics (`ps`, `uptime`, `df`,
     rendered in the **diag** tab)
   * `hvc1` → buildroot getty (login shell in the **shell** tab)
5. First bytes on `vport0p0` flip the visible tab from **kernel** to
   **wash** and the desktop mounts via `src/shell-bootstrap.ts`,
   which fetches `shell.js` over the wash asset channel and imports
   it as a module.

## Run locally

Prerequisites: `pnpm install` at the repo root.

```sh
# 1. Build the wash shell (the page imports /shell/shell.js).
pnpm -F @wash/shell build

# 2. Build the VM artifacts (kernel/firmware/rootfs/wasm) if you haven't.
make -C wash-vm/image all

# 3. Start the dev server.
cd wash-vm/web
node server/server.mjs            # → http://localhost:5180
                                  # PORT=12000 node server/server.mjs to relocate
```

The dev server is a Vite middleware host with two extras:

* `/ws` — JSON message bus. Browser pushes `log` / `stage` frames;
  admin clients (CLI, your terminal) push `ctl` (reload / dump / mem)
  and `input` (inject keystrokes into a virtio-console port). The
  bridge falls back to a queue-and-retry loop if `/ws` isn't there.
* `/admin/<verb>` — HTTP shim for the same verbs (`curl -X POST
  :5180/admin/reload` etc.).

Tail the live boot log:

```sh
tail -F /tmp/wash-demo-server.log | grep -vE 'diag #|\[diag\]|\[heap\]|\[rate\]'
```

## What lives in `public/tinyemu/`

| File | Owner | Notes |
|---|---|---|
| `wash-riscv64.cfg`       | hand-edited | TinyEMU machine cfg — memory, cmdline, drive0 path |
| `wash-kernel.bin`        | `make -C ../image install` | Linux Image |
| `opensbi-fw_jump.bin`    | same | OpenSBI firmware |
| `riscvemu64-wasm.{js,wasm}` | same | TinyEMU emcc output |
| `wash-rootfs.ext2`       | same | Full 20 MiB image (kept around for inspection) |
| `wash-rootfs/blk*.bin`   | same | 2 MiB chunks TinyEMU's `drive0` actually reads |
| `wash-rootfs/blk.txt`    | same | Manifest pointing at the chunks |
| `term.js`, `jslinux.js`  | vendored bellard.org | WASM loader + (now-hidden) canvas Term |
| `style.css`              | vendored bellard.org | Bellard Term CSS (kept for the measurement div) |

## Deploy as static HTML

The demo is fully static-deployable (GitHub Pages, Cloudflare Pages,
Netlify, S3 + CloudFront — anything that serves byte ranges so the
chunked rootfs `Range:` reads work).

```sh
# 1. Build the shell bundle.
pnpm -F @wash/shell build

# 2. Build the VM artifacts (if not already in public/tinyemu/).
make -C wash-vm/image all

# 3. Vite-build the host page.
cd wash-vm/web
pnpm exec vite build              # → dist/

# 4. Copy in the things the dev server otherwise proxies on the fly.
mkdir -p dist/shell
cp -a ../../web/shell/dist/. dist/shell/
cp ../../internal/runner/router/assets/icons.svg     dist/
cp ../../internal/runner/router/assets/wash-logo.svg dist/

# 5. Trim the cruft Vite's public/ copy includes.
rm -f dist/tinyemu/riscvemu64-wasm.{js,wasm}.{broken-dev,prod}
rm -f dist/v86.wasm                      # leftover from the v86-era demo
rm -f dist/tinyemu/wash-rootfs.ext2      # 20 MiB duplicate of wash-rootfs/blk*.bin
                                          # — TinyEMU reads the chunks, not the
                                          # unsplit image. Drops dist/ from
                                          # ~45 MiB to ~24 MiB.

# 6. Push dist/ to your static host of choice.
```

### GitHub Pages

If you're hosting under `https://<user>.github.io/<repo>/`, set
Vite's base path so asset URLs resolve correctly:

```sh
pnpm exec vite build --base=/<repo>/
```

Then push `dist/` to a `gh-pages` branch or the repo's
"Pages → Build from a branch / folder" source. For a single
top-level `https://<user>.github.io/`, no `--base` flag is needed.

### Hosting notes

* The page makes a **WebSocket** connection to `/ws` (the dev-server
  log bus). On static hosting that connection fails, the bridge
  goes into reconnect backoff (250 ms → 5 s, capped), and the page
  keeps working — logs just don't leave the tab. If you want it
  silenced, gate the `connect()` call in `src/dbg.ts` on
  `import.meta.env.DEV`.
* The rootfs is **chunked** (10 × 2 MiB) and fetched on demand by
  TinyEMU's virtio-blk path. Your host must honor HTTP `Range:`
  reads — every static host worth using does, but check if you're
  serving from S3 + an unusual CloudFront config.
* Total cold-boot transfer is **≈4 MiB** (kernel + firmware + a few
  rootfs chunks the kernel touches before `/sbin/init`). Subsequent
  chunks lazy-load as the kernel reads them.
* Compression: gzip transfer-encoding gives a further ~50 % on the
  text-y `.js` + the kernel; the ext2 chunks barely compress.
  Pre-`gzip` the artifacts and serve with `Content-Encoding: gzip`
  if your host supports it.

### Linking from your docs site

The simplest path: drop the `dist/` output under a `demo/`
subdirectory of your project's GitHub Pages site, then link to it
with a regular `<a href="demo/">`. Visitors get a full wash desktop
running in their browser, ~3-5 s from click.

## Architecture details (for the curious)

* `src/tinyemu-bridge.ts` is the single source of truth — it owns
  the term tap, the xterm.js mount, the Module lifecycle hooks, the
  virtio-console / multiport bus shim, and the shell-bootstrap kick.
* `src/dbg.ts` is the WebSocket bus — log frames, queue, reconnect.
* `src/shell-bootstrap.ts` fetches the shell bundle over the wash
  asset channel and hands it to a `<script type="module">` import.
* `server/server.mjs` is the dev server — Vite middleware + a `ws`
  WebSocketServer on `/ws` + an HTTP admin shim. Not used in
  production; static hosting needs none of it.

## Iterating

* **Front-end only** (bridge / xterm / shell wiring): Vite HMR picks
  up `src/*.ts` edits.
* **Shell** (`web/shell/src/...`): `pnpm -F @wash/shell build`,
  reload the tab — the page imports the new bundle.
* **Wash backend** (`internal/runner/router/*`, apps): `make -C
  wash-vm/image rootfs install`, reload — the new multicall is
  baked into the rootfs.
* **Kernel config / firmware** (`wash-vm/image/{kernel,firmware}/*`):
  `make -C wash-vm/image {kernel,firmware} install`, reload.
* **TinyEMU C / lib.js**: `make -C wash-vm/image wasm install`,
  reload.

There's no live-reload back into the VM from the host — every
rebuild requires a page reload, but boot is fast enough (~3 s cold)
that this is fine.
