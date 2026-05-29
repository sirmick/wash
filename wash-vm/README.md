# wash-vm — wash inside a browser-tab RISC-V Linux VM

`wash-vm/` packages everything needed to run wash inside a real Linux
VM (kernel + rootfs + emulator) in a browser tab. The emulator is
vendored TinyEMU compiled to WASM; the kernel is upstream Linux 6.6
LTS for RISC-V; the rootfs is a buildroot+musl userland with the wash
multicall binary baked in.

End result: open a page, ~3 seconds later you have a working wash
desktop running over a real `wash-router` process inside a real Linux
kernel, talking to the browser over a multiport virtio-console.

```
┌────────────────────────────────── browser tab ─────────────────────────────────┐
│                                                                                │
│   index.html ── tinyemu-bridge.ts ── xterm.js ── shell.js (wash desktop UI)    │
│        │              │                              ▲                         │
│        │              │ virtio-console: vport0p{0,1,2} ───────────┐            │
│        │              ▼                                           │            │
│        │       riscvemu64-wasm.{js,wasm}   (TinyEMU JIT)          │            │
│        │              │                                           │            │
│        │     ┌────────┴────────────────── VM ──────────┐          │            │
│        │     │  OpenSBI fw_jump.bin (M-mode)           │          │            │
│        │     │  Linux 6.6 riscv64                      │          │            │
│        │     │  busybox-init → /etc/init.d/SNN…        │          │            │
│        │     │  wash-supervisor → wash-router (uid 100)│──────────┘            │
│        │     └─────────────────────────────────────────┘                       │
│        ▼                                                                       │
│   /tinyemu/wash-rootfs/blk*.bin   ← 2 MiB chunks fetched lazily by virtio-blk  │
│   /tinyemu/wash-kernel.bin         ← Linux Image (one shot at boot)            │
│   /tinyemu/opensbi-fw_jump.bin     ← SBI firmware (one shot at boot)           │
│                                                                                │
└────────────────────────────────────────────────────────────────────────────────┘
```

## Layout

```
wash-vm/
├── image/        # build pipeline for kernel + firmware + rootfs + wasm
│   ├── Makefile             # `make all install` produces everything
│   ├── firmware/Dockerfile  # OpenSBI 1.5 (banner suppressed)
│   ├── kernel/Dockerfile    # Linux 6.6.94 riscv64 (tinyconfig + fragment)
│   ├── rootfs/              # buildroot 2024.02 + wash multicall
│   │   ├── Dockerfile, build.sh, post-build.sh
│   │   ├── wash_riscv64_defconfig
│   │   └── overlay/         # /etc/init.d/, /etc/profile.d/, sudoers.d/
│   └── wasm/Dockerfile      # TinyEMU emcc build (emscripten/emsdk:3.1.74)
│
├── tinyemu/      # vendored TinyEMU C source + js/lib.js shim
│
└── web/          # the browser host: HTML + bridge + dev server
    ├── index.html
    ├── src/tinyemu-bridge.ts   # WASM lifecycle + xterm.js + virtio-console fanout
    ├── server/server.mjs       # dev server (vite middleware + /ws bus)
    ├── public/tinyemu/         # the artifacts `make install` lands here
    └── package.json
```

## Build the demo artifacts

Prerequisite: **Docker only**. The kernel + firmware + rootfs + wasm
all cross-compile inside containers. No host toolchain needed.

```sh
cd wash-vm/image
make all          # rootfs + kernel + firmware + wasm + install
                  # ~5-10 min cold; seconds on subsequent runs (Docker cache)
```

This produces and installs into `wash-vm/web/public/tinyemu/`:

| Artifact | Size | Notes |
|---|---|---|
| `wash-kernel.bin`        | ~3.5 MiB | Linux 6.6.94 Image, RV64 + virtio + ext2 + futex |
| `opensbi-fw_jump.bin`    | ~270 KiB | OpenSBI 1.5 generic, `FW_OPTIONS=0x1` (no banner) |
| `riscvemu64-wasm.{js,wasm}` | ~200 KiB | TinyEMU compiled with emscripten/emsdk:3.1.74 |
| `wash-rootfs.ext2`       | 20 MiB   | buildroot+musl, wash multicall (~11 MiB), splits into 10 × 2 MiB chunks |
| `wash-rootfs/blk*.bin`   | 10 × 2 MiB | TinyEMU drive0 chunked format, on-demand fetched by virtio-blk |

Piecewise targets if you only want to rebuild one thing: `make rootfs
| kernel | firmware | wasm | install`. See [`image/README.md`](image/README.md)
for build internals.

## Run locally

The shell bundle has to be built first (the host page imports it from
`/shell/shell.js`):

```sh
pnpm -F @wash/shell build           # produces web/shell/dist/
cd wash-vm/web
node server/server.mjs              # → http://localhost:5180
```

The dev server is a thin wrapper around Vite that adds a `/ws`
log/control bus (`server/server.mjs`). Vite handles HMR for
`src/tinyemu-bridge.ts`; the WASM, kernel, firmware, rootfs chunks
are static under `public/tinyemu/` and don't need rebuilds for
front-end iteration.

## Deploy as static HTML

The demo can be hosted as static files (GitHub Pages, Cloudflare
Pages, Netlify, S3, anything). The dev server's `/ws` bus is
optional — the bridge falls back to a backoff-and-retry loop if the
WS can't be reached, so the demo runs fine without one.

Recipe lives in [`web/README.md`](web/README.md#deploy-as-static-html).

## Future-self notes

`NEXT-SESSION.md` is the rolling "where we left off" log — pick-up
state, open issues, things to read first. Update it when you stop
mid-flow on something tricky (current entry: native `temu` boot hang
at futex_init).
