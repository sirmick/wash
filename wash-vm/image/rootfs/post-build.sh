#!/usr/bin/env bash
# Buildroot post-build hook for the wash riscv64 rootfs.
#
# Trim space we can't avoid via kconfig:
#
# 1. Toolchain runtime libs the bootlin prebuilt always copies even
#    though nothing in our target links against them:
#      libstdc++.so.*     — no C++ binaries (wash is Go, sudo/nano/
#                           haveged are C, busybox is C). 1.9 MB.
#      libgfortran.so.*   — no Fortran. 1.15 MB.
#      libgomp.so.*       — no OpenMP. 200 KB.
#    pkg-toolchain-external.mk copies these unconditionally when
#    BR2_TOOLCHAIN_HAS_{CXX,FORTRAN,OPENMP}=y; there's no opt-out for
#    the lib-install step itself (only for C++ enablement, which the
#    bootlin Config.in.options force-selects). So: delete here.
#
# 2. Sudo helpers we never invoke at runtime:
#      cvtsudoers     — offline sudoers format conversion. ~270 KB
#      sudoreplay     — offline session replay. ~90 KB
#      visudo         — offline sudoers editing. ~210 KB
#      sudo_logsrvd   — central log daemon (not used). ~180 KB
#      sudo_sendlog   — log forwarder for the above. ~110 KB
#    wash-priv only needs `sudo` itself. ~860 KB total saved.
#
# 3. Strip a few stat'd-but-never-executed extras (gdb.py companion
#    file for libstdc++; tail it along with libstdc++ for tidiness).
#
# Sized to claw back ~4 MB from a stock buildroot+bootlin-riscv64+
# musl rootfs so we can drop BR2_TARGET_ROOTFS_EXT2_SIZE one or two
# 2 MiB chunks (every chunk skipped is one fewer HTTP fetch on cold
# boot).
#
# Buildroot invokes this with $1 = target dir.

set -euo pipefail

TARGET="$1"
echo "wash post-build: pruning unused toolchain libs + sudo helpers"

prune() {
    local path="$1"
    if [ -e "$TARGET/$path" ] || [ -L "$TARGET/$path" ]; then
        rm -fv "$TARGET/$path"
    fi
}

# --- 1. Unused toolchain runtime libs ---------------------------------
# Glob with shopt -s nullglob so missing patterns don't blow up the
# script (different toolchain versions ship different exact suffixes).
shopt -s nullglob
for lib in "$TARGET"/usr/lib/libstdc++.so* \
           "$TARGET"/usr/lib/libgfortran.so* \
           "$TARGET"/usr/lib/libgomp.so* \
           "$TARGET"/usr/lib/libstdc++.so.*-gdb.py; do
    rm -fv "$lib"
done
shopt -u nullglob

# --- 2. Unused sudo helpers -------------------------------------------
prune usr/bin/cvtsudoers
prune usr/bin/sudoreplay
prune usr/sbin/visudo
prune usr/sbin/sudo_logsrvd
prune usr/sbin/sudo_sendlog

# --- 3. Sanity ---------------------------------------------------------
# /usr/bin/sudo must survive — wash-priv depends on it.
if [ ! -x "$TARGET/usr/bin/sudo" ]; then
    echo "wash post-build: ERROR — /usr/bin/sudo missing after prune" >&2
    exit 1
fi

echo "wash post-build: done"
