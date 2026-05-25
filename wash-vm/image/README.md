# wash RISC-V image pipeline (image-rv)

Sibling of `image/`. Produces a Linux 6.6 LTS kernel + buildroot-musl
riscv64 rootfs (wash binaries baked in) for the TinyEMU/jslinux
RISC-V demo (`web/demo/tinyemu.html`).

## Layout

```
image-rv/
├── Makefile                         # make rootfs / kernel / all / install
├── kernel/
│   ├── Dockerfile                   # Linux 6.6.94, ARCH=riscv, cross-build
│   └── config.fragment              # tinyconfig + virtio + ext2 + Go prereqs
└── rootfs/
    ├── Dockerfile                   # buildroot 2024.02 + wash multicall binary
    ├── build.sh                     # cross-compile wash → overlay → mkfs
    ├── wash_riscv64_defconfig       # buildroot config (rv64gc + musl + busybox)
    └── overlay/etc/init.d/
        └── S99wash-router           # busybox-init service for wash-router
```

## Usage

```sh
# one command from the repo root:
make rv

# or piecewise from image-rv/:
make all           # rootfs + kernel + install into web/demo/public/tinyemu/
make rootfs        # buildroot rebuild (~5-10 min cold, seconds cached)
make kernel        # kernel rebuild (~20 min cold, seconds cached)
make install       # copy dist/ → web/demo/public/tinyemu/{wash-kernel.bin,wash-rootfs.ext2}
```

Output sizes (target: total demo payload <10 MiB):

| Artifact | Size |
|---|---|
| `wash-kernel.bin` (Linux Image) | ~2.3 MiB |
| `wash-rootfs.ext2` (buildroot + wash multicall) | ~5-8 MiB |
| `bbl64.bin` (vendored Bellard) | 53 KiB |
| `riscvemu64-wasm.wasm` (vendored TinyEMU) | 221 KiB |

## What's different from `image/`

| Topic | image (i686) | image-rv (riscv64) |
|---|---|---|
| Kernel arch | `ARCH=x86` → bzImage | `ARCH=riscv` → Image |
| Cross toolchain | host gcc-multilib | gcc-riscv64-linux-gnu (in Docker) |
| Firmware | SeaBIOS (`seabios.bin`) | BBL (`bbl64.bin` from Bellard) |
| Kernel console | `console=ttyS0` (8250) | `console=hvc0` (HVC over SBI) |
| Router transport | `/dev/hvc0` (virtio-console port 0) | `/dev/hvc1` (virtio-console port 1; hvc0 is the kernel console) |
| Wash binaries | `GOARCH=386 GO386=softfloat` | `GOARCH=riscv64` |
| Alpine port | `--arch=x86` | `--arch=riscv64` |

## Demo wiring

`web/demo/tinyemu.html` is the demo page. The serve-side TinyEMU
config (`web/demo/public/tinyemu/buildroot-riscv64.cfg`) needs to
point its `kernel:`, `bios:`, and `drive0:` (block device) at the
files we install here.

Once `make all` lands the artifacts, edit the `.cfg` to:

```
{
    version: 1,
    machine: "riscv64",
    memory_size: 256,
    bios: "bbl64.bin",
    kernel: "/tinyemu-rv/Image",
    cmdline: "console=hvc0 root=/dev/vda rootfstype=ext2 ro",
    drive0: { file: "/tinyemu-rv/rootfs.ext2" },
}
```

## State

This pipeline is **scaffolded but unbuilt**. The kernel and rootfs
builds have NOT been run end-to-end yet — gating items:

- Confirm wash binaries cross-compile cleanly for `GOARCH=riscv64`.
- First `make kernel` run will surface any RISC-V config issues.
- TinyEMU may need its `drive0:` option exercised differently than
  the v86 `hda:` model.

Build when you're ready to iterate.
