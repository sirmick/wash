# wash-vm — wash inside a browser-tab RISC-V Linux VM

> **Live demo:** [sirmick.github.io/wash](https://sirmick.github.io/wash/)
> — auto-built from this directory by [`.github/workflows/demo.yml`](../.github/workflows/demo.yml)
> on every push to `main`.

`wash-vm/` packages everything needed to run wash inside a real Linux
VM (kernel + rootfs + emulator) in a browser tab. The emulator is
vendored TinyEMU compiled to WASM; the kernel is upstream Linux 6.6
LTS for RISC-V; the rootfs is a buildroot+musl userland with the wash
multicall binary baked in.

End result: open a page, log in, and ~3 seconds later you have a working
wash desktop running over a real `wash-router` process (forked by the
`wash-vmlogin` login front after auth) inside a real Linux kernel, talking
to the browser over a multiport virtio-console.

> This dir is the **in-browser** surface. The sibling **wemu** surface
> (host QEMU microvm) lives in [`vm/`](vm/) + [`docs/NET.md §8`](../docs/NET.md);
> both share the login front + wire and are launched the same way — see
> **Build & run** below and [`UNIFY.md`](UNIFY.md).

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
| `wash-rootfs.squashfs`   | ~7 MiB   | buildroot+musl, wash multicall + wash-vmlogin, wash shadow pw; split into 512 KiB chunks |
| `wash-rootfs/blk*.bin`   | N × 512 KiB | TinyEMU drive0 chunked format, on-demand fetched by virtio-blk |

Piecewise targets if you only want to rebuild one thing: `make rootfs
| kernel | firmware | wasm | install`. See [`image/README.md`](image/README.md)
for build internals.

## Build & run

There are **two VM surfaces**, both serving the same wash UI on **:13000**
(run one at a time), both gated by a login (**`wash` / `wash`**, real
`su`/shadow auth — see [`UNIFY.md`](UNIFY.md)):

| Surface | Launch | What it is |
|---|---|---|
| **In-browser** (TinyEMU RISC-V, WASM) | `wash-vm/run-browser.sh` | boots the VM in the tab; dev server hosts the page + artifacts |
| **wemu** (host QEMU x86 microvm) | `wash-vm/run-qemu.sh` | host `qemu` + a proxy that tunnels the wire (`docs/NET.md §8`) |

```sh
# in-browser VM  → http://localhost:13000   (login wash / wash)
wash-vm/run-browser.sh          # builds the shell, starts the dev server (0.0.0.0:13000)

# wemu QEMU VM   → http://localhost:13000   (login wash / wash)
wash-vm/run-qemu.sh             # builds image+chrome+runner, boots qemu + proxy
wash-vm/run-qemu.sh -smp 2 -m 2048   # extra args pass straight through to qemu
```

Both bind `0.0.0.0` (LAN-reachable; override with `PORT=`/`ADDR=`). In both, the
in-guest **`wash-vmlogin`** front authenticates, then forks **`wash-router`** as
the user; `shell.js` + every app bundle stream from the VM over the channel —
the host only serves a minimal bootstrap.

### Cutting the images

The run scripts (re)build incrementally; to cut an image explicitly:

```sh
make vm                  # RISC-V: kernel + firmware + rootfs + wasm → wash-vm/web/public/tinyemu/
                         #   (needs Docker; rootfs bakes the multicall + wash-vmlogin + users.table pw)
make vm-image vm-chrome  # wemu: Alpine initramfs + host chrome → out/vm/, out/vm-chrome/
                         #   (needs Docker; out/washvm-run is the proxy runner)
```

The in-browser dev server is a thin Vite wrapper with a `/ws` log/control bus
(`web/server/server.mjs`); it HMRs `src/tinyemu-bridge.ts`, while the WASM/
kernel/rootfs chunks under `public/tinyemu/` only change when you `make vm`.

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
