# wash demo — outer page

The vite-runnable host page for wash's online demo (PLAN.md Phase 6b).
Boots a Linux VM via [v86](https://github.com/copy/v86) inside the
browser, wires the wash backend's virtio-console output into an
xterm.js boot terminal, and mounts the wash shell once the backend
signals readiness.

## How to run

```sh
# 1. Build the wash shell (the demo serves it from /shell/*).
pnpm -F @wash/shell build

# 2. Supply the four required public/ assets (see below).
ls web/demo/public/
#   libv86.js  v86.wasm  kernel.bin  rootfs.ext2

# 3. Start the dev server.
pnpm -F @wash/demo dev
# → open http://localhost:5180
```

Without the assets the page still loads — the boot terminal lists
which files are missing and where to put them. You can iterate on the
demo's chrome and the wash shell's virtio transport before the kernel
image is ready.

## Required `public/` assets

| File | Source | Notes |
|---|---|---|
| `libv86.js` | [copy/v86 releases](https://github.com/copy/v86/releases) `latest` | Browser-side emulator (~350 KB). |
| `v86.wasm` | same | x86-to-WASM JIT runtime (~2 MB). |
| `seabios.bin` | [copy/v86 repo `bios/`](https://github.com/copy/v86/tree/master/bios) | SeaBIOS used by v86 to handle the bzImage real-mode boot protocol. Without it the kernel never starts. |
| `vgabios.bin` | same | VGA BIOS, required even though the demo doesn't render VGA. |
| `kernel.bin` | custom build (`image/`) | i686, no TCP/IP, virtio-PCI + virtio-console + 8250 console. See PLAN.md §6b. |
| `rootfs.ext2` | custom build (`image/`) | Alpine minirootfs with wash binaries baked in; OpenRC starts `wash-router --transport=virtio-console:/dev/hvc0`. |

## How it wires up

1. `index.html` hosts the xterm.js boot terminal + an offscreen v86
   canvas. The wash shell mounts directly into the document body when
   imported.
2. `src/main.ts` boots v86 with `virtio_console: true`, hooks port 0
   (`hvc0` — kernel console) into the boot terminal, port 1 into a
   `WASH_READY` token watcher, and publishes `emulator.bus` on
   `window.washV86Bus`.
3. The URL is rewritten to `?transport=virtio-console&port=2` before
   the shell bundle is imported.
4. `web/shell/src/main.tsx`'s `pickTransport()` reads the query
   param, finds `window.washV86Bus`, and constructs a
   `VirtioConsoleSocket` (see `web/shell/src/virtio.ts`) targeting
   `/dev/vport0p2` inside the VM.
5. Inside the VM, OpenRC starts `wash-router
   --transport=virtio-console:/dev/vport0p2
   --ready-path=/dev/vport0p1`, which writes `WASH_READY\n` to port 1
   once HandleShell setup is done.
6. The outer page sees the token, fades the boot overlay out, and
   the wash desktop the shell already mounted underneath becomes
   visible.

## Production build

`pnpm -F @wash/demo build` produces a static site in `dist/`. The
shell bundle is *not* automatically copied — add a build step or a
symlink so `dist/shell/shell.js` exists.
