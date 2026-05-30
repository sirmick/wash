#!/bin/sh
# Build a minimal Alpine microvm image for the wash-vm/vm harness (docs/NET.md
# §8.4). For B0 the image is an Alpine-minirootfs initramfs + a static guest
# agent, booted with Alpine's vmlinuz-virt; no apk/virt tools required on the
# host. Later phases swap to a baked qcow2 + 9p dev-share.
#
# Outputs: out/vm/vmlinuz, out/vm/initramfs.gz  (git-ignored build artifacts)
set -eu

ALPINE_VER="${ALPINE_VER:-3.23.0}"
ARCH=x86_64
MIRROR="https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/${ARCH}"

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT_DIR/out/vm"
BUILD="$OUT/build"
mkdir -p "$BUILD"

echo ">> fetching Alpine assets (cached in $BUILD)"
[ -f "$BUILD/minirootfs.tar.gz" ] || \
  curl -fsSL -o "$BUILD/minirootfs.tar.gz" "$MIRROR/alpine-minirootfs-${ALPINE_VER}-${ARCH}.tar.gz"
[ -f "$OUT/vmlinuz" ] || \
  curl -fsSL -o "$OUT/vmlinuz" "$MIRROR/netboot/vmlinuz-virt"

echo ">> building static guest agent"
CGO_ENABLED=0 GOOS=linux GOARCH="$( [ "$ARCH" = x86_64 ] && echo amd64 || echo "$ARCH" )" \
  go build -trimpath -ldflags="-s -w" -o "$BUILD/washvm-agent" "$ROOT_DIR/cmd/washvm-agent"

# The multicall wash binary (router + apps, incl. com.wash.net/.netd) is the
# real payload (docs/NET.md §8.3 — the VM serves everything). It embeds every
# FE bundle, so it can only be produced by the full web+go build: `make
# multicall`. Image building is file ops (§8.4), so we require it pre-built.
WASH_BIN="$ROOT_DIR/out/wash"
if [ ! -x "$WASH_BIN" ]; then
  echo "!! $WASH_BIN missing — run 'make multicall' first (builds the FE bundles + static multicall)" >&2
  exit 1
fi

echo ">> assembling rootfs"
RFS="$BUILD/root"
rm -rf "$RFS"
mkdir -p "$RFS"
tar -C "$RFS" -xzf "$BUILD/minirootfs.tar.gz"
install -Dm755 "$BUILD/washvm-agent" "$RFS/sbin/washvm-agent"

# Bake the multicall + materialize the per-app symlinks the router scans. The
# host binary is the same arch (amd64), so run it on the host to emit symlinks
# into the guest apps dir; symlinks are just names (→ wash).
install -Dm755 "$WASH_BIN" "$RFS/usr/lib/wash/wash"
"$WASH_BIN" install-symlinks "$RFS/usr/lib/wash" >/dev/null
ln -sf ../lib/wash/wash "$RFS/usr/bin/wash"

cat > "$RFS/init" <<'INIT'
#!/bin/sh
# wash-vm guest init (docs/NET.md §8.3): bring up the basics, launch the real
# in-guest wash-router on the DATA plane (virtio-serial /dev/vport0p1 — a raw
# chardev), and hand off to the control agent on the raw serial control plane
# (ttyS1). The kernel logs to ttyS0 (the log plane).
mount -t proc proc /proc 2>/dev/null
mount -t sysfs sys /sys 2>/dev/null
mount -t devtmpfs dev /dev 2>/dev/null

# Serve the wash UI + wire over the data port. The virtio-serial port device
# (/dev/vport0p1) only appears once the HOST connects the data chardev (the
# harness dials it in Launch), so wait generously — the router is pointless
# without it. Re-launch on exit (the host may reconnect across browser
# sessions). The router scans /usr/lib/wash for apps (com.wash.net, .netd, the
# session desktop, …); logs to /run/wash-router.log for ctl-plane inspection.
# The port is a raw chardev, so no stty/raw is needed.
mkdir -p /run
( i=0; while [ ! -e /dev/vport0p1 ] && [ $i -lt 600 ]; do i=$((i+1)); sleep 0.1; done
  while :; do
    /usr/lib/wash/wash-router \
        --transport=virtio-console:/dev/vport0p1 \
        --apps-dir=/usr/lib/wash \
        >>/run/wash-router.log 2>&1
    echo "wash-router exited rc=$? — respawn in 1s" >>/run/wash-router.log
    sleep 1
  done ) &

exec /sbin/washvm-agent /dev/ttyS1
INIT
chmod +x "$RFS/init"

echo ">> packing initramfs (root-owned: reserved ids like com.wash.netd need a"
echo "   root-owned/non-world-writable binary to be served — see registry.go)"
( cd "$RFS" && find . | cpio -o -H newc -R 0:0 2>/dev/null | gzip -9 ) > "$OUT/initramfs.gz"

echo ">> done:"
ls -lh "$OUT/vmlinuz" "$OUT/initramfs.gz"
