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

# No S00mounts — buildroot's default /etc/inittab runs `mount -a`
# in sysinit (before rcS), which handles every line in /etc/fstab:
# /proc, /sys, /tmp (mode=1777), /run, /dev/pts, plus the /var/log
# tmpfs line that post-build.sh appends. Anything we mounted from a
# rcS script ran AFTER mount -a and would shadow it with the wrong
# mode (lost the 1777 on /tmp once, busted bind() for non-root).
# Filesystem table is authoritative; we just extend it.

# busybox-init service to start wash-router on boot.
install -m 0755 "$here/overlay/etc/init.d/S99wash-router" \
                "$ctx/overlay/etc/init.d/S99wash-router"
install -m 0755 "$here/overlay/etc/init.d/S98wash-diag" \
                "$ctx/overlay/etc/init.d/S98wash-diag"

# Sudo policy for the unprivileged `wash` user that wash-router
# drops to. Sudo refuses to read sudoers fragments with anything
# weaker than mode 0440, hence the explicit -m here (the default
# 0644 from the repo wouldn't pass visudo's perm check).
install -m 0440 -D "$here/overlay/etc/sudoers.d/wash-nopasswd" \
                   "$ctx/overlay/etc/sudoers.d/wash-nopasswd"

# Boot-speed: stub out init.d entries we don't want for the wash
# demo (no network → CONFIG_NET disabled in kernel; sysctl touches
# nothing useful; seedrng is redundant with haveged + the kernel's
# RANDOM_TRUST_BOOTLOADER). Overlay-shadowed with no-op scripts.
#
# Note: S01syslogd + S02klogd are NOT stubbed — busybox's own init
# scripts wire syslogd → /var/log/messages and klogd → /dev/kmsg
# forwarding. Result: every printk + syslog(3) call lands in a
# greppable log file, dmesg still works for the raw ring buffer.
for stub in S40network S02sysctl S01seedrng; do
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
cp "$here/busybox.fragment"       "$ctx/busybox.fragment"
cp "$here/users.table"            "$ctx/users.table"
cp "$here/post-build.sh"          "$ctx/post-build.sh"
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
