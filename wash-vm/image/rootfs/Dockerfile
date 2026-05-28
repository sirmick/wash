# Buildroot-based wash rootfs (riscv64).
#
# Produces /rootfs.ext2 — small (~5-8 MiB) ext2 with busybox + wash
# binaries baked in. Replaces the old alpine-edge pipeline which
# weighed in at 68 MiB (alpine-base + openrc + busybox-openrc + wash).
#
# Build time: ~5-10 min cold (the bootlin prebuilt toolchain skips
# the ~25 min gcc cross-build); seconds when fully cached. The slow
# steps cache in distinct layers so wash-binary changes only re-run
# the final make.

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        bc bison build-essential ca-certificates cpio file flex \
        git libelf-dev libssl-dev python3 rsync unzip wget xz-utils \
 && rm -rf /var/lib/apt/lists/*

# Buildroot LTS. Bump version here and rebuild to pick up upstream
# package/toolchain updates.
ARG BR_VERSION=2024.02
RUN git clone --depth=1 -b $BR_VERSION \
        https://git.buildroot.net/buildroot /buildroot
WORKDIR /buildroot

# Defconfig drives the buildroot configuration. Caching: this layer
# only changes when the defconfig does.
COPY wash_riscv64_defconfig configs/wash_defconfig
COPY busybox.fragment /input/busybox.fragment
COPY users.table /input/users.table
RUN make wash_defconfig

# Wash binaries + init script overlay. This is the layer that changes
# most often — kept last so the toolchain layers above stay cached.
# build.sh stages the wash multicall binary + symlinks into ./overlay/.
COPY overlay /overlay

# Build. Buildroot does: fetch bootlin toolchain, build busybox, rsync
# /overlay over staged target/, mkfs.ext2 → output/images/rootfs.ext2.
# The overlay path is in the defconfig — env vars at make-time are
# too late once `make wash_defconfig` has produced .config.
RUN make -j"$(nproc)" \
 && cp output/images/rootfs.ext2 /rootfs.ext2 \
 && ls -lh /rootfs.ext2 \
 && ls -la output/target/usr/bin/wash 2>/dev/null && echo "wash binary present in target" \
    || echo "WARNING: wash binary missing from target"
