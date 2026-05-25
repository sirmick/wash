#!/usr/bin/env bash
# Build rootfs.ext2 for the TinyEMU RISC-V demo (buildroot pipeline).
#
# Output: $DIST/rootfs.ext2
# Env: REPO_ROOT, DIST (set by image-rv/Makefile).
#
# Flow:
#   1. Cross-compile the wash multicall binary (GOARCH=riscv64).
#   2. Stage build context: defconfig + Dockerfile + overlay with
#      wash binaries materialised as /usr/bin/wash + symlinks.
#   3. docker build runs buildroot inside the container; output is
#      /rootfs.ext2 (~5-8 MiB).
#   4. docker cp the rootfs out.

set -euo pipefail

: "${REPO_ROOT:?REPO_ROOT must be set}"
: "${DIST:?DIST must be set}"

here="$(cd -- "$(dirname "$0")" && pwd)"
work=$(mktemp -d -t wash-rootfs-rv.XXXXXX)
trap 'rm -rf "$work"' EXIT

echo "==> cross-compiling wash multicall binary (linux/riscv64)"
make -C "$REPO_ROOT" \
    GOARCH=riscv64 \
    OUT="$work/washrv64" \
    multicall \
    >/dev/null

# Sanity-check the multicall binary exists. The top-level Makefile's
# `multicall` target produces $(OUT)/wash + per-app symlinks; we only
# need the binary itself for the rootfs (we re-create symlinks below).
if [ ! -f "$work/washrv64/wash" ]; then
    echo "ERROR: $work/washrv64/wash not produced by 'make multicall'" >&2
    exit 1
fi

echo "==> staging buildroot context"
ctx="$work/ctx"
mkdir -p "$ctx" "$ctx/overlay/usr/bin" "$ctx/overlay/usr/lib/wash" \
         "$ctx/overlay/etc/init.d"

# Multicall binary at /usr/bin/wash; per-app symlinks resolve to it via
# argv[0] dispatch. One binary, N entry points — saves disk vs N
# independent binaries (~9 MiB → 9 MiB instead of N × 9 MiB).
install -m 0755 "$work/washrv64/wash" "$ctx/overlay/usr/bin/wash"

# /usr/bin entry points (the multicall dispatch table).
for app in wash-router wash-session wash-about wash-fm wash-bulk \
           wash-edit wash-settings wash-top wash-priv wash-journal \
           wash-syslogs wash-launch wash-term; do
    ln -s wash "$ctx/overlay/usr/bin/$app"
done

# /usr/lib/wash mirrors — wash-router's --apps-dir scans here.
for app in wash-session wash-about wash-fm wash-bulk wash-edit \
           wash-settings wash-top wash-priv wash-journal \
           wash-syslogs wash-launch wash-term; do
    ln -s ../../bin/wash "$ctx/overlay/usr/lib/wash/$app"
done

# Ensure proc + sys + run + tmp are mounted before any user init runs.
# Buildroot's default mountVirtFS runs from /etc/init.d/rcS but if we
# stub other scripts we still need these. Use S00 prefix to ensure
# it runs first.
cat > "$ctx/overlay/etc/init.d/S00mounts" <<'EOF'
#!/bin/sh
# wash: mount the virtual filesystems userspace needs.
[ -d /proc ] || mkdir -p /proc
[ -d /sys  ] || mkdir -p /sys
[ -d /run  ] || mkdir -p /run
[ -d /tmp  ] || mkdir -p /tmp
mountpoint -q /proc || mount -t proc     proc /proc
mountpoint -q /sys  || mount -t sysfs    sys  /sys
mountpoint -q /run  || mount -t tmpfs    tmpfs /run
mountpoint -q /tmp  || mount -t tmpfs    tmpfs /tmp
EOF
chmod 0755 "$ctx/overlay/etc/init.d/S00mounts"

# busybox-init service to start wash-router on boot.
install -m 0755 "$here/overlay/etc/init.d/S99wash-router" \
                "$ctx/overlay/etc/init.d/S99wash-router"
install -m 0755 "$here/overlay/etc/init.d/S98wash-diag" \
                "$ctx/overlay/etc/init.d/S98wash-diag"

# Boot-speed: stub out the init.d entries we don't want for the wash
# demo (no network, no CONFIG_NET in kernel; syslogd/klogd add ~3-5s
# of startup time; sysctl touches nothing useful for a single-tenant
# browser VM). Overlay-shadowed with no-op scripts.
for stub in S01syslogd S02klogd S40network S02sysctl S01seedrng; do
    cat > "$ctx/overlay/etc/init.d/$stub" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod 0755 "$ctx/overlay/etc/init.d/$stub"
done

# Banner.
cat > "$ctx/overlay/etc/motd" <<'EOF'
wash riscv64 demo (buildroot)
- wash-router: listens on /dev/hvc1 (when virtio-console is present)
- console:     hvc0 (this getty)
EOF

# Buildroot inputs.
cp "$here/wash_riscv64_defconfig" "$ctx/wash_riscv64_defconfig"
cp "$here/Dockerfile" "$ctx/Dockerfile"

echo "==> building buildroot rootfs"
echo "    (cold build: ~5-10 min; cached: seconds)"
img_tag="wash-rootfs-rv-builder:latest"
docker build -t "$img_tag" "$ctx"

echo "==> extracting rootfs.ext2"
cid=$(docker create "$img_tag")
trap 'rm -rf "$work"; docker rm -f "$cid" >/dev/null 2>&1 || true' EXIT
rm -f "$DIST/rootfs.ext2"
docker cp "$cid:/rootfs.ext2" "$DIST/rootfs.ext2"

size=$(du -h "$DIST/rootfs.ext2" | cut -f1)
echo "==> $DIST/rootfs.ext2 ($size)"
