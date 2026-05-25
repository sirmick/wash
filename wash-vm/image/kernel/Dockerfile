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
RUN apt-get update && apt-get install -y --no-install-recommends \
    bc bison build-essential ca-certificates cpio curl flex \
    gcc-riscv64-linux-gnu libelf-dev libssl-dev xz-utils \
 && rm -rf /var/lib/apt/lists/*

ARG LINUX_VER=6.6.94
ARG LINUX_MAJOR=6.x

WORKDIR /build
RUN curl -fsSL "https://cdn.kernel.org/pub/linux/kernel/v${LINUX_MAJOR}/linux-${LINUX_VER}.tar.xz" \
    | tar -xJ \
 && mv "linux-${LINUX_VER}" linux

WORKDIR /build/linux

# RISC-V's tinyconfig is already extremely lean; merge our fragment
# on top to enable virtio-blk/console + ext2 + futex + binfmt + the
# usual Go-runtime prereqs.
COPY config.fragment /tmp/config.fragment
RUN make ARCH=riscv CROSS_COMPILE=riscv64-linux-gnu- tinyconfig \
 && ./scripts/kconfig/merge_config.sh -m .config /tmp/config.fragment \
 && make ARCH=riscv CROSS_COMPILE=riscv64-linux-gnu- olddefconfig

RUN make ARCH=riscv CROSS_COMPILE=riscv64-linux-gnu- -j"$(nproc)" Image

RUN ls -lh arch/riscv/boot/Image
