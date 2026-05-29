# wash-vm/image — build pipeline for the RISC-V demo artifacts

Produces the four artifacts the browser-host page loads:

| Artifact | Source | What it is |
|---|---|---|
| `wash-kernel.bin`        | `kernel/Dockerfile`   | Linux 6.6.94 LTS for `ARCH=riscv` — tinyconfig + `kernel/config.fragment` |
| `opensbi-fw_jump.bin`    | `firmware/Dockerfile` | OpenSBI 1.5 generic platform — `FW_OPTIONS=0x1` suppresses the banner |
| `wash-rootfs.ext2`       | `rootfs/`             | Buildroot 2024.02 + musl + busybox + sudo + haveged + wash multicall |
| `riscvemu64-wasm.{js,wasm}` | `wasm/Dockerfile`  | TinyEMU compiled with emscripten/emsdk:3.1.74; `tinyemu/js/lib.js` shim is the WASM↔JS bridge |

Everything cross-compiles inside Docker. **No host toolchain needed
beyond Docker + make**.

## Layout

```
image/
├── Makefile                    # targets: rootfs / kernel / firmware / wasm / install / all
├── firmware/
│   ├── Dockerfile              # OpenSBI v1.5, FW_OPTIONS=0x1
│   ├── aclint_mswi.c           # wash patch (futex_init workaround)
│   ├── sbi_domain.c            # wash patch (ROOT_REGION_MAX bump 16→64)
│   └── fdt_ipi_mswi.c
├── kernel/
│   ├── Dockerfile              # debian:bookworm + gcc-riscv64-linux-gnu
│   └── config.fragment         # merged on top of `make tinyconfig`
├── rootfs/
│   ├── Dockerfile              # buildroot 2024.02 inside debian:bookworm
│   ├── build.sh                # cross-compiles wash multicall, stages overlay, docker build
│   ├── post-build.sh           # runs inside the buildroot target before mkfs.ext2
│   ├── wash_riscv64_defconfig  # rv64gc + musl + busybox-init + ext2 20M
│   ├── busybox.fragment        # busybox applet selections
│   ├── users.table             # creates the `wash` non-root user
│   └── overlay/                # rsync'd onto target/ during buildroot assembly
│       └── etc/{init.d,profile.d,sudoers.d}/
└── wasm/
    └── Dockerfile              # FROM emscripten/emsdk:3.1.74; builds from ../tinyemu/
```

## Targets

```sh
make all       # rootfs + kernel + firmware + wasm + install
make rootfs    # buildroot rebuild — ~3-5 min cold (toolchain prebuilt), seconds cached
make kernel    # Linux build — ~15-25 min cold (one-time), seconds cached
make firmware  # OpenSBI build — ~30 s cold, seconds cached
make wasm      # TinyEMU emcc build — ~3-5 s once the emsdk image is pulled
make install   # copy dist/ → ../web/public/tinyemu/ with the cfg-expected names
make clean     # wipe dist/
```

`install` also runs `splitimg` on `wash-rootfs.ext2` to produce the
2 MiB chunks TinyEMU's `drive0` reads on demand (`wash-rootfs/blk*.bin`).

## What got us here

| Optimization | Effect | Where |
|---|---|---|
| `FW_OPTIONS=0x1` | Suppresses OpenSBI's ~50-line banner. Cuts ~1-1.5 s of pre-Linux wall-clock — HTIF putchar is 1 byte at a time. | `firmware/Dockerfile` |
| `post-build.sh` prunes | Deletes libstdc++/libgfortran/libgomp (toolchain auto-copies them) + offline sudo helpers. ~4 MiB saved. | `rootfs/post-build.sh` |
| `BR2_OPTIMIZE_S=y` | Buildroot `-Os` package builds. | `rootfs/wash_riscv64_defconfig` |
| 20 MiB rootfs | Used to be 16 MiB but ENOSPC bit us. 20 MiB = 10 × 2 MiB chunks, ~5 MiB free. | `rootfs/wash_riscv64_defconfig` |
| `/var/log → real dir` | Buildroot ships `/var/log → ../tmp` (space-saving symlink). Replacing with a real dir keeps the `/var/log tmpfs` fstab line from accidentally remounting /tmp. | `rootfs/post-build.sh` |

## Adding an app

1. Drop the app's BE under `apps/<name>/be/` (top-level wash repo).
2. Add `cmd/wash/imports_<name>.go` to wire it into the multicall.
3. Add the app's stamp to `MULTICALL_STAMPS` in the top-level `Makefile`.
4. Add `<name>` to the symlink lists in `rootfs/build.sh` (`/usr/bin/wash-<name>` + `/usr/lib/wash/wash-<name>`).
5. `make rootfs install`.

The `wash-launch` symlink is intentionally absent from
`/usr/lib/wash/` — it's a CLI client, not a probe-able app. See the
inline comment in `build.sh`.

## Debugging a broken build

* **rootfs fails with `Could not allocate block`** → bump
  `BR2_TARGET_ROOTFS_EXT2_SIZE` in the defconfig.
* **Docker snapshot/extract error mid-build** → `docker builder prune
  -f` and retry. Buildkit sometimes loses track of intermediate
  snapshots when layers are large.
* **Kernel boot stops at `futex hash table entries`** → that's the
  native `temu` hang documented in `NEXT-SESSION.md`. The WASM build
  doesn't hit it.
* **Demo page boots but desktop never paints** → check
  `/tmp/wash-demo-server.log` for `wash-router control socket:`. If
  it says `bind: permission denied`, something's shadowing `/tmp`
  with a non-1777 mount (we've been bitten by both `S00mounts` and
  the `/var/log → /tmp` symlink). Verify with `ls -la /` inside the
  running VM via the shell tab.
