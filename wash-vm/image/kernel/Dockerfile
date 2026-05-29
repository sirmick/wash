# Linux 6.6 LTS for RISC-V (riscv64). Built from tinyconfig + our
# fragment, cross-compiled inside Debian with gcc-riscv64-linux-gnu.
#
# Output: /build/linux/arch/riscv/boot/Image — flat kernel image
# format expected by BBL (Bellard's bbl64.bin) and OpenSBI.
#
# Differences from image/kernel/Dockerfile (x86):
#   - ARCH=riscv (not i386)
#   - cross toolchain: gcc-riscv64-linux-gnu (not host gcc-multilib)
#   - boot artifact: arch/riscv/boot/Image (not arch/x86/boot/bzImage)
#   - smaller default config — RISC-V tinyconfig is already minimal

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
# linux-libc-dev-riscv64-cross gives us riscv64 kernel headers
# (sys/mount.h, linux/reboot.h, asm-generic/*) needed to compile our
# initramfs /init binary against the cross-libc sysroot.
RUN apt-get update && apt-get install -y --no-install-recommends \
    bc bison build-essential ca-certificates cpio curl flex \
    gcc-riscv64-linux-gnu libc6-dev-riscv64-cross linux-libc-dev-riscv64-cross \
    libelf-dev libssl-dev xz-utils \
 && rm -rf /var/lib/apt/lists/*

ARG LINUX_VER=6.6.94
ARG LINUX_MAJOR=6.x

WORKDIR /build
RUN curl -fsSL "https://cdn.kernel.org/pub/linux/kernel/v${LINUX_MAJOR}/linux-${LINUX_VER}.tar.xz" \
    | tar -xJ \
 && mv "linux-${LINUX_VER}" linux

# ─── initramfs init binary ─────────────────────────────────────────────
# Static-linked riscv64 binary that PID 1 runs from the kernel's
# baked-in initramfs. It mounts squashfs+tmpfs+overlay and pivots
# into the real rootfs (see init.c for the full story).
#
# Static linking keeps the initramfs self-contained — no /lib
# dependency, no ld-musl in the cpio. Result is ~700 KiB with glibc
# static; we accept that vs the complexity of bringing in a musl
# toolchain just for this binary.
COPY init.c /build/init.c
RUN riscv64-linux-gnu-gcc -Os -static -s -o /build/init /build/init.c \
 && ls -lh /build/init

# Pack /init into a one-file cpio newc archive at /build/initramfs.cpio.
# The kernel build picks it up via CONFIG_INITRAMFS_SOURCE which our
# config.fragment points at this path.
RUN mkdir -p /build/initramfs-stage \
 && cp /build/init /build/initramfs-stage/init \
 && chmod 0755 /build/initramfs-stage/init \
 && (cd /build/initramfs-stage && \
     echo "./init" | cpio -o -H newc -R 0:0 > /build/initramfs.cpio) \
 && ls -lh /build/initramfs.cpio

WORKDIR /build/linux

# RISC-V's tinyconfig is already extremely lean; merge our fragment
# on top to enable virtio-blk/console + squashfs + overlayfs + futex
# + binfmt + the usual Go-runtime prereqs.
COPY config.fragment /tmp/config.fragment
RUN make ARCH=riscv CROSS_COMPILE=riscv64-linux-gnu- tinyconfig \
 && ./scripts/kconfig/merge_config.sh -m .config /tmp/config.fragment \
 && make ARCH=riscv CROSS_COMPILE=riscv64-linux-gnu- olddefconfig

RUN make ARCH=riscv CROSS_COMPILE=riscv64-linux-gnu- -j"$(nproc)" Image

RUN ls -lh arch/riscv/boot/Image
